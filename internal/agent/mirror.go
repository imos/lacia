package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
	"golang.org/x/sys/unix"
)

const mirrorRescue = "local/mirror-recovery"
const mirrorPending = mirrorRescue + "/pending.json"

// MirrorResult describes the last verified fast-forward and any saved local changes.
type MirrorResult struct {
	Revision  string
	Backup    string
	LocalOnly []string
}

type mirrorGit struct {
	ctx  context.Context
	dir  string
	root *disk.Dir
}

type mirrorFile struct {
	Present bool
	Mode    uint32
	SHA256  string
}

type mirrorEntry struct {
	Path      string
	Tracked   bool
	Index     string
	BaseIndex string
	Work      mirrorFile
	BaseWork  mirrorFile
	Saved     string
	LocalOnly bool
}

type mirrorJournal struct {
	Base, Target string
	Backup       string
	Entries      []mirrorEntry
	Patches      map[string]string
}

// SyncMirror fetches origin/main and fast-forwards the read-only copy. A nonempty
// revision is the commit advertised by the server, which must already be published.
func SyncMirror(ctx context.Context, dir, revision string) (MirrorResult, error) {
	g, release, err := openMirror(ctx, dir)
	if err != nil {
		return MirrorResult{}, err
	}
	defer release()
	if revision != "" && !mirrorOID(revision) {
		return MirrorResult{}, errors.New("サーバーのコミットIDを確かめられません。")
	}
	if err := g.checkWorktree(); err != nil {
		return MirrorResult{}, err
	}
	if _, err := g.run("fetch", "--quiet", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"); err != nil {
		return MirrorResult{}, fmt.Errorf("サーバーの main を取得できませんでした: %w", err)
	}
	target, err := g.commit("refs/remotes/origin/main")
	if err != nil {
		return MirrorResult{}, err
	}
	if revision != "" && !g.ancestor(revision, target) {
		return MirrorResult{}, errors.New("サーバーが示した版の公開を待っています。")
	}
	head, err := g.commit("HEAD")
	if err != nil {
		return MirrorResult{}, err
	}
	if !g.ancestor(head, target) {
		return MirrorResult{}, errors.New("読み取り専用のコピーとサーバーの履歴が分かれているため、同期を停止しました。")
	}
	for _, rev := range []string{head, target} {
		if err := g.checkLocalTree(rev); err != nil {
			return MirrorResult{}, err
		}
	}
	pending, err := g.pending()
	if err != nil {
		return MirrorResult{}, err
	}
	if pending != nil {
		if err := g.verifyBackup(pending); err != nil {
			return MirrorResult{}, err
		}
		switch {
		case g.ancestor(pending.Target, head):
			return g.fastForward(head, head, pending)
		case head == pending.Base && g.ancestor(pending.Target, target):
			// Finish the interrupted update against its original target. A later
			// pass can check for conflicts with commits published in the meantime.
			if err := g.checkLocalTree(pending.Target); err != nil {
				return MirrorResult{}, err
			}
			return g.apply(head, pending)
		default:
			return MirrorResult{}, errors.New("前回の退避以降の履歴を照合できませんでした。")
		}
	}
	if head == target {
		return MirrorResult{Revision: head}, nil
	}
	journal, err := g.plan(head, target)
	if err != nil {
		return MirrorResult{}, err
	}
	if len(journal.Entries) == 0 {
		return g.fastForward(head, target, nil)
	}
	if err := g.saveBackup(journal); err != nil {
		return MirrorResult{}, err
	}
	return g.apply(head, journal)
}

// AcknowledgeMirrorRecovery clears the pending report after its backup and
// local-only paths have been recorded by the caller. The backup itself remains.
func AcknowledgeMirrorRecovery(dir, backup string) error {
	g, release, err := openMirror(context.Background(), dir)
	if err != nil {
		return err
	}
	defer release()
	current, err := g.pending()
	if err != nil || current == nil {
		return err
	}
	if filepath.Join(g.dir, current.Backup) != backup {
		return errors.New("保存済みの退避先が前回の復旧記録と一致しません。")
	}
	head, err := g.commit("HEAD")
	if err != nil {
		return err
	}
	if !g.ancestor(current.Target, head) {
		return errors.New("退避のあとでサーバーの更新を取り込めたか、確かめられません。")
	}
	return g.root.RemoveFile(mirrorPending)
}

func openMirror(ctx context.Context, dir string) (mirrorGit, func(), error) {
	var g mirrorGit
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return g, nil, err
	}
	root, err := disk.OpenRecords(absolute)
	if err != nil {
		return g, nil, err
	}
	g = mirrorGit{ctx, absolute, root}
	gitInfo, err := root.Lstat(".git")
	if err != nil || !gitInfo.IsDir() {
		root.Close()
		return g, nil, errors.New("手元の読み取り専用のコピーの Git ディレクトリーを確かめられません。")
	}
	marker, err := g.run("config", "--local", "--get-all", "remote.origin.pushurl")
	if err != nil || strings.TrimSpace(string(marker)) != "disabled://lacia-local-readonly" {
		root.Close()
		return g, nil, errors.New("対象が手元の読み取り専用のコピーであることを確かめられません。")
	}
	lock, err := root.OpenLock(".git/lacia-mirror.lock", 0600)
	if err != nil {
		root.Close()
		return g, nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		root.Close()
		return g, nil, fmt.Errorf("別の同期処理が進行中です: %w", err)
	}
	return g, func() {
		lock.Close()
		root.Close()
	}, nil
}

func (g mirrorGit) run(args ...string) ([]byte, error) {
	prefix := []string{"-C", g.dir}
	if args[0] != "check-ignore" {
		prefix = append(prefix, "--literal-pathspecs")
	}
	cmd := program.Git().Command(g.ctx, append(prefix, args...)...)
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		switch name {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_NAMESPACE",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_TERMINAL_PROMPT":
			continue
		}
		cmd.Env = append(cmd.Env, env)
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	return cmd.Output()
}

func (g mirrorGit) checkWorktree() error {
	branch, err := g.run("symbolic-ref", "--quiet", "HEAD")
	if err != nil || strings.TrimSpace(string(branch)) != "refs/heads/main" {
		return errors.New("読み取り専用のコピーで main が選ばれていません。")
	}
	for _, path := range []string{
		"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_START", "sequencer",
		"rebase-apply", "rebase-merge", "index.lock", "HEAD.lock", "refs/heads/main.lock",
	} {
		if _, err := g.root.Lstat(".git/" + path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("読み取り専用のコピーで別の Git 操作が進行中のため、同期を停止しました。")
		}
	}
	return nil
}

func (g mirrorGit) commit(rev string) (string, error) {
	out, err := g.run("rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if !mirrorOID(id) {
		return "", errors.New("Git のコミットIDを確かめられません。")
	}
	return id, nil
}

func mirrorOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (g mirrorGit) ancestor(before, after string) bool {
	_, err := g.run("merge-base", "--is-ancestor", before, after)
	return err == nil
}

func (g mirrorGit) plan(head, target string) (*mirrorJournal, error) {
	j := &mirrorJournal{Base: head, Target: target}
	changed, err := g.run("diff", "--name-only", "-z", "--no-renames", head, target, "--")
	if err != nil {
		return nil, err
	}
	targetPaths := mirrorNames(changed)
	for i := range targetPaths {
		targetPaths[i] = strings.ToLower(targetPaths[i])
	}
	for _, cmd := range [][]string{
		{"diff", "--name-only", "-z", "--no-renames", "HEAD", "--"},
		{"diff", "--cached", "--name-only", "-z", "--no-renames", "HEAD", "--"},
		{"ls-files", "-z", "--others", "--exclude-standard"},
	} {
		names, err := g.run(cmd...)
		if err != nil {
			return nil, err
		}
		tracked := cmd[0] == "diff"
		for _, path := range mirrorNames(names) {
			if !collides(path, targetPaths) || slices.ContainsFunc(j.Entries, func(e mirrorEntry) bool { return e.Path == path }) {
				continue
			}
			entry, err := g.inspectFile(path, tracked, head, target)
			if err != nil {
				return nil, fmt.Errorf("%s の退避を準備できませんでした: %w", path, err)
			}
			j.Entries = append(j.Entries, entry)
		}
	}
	return j, nil
}

func mirrorNames(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

func collides(path string, targetPaths []string) bool {
	path = strings.ToLower(path)
	for _, next := range targetPaths {
		if path == next || strings.HasPrefix(path, next+"/") || strings.HasPrefix(next, path+"/") {
			return true
		}
	}
	return false
}

func (g mirrorGit) inspectFile(path string, tracked bool, head, target string) (mirrorEntry, error) {
	entry := mirrorEntry{Path: path, Tracked: tracked}
	work, _, err := g.workFile(path)
	if err != nil {
		return entry, err
	}
	entry.Work = work
	stage, err := g.run("ls-files", "--stage", "-z", "--", path)
	if err != nil {
		return entry, err
	}
	entry.Index = string(stage)
	base, err := g.treeFile(head, path)
	if err != nil {
		return entry, err
	}
	if base.oid != "" {
		entry.BaseIndex = base.mode + " " + base.oid + " 0\t" + path + "\x00"
		entry.BaseWork = base.data
	}
	next, err := g.treeFile(target, path)
	if err != nil {
		return entry, err
	}
	if work != next.data {
		entry.LocalOnly = true
	}
	if entry.Index != entry.BaseIndex {
		targetIndex := ""
		if next.oid != "" {
			targetIndex = next.mode + " " + next.oid + " 0\t" + path + "\x00"
		}
		entry.LocalOnly = entry.LocalOnly || entry.Index != targetIndex
	}
	return entry, nil
}

type mirrorTreeFile struct {
	mode, oid string
	data      mirrorFile
}

func (g mirrorGit) treeFile(rev, path string) (mirrorTreeFile, error) {
	out, err := g.run("ls-tree", "-z", rev, "--", path)
	if err != nil || len(out) == 0 {
		return mirrorTreeFile{}, err
	}
	info, name, found := strings.Cut(strings.TrimSuffix(string(out), "\x00"), "\t")
	fields := strings.Fields(info)
	if !found || name != path || len(fields) != 3 {
		return mirrorTreeFile{}, errors.New("Git に記録されたファイルの情報を照合できません。")
	}
	mode, kind, oid := fields[0], fields[1], fields[2]
	if kind == "tree" {
		return mirrorTreeFile{}, nil
	}
	if kind != "blob" || (mode != "100644" && mode != "100755") || !mirrorOID(oid) {
		return mirrorTreeFile{}, errors.New("通常のファイル以外に競合があるため、同期を停止しました。")
	}
	content, err := g.run("cat-file", "blob", oid)
	return mirrorTreeFile{mode, oid, describeMirrorFile(content, mode == "100755")}, err
}

func (g mirrorGit) workFile(path string) (mirrorFile, []byte, error) {
	if err := g.checkPath(path); err != nil {
		return mirrorFile{}, nil, err
	}
	info, err := g.root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return mirrorFile{}, nil, nil
	}
	if err != nil {
		return mirrorFile{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return mirrorFile{}, nil, errors.New("通常のファイル以外に競合があります。")
	}
	content, err := g.root.ReadFile(path)
	return describeMirrorFile(content, info.Mode()&0100 != 0), content, err
}

func describeMirrorFile(content []byte, executable bool) mirrorFile {
	mode := uint32(0644)
	if executable {
		mode = 0755
	}
	return mirrorFile{true, mode, mirrorDigest(content)}
}

func mirrorDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (g mirrorGit) checkPath(path string) error {
	if path == "." || !utf8.ValidString(path) || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return errors.New("コピー内のパスを照合できません。")
	}
	for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
		if info, err := g.root.Lstat(parent); err == nil && !info.IsDir() {
			return errors.New("パスの途中に、通常のディレクトリーではない項目があります。")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (g mirrorGit) checkLocalTree(revision string) error {
	out, err := g.run("ls-tree", "-z", "--name-only", revision)
	if err != nil || slices.ContainsFunc(mirrorNames(out), func(path string) bool {
		return strings.EqualFold(path, "local")
	}) {
		return errors.New("local/ が Git の管理外にあることを確かめられません。")
	}
	return nil
}

func (g mirrorGit) saveBackup(j *mirrorJournal) error {
	if _, err := g.run("check-ignore", "--quiet", "--", mirrorPending); err != nil {
		return errors.New("退避先が Git の管理から除外されていることを確かめられません。")
	}
	if err := g.checkPath(mirrorPending); err != nil {
		return err
	}
	backup, err := g.root.MkdirTemp(mirrorRescue, "rescue-")
	if err != nil {
		return err
	}
	j.Backup = backup
	tracked := []string{}
	for i := range j.Entries {
		e := &j.Entries[i]
		if e.Tracked {
			tracked = append(tracked, e.Path)
		}
		got, data, err := g.workFile(e.Path)
		if err != nil || got != e.Work {
			return fmt.Errorf("退避中に %s の状態が変わりました。", e.Path)
		}
		if got.Present {
			e.Saved = fmt.Sprintf("work-%d", i)
			if err := g.writeSaved(filepath.Join(j.Backup, e.Saved), data); err != nil {
				return err
			}
		}
	}
	j.Patches = map[string]string{}
	if len(tracked) > 0 {
		for _, patch := range []struct {
			name string
			args []string
		}{
			{"working-tree.patch", []string{"diff", "--no-ext-diff", "--no-textconv", "--binary", "HEAD", "--"}},
			{"index.patch", []string{"diff", "--cached", "--no-ext-diff", "--no-textconv", "--binary", "HEAD", "--"}},
		} {
			data, err := g.run(append(patch.args, tracked...)...)
			if err != nil {
				return err
			}
			if err := g.writeSaved(filepath.Join(j.Backup, patch.name), data); err != nil {
				return err
			}
			j.Patches[patch.name] = mirrorDigest(data)
		}
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := g.writeSaved(filepath.Join(j.Backup, "manifest.json"), data); err != nil {
		return err
	}
	if err := g.verifyBackup(j); err != nil {
		return err
	}
	return g.writeSaved(mirrorPending, data)
}

func (g mirrorGit) writeSaved(path string, data []byte) error {
	if err := g.root.WriteFile(path, data, 0600); err != nil {
		return err
	}
	roundTrip, err := g.root.ReadFile(path)
	if err != nil || !bytes.Equal(roundTrip, data) {
		return errors.New("退避したファイルの内容を読み戻せませんでした。")
	}
	return nil
}

func (g mirrorGit) pending() (*mirrorJournal, error) {
	if err := g.checkPath(mirrorPending); err != nil {
		return nil, err
	}
	info, err := g.root.Lstat(mirrorPending)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("同期の再開に必要な退避記録を読み込めません。")
	}
	raw, err := g.root.ReadFile(mirrorPending)
	if err != nil {
		return nil, err
	}
	j := new(mirrorJournal)
	if err := json.Unmarshal(raw, j); err != nil ||
		!mirrorOID(j.Base) || !mirrorOID(j.Target) ||
		filepath.Dir(j.Backup) != mirrorRescue || !strings.HasPrefix(filepath.Base(j.Backup), "rescue-") {
		return nil, errors.New("同期の再開に必要な退避記録を照合できません。")
	}
	saved, err := g.root.ReadFile(filepath.Join(j.Backup, "manifest.json"))
	if err != nil || !bytes.Equal(saved, raw) {
		return nil, errors.New("同期の退避記録が保存時と一致しません。")
	}
	return j, nil
}

func (g mirrorGit) verifyBackup(j *mirrorJournal) error {
	verify := func(name, expected string) error {
		if name == "." || name != filepath.Base(name) {
			return errors.New("退避ファイル名を照合できません。")
		}
		path := filepath.Join(j.Backup, name)
		if err := g.checkPath(path); err != nil {
			return err
		}
		info, err := g.root.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("退避ファイルの本体を照合できません。")
		}
		content, err := g.root.ReadFile(path)
		if err != nil || mirrorDigest(content) != expected {
			return errors.New("退避ファイルの内容が一致しません。")
		}
		return nil
	}
	for _, e := range j.Entries {
		if e.Work.Present {
			if err := verify(e.Saved, e.Work.SHA256); err != nil {
				return err
			}
		}
	}
	for path, digest := range j.Patches {
		if err := verify(path, digest); err != nil {
			return err
		}
	}
	return nil
}

func (g mirrorGit) apply(head string, j *mirrorJournal) (MirrorResult, error) {
	if err := g.checkHead(head); err != nil {
		return MirrorResult{}, err
	}
	if err := g.verifyBackup(j); err != nil {
		return MirrorResult{}, err
	}
	tracked, untracked := []string{}, []string{}
	// Any item may already be restored if the process was interrupted.
	for _, e := range j.Entries {
		if top, _, _ := strings.Cut(e.Path, "/"); strings.EqualFold(top, ".git") || strings.EqualFold(top, "local") {
			return MirrorResult{}, errors.New("退避したパスは同期の対象にできません。")
		}
		work, _, err := g.workFile(e.Path)
		if err != nil || (work != e.Work && work != e.BaseWork) {
			return MirrorResult{}, fmt.Errorf("退避後に %s が変わりました。", e.Path)
		}
		index, err := g.run("ls-files", "--stage", "-z", "--", e.Path)
		if err != nil || (string(index) != e.Index && string(index) != e.BaseIndex) {
			return MirrorResult{}, fmt.Errorf("退避後に Git に記録された %s の状態が変わりました。", e.Path)
		}
		if e.Tracked {
			if work != e.BaseWork || string(index) != e.BaseIndex {
				tracked = append(tracked, e.Path)
			}
		} else if work.Present {
			untracked = append(untracked, e.Path)
		}
	}
	if len(tracked) > 0 {
		if _, err := g.run(append([]string{"restore", "--source", head, "--staged", "--worktree", "--"}, tracked...)...); err != nil {
			return MirrorResult{}, fmt.Errorf("退避済みの変更を取り除けませんでした: %w", err)
		}
	}
	for _, path := range untracked {
		// Only the verified non-directory entries in the journal reach this path.
		if err := g.root.RemoveFile(path); err != nil {
			return MirrorResult{}, err
		}
	}
	return g.fastForward(head, j.Target, j)
}

func (g mirrorGit) checkHead(expected string) error {
	if err := g.checkWorktree(); err != nil {
		return err
	}
	got, err := g.commit("HEAD")
	if err != nil || got != expected {
		return errors.New("同期中に読み取り専用のコピーの版が変更されました。")
	}
	return nil
}

func (g mirrorGit) fastForward(head, target string, j *mirrorJournal) (MirrorResult, error) {
	if err := g.checkHead(head); err != nil {
		return MirrorResult{}, err
	}
	if head != target {
		if _, err := g.run("merge", "--ff-only", "--no-overwrite-ignore", "--quiet", target); err != nil {
			return MirrorResult{}, fmt.Errorf("サーバーの更新を早送りで取り込めませんでした: %w", err)
		}
	}
	if err := g.checkHead(target); err != nil {
		return MirrorResult{}, err
	}
	result := MirrorResult{Revision: target}
	if j != nil {
		result.Backup = filepath.Join(g.dir, j.Backup)
		for _, e := range j.Entries {
			if e.LocalOnly {
				result.LocalOnly = append(result.LocalOnly, e.Path)
			}
		}
	}
	return result, nil
}

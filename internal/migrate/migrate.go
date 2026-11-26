// Package migrate advances record repositories between layouts and preserves
// links to records that move. Everything is planned and checked in memory first; the steps
// are then written one by one to a journal under local/, which Undo reads
// backwards to put the repository back. See docs/design/records.md.
package migrate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/imos/lacia/internal/mdlink"
	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/todo"
)

// JournalFile is where the steps are written as they are done.
const JournalFile = "local/migrate-journal.jsonl"

// Options says what to migrate and how.
type Options struct {
	Root     string
	Moves    string // the file of extra moves, or ""
	DryRun   bool
	ReadOnly bool // the repository is a read-only copy; nothing may be done
	Now      time.Time
}

// step moves one path. Git says whether git mv does it; a path without
// tracked files is renamed. Either way, the files outside Git in a
// directory (files/ of the tasks) go along.
type step struct {
	From, To string
	Git      bool
}

// Plan is everything the migration will do, worked out before anything is
// written.
type Plan struct {
	root    string
	format  int
	steps   []step   // in order: extra moves, areas, files of the system
	removes []string // extra removals, as paths of format 0
	// writes are the new contents of files, by their path after the moves.
	writes map[string][]byte
	// histories are the log.jsonl to create, by the path of the log.md
	// they replace, after the moves.
	histories map[string][]byte
	todo      bool

	Stats Stats
	// Attention lists what a person should read: links to removed files,
	// paths left without areas/, and links whose target was missing.
	Attention []string
}

// Stats counts what the migration does.
type Stats struct {
	Moves, MovedFiles, Removed          int
	Links, Paths                        int
	Histories, Events, Blocks, Headings int
}

// Move is one line of the file of extra moves.
type Move struct {
	From, To string // To is "" for a removal
}

// ReadMoves reads the file of extra moves: "from<TAB>to" per line, "-" as
// the destination for a removal, blank lines and "#" comments skipped.
func ReadMoves(file string) ([]Move, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var out []Move
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		from, to, ok := strings.Cut(line, "\t")
		from, to = clean(from), strings.TrimSpace(to)
		if !ok || from == "" || to == "" {
			return nil, fmt.Errorf("%s の%d行目が「もとの場所、タブ、新しい場所」の形になっていません", file, n+1)
		}
		if to == "-" {
			to = ""
		} else {
			to = clean(to)
		}
		out = append(out, Move{from, to})
	}
	return out, nil
}

func clean(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(p)), "/")
}

func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s が失敗しました: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// check is M1: the repository can be migrated, is clean, not served, writable,
// and not in the middle of a migration.
func check(o Options) (task.Config, error) {
	cfg, err := task.LoadConfig(o.Root)
	if err != nil {
		return cfg, err
	}
	switch {
	case cfg.Format >= task.LatestFormat:
		return cfg, fmt.Errorf("この記録は、すでに形式%dです", cfg.Format)
	case o.ReadOnly:
		return cfg, errors.New("ここは読み取り専用のコピーです。記録の形式を変えるには、サーバーが使う正本で lacia migrate を実行してください")
	}
	if err := checkNotServed(o.Root); err != nil {
		return cfg, err
	}
	if _, err := os.Stat(filepath.Join(o.Root, filepath.FromSlash(JournalFile))); err == nil {
		return cfg, fmt.Errorf("%s があります。前の移行を lacia migrate --undo で戻すか、片づけてから実行してください", JournalFile)
	}
	status, err := git(o.Root, "status", "--short")
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(status) != "" {
		return cfg, errors.New("コミットしていない変更があります。git status --short が空になってから実行してください")
	}
	return cfg, nil
}

func checkNotServed(root string) error {
	gitDir := filepath.Join(root, ".git")
	info, err := os.Stat(gitDir)
	if os.IsNotExist(err) || err == nil && !info.IsDir() {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(gitDir, "lacia-server")); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return errors.New("サーバーが動いています（.git/lacia-server があります）。止めてから記録の形式を変えてください")
	}
	return nil
}

// Make works out the plan and checks it. Nothing is written.
func Make(o Options) (*Plan, error) {
	cfg, err := check(o)
	if err != nil {
		return nil, err
	}
	var moves []Move
	if o.Moves != "" {
		if moves, err = ReadMoves(o.Moves); err != nil {
			return nil, err
		}
	}
	listed, err := git(o.Root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var tracked []string
	for _, f := range strings.Split(listed, "\x00") {
		if f != "" {
			tracked = append(tracked, f)
		}
	}
	p := &Plan{root: o.Root, format: cfg.Format + 1, writes: map[string][]byte{}, histories: map[string][]byte{}}
	if p.format == task.AreaRoutineFormat {
		if len(moves) > 0 {
			return nil, errors.New("形式2への移行では --moves は使えません")
		}
		if err := p.planAreaRoutines(cfg, tracked); err != nil {
			return nil, err
		}
		return p, nil
	}
	if err := p.planMoves(cfg, moves, tracked); err != nil {
		return nil, err
	}
	if err := p.planContents(cfg, tracked, o.Now); err != nil {
		return nil, err
	}
	return p, nil
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// planMoves is M2 to M4, as a list of moves on paths of format 0.
func (p *Plan) planMoves(cfg task.Config, moves []Move, tracked []string) error {
	areas := map[string]bool{}
	for _, a := range cfg.Areas {
		areas[a] = true
	}
	inArea := func(q string) bool {
		first, _, _ := strings.Cut(q, "/")
		return areas[first] || first == "areas"
	}
	trackedUnder := func(dir string) bool {
		for _, f := range tracked {
			if under(f, dir) {
				return true
			}
		}
		return false
	}
	if exists(p.root, "areas") {
		return errors.New("areas/ がすでにあります。形式0の記録では、この名前は使えません")
	}
	targets := map[string]bool{}
	for i, m := range moves {
		switch {
		case !exists(p.root, m.From):
			return fmt.Errorf("移動元の %s が見つかりません", m.From)
		case areas[m.From] || m.From == ".git" || m.From == ".lacia" || strings.HasPrefix(m.From, ".git/"):
			return fmt.Errorf("%s は追加の移動元に指定できません", m.From)
		}
		for j, other := range moves {
			if i != j && under(m.From, other.From) {
				return fmt.Errorf("移動元の %s は、同じ一覧にある %s の中にあります。どちらか1つを指定してください", m.From, other.From)
			}
		}
		if m.To == "" {
			p.removes = append(p.removes, m.From)
			continue
		}
		switch {
		case exists(p.root, m.To) || targets[m.To]:
			return fmt.Errorf("移動先の %s はすでに存在するか、ほかの項目の移動先に指定されています", m.To)
		case inArea(m.To):
			return fmt.Errorf("移動先の %s は領域の中にあります。追加の移動先には領域の外を指定してください", m.To)
		case under(m.To, m.From):
			return fmt.Errorf("%s 自身やその中のパスは移動先に指定できません", m.From)
		}
		targets[m.To] = true
		p.steps = append(p.steps, step{From: m.From, To: m.To, Git: trackedUnder(m.From)})
	}
	for _, area := range cfg.Areas {
		if info, err := os.Stat(filepath.Join(p.root, area)); err == nil && info.IsDir() {
			p.steps = append(p.steps, step{From: area, To: "areas/" + area, Git: trackedUnder(area)})
		} else {
			p.Attention = append(p.Attention, fmt.Sprintf("領域 %s のディレクトリーがありません。必要なら、移したあとで areas/%s を作ってください。", area, area))
		}
	}
	// The files of the system move into .lacia/; the overrides only from
	// their default place, since a setting that names a place keeps it.
	for _, m := range []step{
		{From: task.AliasFile, To: task.AliasFile1},
		{From: "personal/knowledge/calendar-display-overrides.json", To: ".lacia/calendar-overrides.json"},
	} {
		if !exists(p.root, m.From) || p.removed(m.From) || !p.movedOnlyAsArea(m.From) {
			continue
		}
		if exists(p.root, m.To) {
			return fmt.Errorf("%s がすでにあります", m.To)
		}
		p.steps = append(p.steps, step{From: p.final(m.From), To: m.To, Git: true})
	}
	for _, f := range tracked {
		if p.removed(f) {
			p.Stats.Removed++
		} else if p.final(f) != f {
			p.Stats.MovedFiles++
		}
	}
	p.Stats.Moves = len(p.steps)
	return nil
}

// movedOnlyAsArea reports whether a path moves only because its area does.
func (p *Plan) movedOnlyAsArea(q string) bool {
	for _, s := range p.steps {
		if under(q, s.From) && !strings.HasPrefix(s.To, "areas/") {
			return false
		}
	}
	return true
}

// apply follows a path of format 0 through the moves, in order.
func apply(q string, steps []step) string {
	for _, s := range steps {
		if under(q, s.From) {
			q = s.To + q[len(s.From):]
		}
	}
	return q
}

// final is where a path of format 0 ends up.
func (p *Plan) final(q string) string { return apply(q, p.steps) }

// removed reports whether a path of format 0 is removed.
func (p *Plan) removed(q string) bool {
	for _, r := range p.removes {
		if under(q, r) {
			return true
		}
	}
	return false
}

// archived reports whether a path is, or ends up, under archive/.
func (p *Plan) archived(q string) bool {
	return under(q, "archive") || under(p.final(q), "archive")
}

// planContents is M5 to M9: the new text of every file that changes.
func (p *Plan) planContents(cfg task.Config, tracked []string, now time.Time) error {
	areas := map[string]bool{}
	for _, a := range cfg.Areas {
		areas[a] = true
	}
	// Paths written out in text are replaced when they name something that
	// moved (a file, a directory in it, or the root of a move other than an
	// area), or start with the four directories of an area.
	entries := map[string]string{}
	for _, s := range p.steps {
		if !areas[s.From] {
			entries[s.From] = p.final(s.From)
		}
	}
	gone := map[string]bool{}
	for _, r := range p.removes {
		gone[r] = true
	}
	for _, f := range tracked {
		if p.removed(f) {
			gone[f] = true
			continue
		}
		if p.final(f) == f {
			continue
		}
		for d := f; d != "."; d = path.Dir(d) {
			if areas[d] {
				break
			}
			if p.final(d) != d {
				entries[d] = p.final(d)
			}
		}
	}
	for _, a := range cfg.Areas {
		for _, sub := range []string{"tasks", "projects", "knowledge", "procedures"} {
			entries[a+"/"+sub] = "areas/" + a + "/" + sub
		}
	}

	// The histories to convert: those of the tasks and of the projects.
	logs := map[string]string{} // path of log.md → the day a dateless block falls back to
	legacyTasks, problems, err := task.ReadLegacyTasks(p.root, cfg.Areas)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("読めないタスクがあります。直してから移してください: %s: %s", problems[0].Path, problems[0].Err)
	}
	for _, t := range legacyTasks {
		logs[t.Dir+"/"+task.LogFile] = "task:" + t.Created
	}
	projects, _ := project.Load(p.root, 0, cfg.Areas)
	for _, pr := range projects {
		logs[pr.Dir+"/"+task.LogFile] = "project"
	}

	for _, f := range tracked {
		if p.removed(f) || p.archived(f) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.root, filepath.FromSlash(f)))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		text := string(data)
		base := path.Base(f)
		_, isLog := logs[f]
		frozen := base == task.LogFile || artifactOfTask(f, areas)
		text = p.rewrite(f, text, strings.HasSuffix(f, ".md"), !frozen, entries, gone, areas)
		if base == "task.md" || base == project.File {
			var n int
			text, n = renameHeadings(text, base)
			p.Stats.Headings += n
		}
		if isLog {
			kind := logs[f]
			fallback := strings.TrimPrefix(kind, "task:")
			if kind == "project" {
				if fallback = FirstDate(text); fallback == "" {
					fallback = now.In(task.JST).Format(time.DateOnly)
				}
			}
			out, events, blocks, err := Convert(text, kind != "project", fallback)
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			p.histories[p.final(f)] = out
			p.Stats.Histories++
			p.Stats.Events += events
			p.Stats.Blocks += blocks
			continue
		}
		if text != string(data) {
			p.writes[p.final(f)] = []byte(text)
		}
	}

	// M8 and M9.
	ignore, _ := os.ReadFile(filepath.Join(p.root, ".gitignore"))
	if !p.removed(".gitignore") {
		if text, ok := p.writes[".gitignore"]; ok {
			ignore = text
		}
		if next := gitignore(string(ignore)); next != string(ignore) {
			p.writes[".gitignore"] = []byte(next)
		}
	}
	config, err := os.ReadFile(filepath.Join(p.root, task.ConfigFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if text, ok := p.writes[task.ConfigFile]; ok {
		config = text
	}
	next, err := configText(string(config))
	if err != nil {
		return err
	}
	p.writes[task.ConfigFile] = []byte(next)
	p.todo = true
	sort.Strings(p.Attention)
	return nil
}

// artifactOfTask reports whether a path is in the artifacts/ of a task,
// which keep what they said when they were made.
func artifactOfTask(f string, areas map[string]bool) bool {
	parts := strings.Split(f, "/")
	return len(parts) > 5 && areas[parts[0]] && parts[1] == "tasks" && parts[4] == task.ArtifactsDir
}

// rewrite is M5 for one file: the links of a Markdown file, and, when
// paths is set, the paths written out in the text.
func (p *Plan) rewrite(file, text string, markdown, paths bool, entries map[string]string, gone map[string]bool, areas map[string]bool) string {
	lines := strings.Split(text, "\n")
	fenced := false
	for n, line := range lines {
		fence := markdown && mdlink.Fence(line)
		var links []mdlink.Link
		if markdown && !fenced && !fence {
			links = mdlink.Find(line)
		}
		var b strings.Builder
		at := 0
		for _, l := range links {
			b.WriteString(p.rewritePaths(file, n, line[at:l.Start], paths, entries, gone))
			b.WriteString(p.rewriteLink(file, n, l.Dest))
			at = l.End
		}
		b.WriteString(p.rewritePaths(file, n, line[at:], paths, entries, gone))
		lines[n] = b.String()
		if fence {
			fenced = !fenced
		}
		if paths {
			for _, a := range leftAreaPaths(lines[n], areas) {
				p.Attention = append(p.Attention, fmt.Sprintf("%s:%d areas/ の付いていない場所が残っています: %s", p.final(file), n+1, a))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// rewriteLink is M5.1 for one destination.
func (p *Plan) rewriteLink(file string, n int, dest string) string {
	target, fragment, ok := mdlink.Resolve(file, dest)
	if !ok {
		return dest
	}
	if p.removed(target) {
		p.Attention = append(p.Attention, fmt.Sprintf("%s:%d 消すファイルを指しています: %s", p.final(file), n+1, dest))
		return dest
	}
	if target == "." {
		return dest
	}
	if !exists(p.root, target) {
		if plain, err := url.PathUnescape(target); err != nil || !exists(p.root, plain) {
			p.Attention = append(p.Attention, fmt.Sprintf("%s:%d リンクの先が、移す前からありません: %s", p.final(file), n+1, dest))
		}
	}
	before := mdlink.Relative(file, target)
	after := mdlink.Relative(p.final(file), p.final(target))
	if before == after {
		return dest
	}
	if strings.HasSuffix(strings.SplitN(dest, "#", 2)[0], "/") {
		after += "/"
	}
	p.Stats.Links++
	return after + fragment
}

// pathChar is what a path written in text is made of.
func pathChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == '/'
}

// rewritePaths is M5.2 for a piece of a line.
func (p *Plan) rewritePaths(file string, n int, text string, enabled bool, entries map[string]string, gone map[string]bool) string {
	if !enabled {
		return text
	}
	var b strings.Builder
	i := 0
	for i < len(text) {
		c := text[i]
		if !pathChar(c) || c == '/' || c == '.' || i > 0 && (pathChar(text[i-1]) || text[i-1] == '~') {
			b.WriteByte(c)
			i++
			continue
		}
		j := i
		for j < len(text) && pathChar(text[j]) {
			j++
		}
		token := text[i:j]
		replaced := false
		for cut := len(token); cut > 0; cut = strings.LastIndex(token[:cut], "/") {
			candidate := token[:cut]
			if gone[candidate] {
				p.Attention = append(p.Attention, fmt.Sprintf("%s:%d 消すファイルを指しています: %s", p.final(file), n+1, candidate))
				break
			}
			if to, ok := entries[candidate]; ok {
				b.WriteString(to + token[cut:])
				p.Stats.Paths++
				replaced = true
				break
			}
		}
		if !replaced {
			b.WriteString(token)
		}
		i = j
	}
	return b.String()
}

// leftAreaPaths finds paths that still start with an area's name.
func leftAreaPaths(line string, areas map[string]bool) []string {
	var out []string
	for i := 0; i < len(line); i++ {
		if i > 0 && (pathChar(line[i-1]) || line[i-1] == '~') {
			continue
		}
		for a := range areas {
			if strings.HasPrefix(line[i:], a+"/") {
				j := i
				for j < len(line) && pathChar(line[j]) {
					j++
				}
				out = append(out, line[i:j])
			}
		}
	}
	sort.Strings(out)
	return out
}

// headings1 are the renamed sections of M7, by file.
var headings1 = map[string]map[string]string{
	"task.md":    {"## 見ていただくもの": "## いまの資料"},
	"project.md": {"## 目的": "## めざすこと", "## 方針": "## 進め方", "## 順序": "## 取りかかる順"},
}

// renameHeadings is M7: whole lines outside fenced code.
func renameHeadings(text, base string) (string, int) {
	names := headings1[base]
	lines := strings.Split(text, "\n")
	fenced, n := false, 0
	for i, line := range lines {
		if mdlink.Fence(line) {
			fenced = !fenced
			continue
		}
		if to, ok := names[line]; ok && !fenced {
			lines[i] = to
			n++
		}
	}
	return strings.Join(lines, "\n"), n
}

// gitignore is M8.
func gitignore(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	have := map[string]bool{}
	for i, line := range lines {
		if strings.TrimSpace(line) == "/*/tasks/*/*/files/" {
			lines[i] = "/areas/*/tasks/*/*/files/"
		}
		have[strings.TrimSpace(lines[i])] = true
	}
	for _, want := range []string{"/local/", "/.lacia/source/", "/areas/*/tasks/*/*/files/"} {
		if !have[want] {
			lines = append(lines, want)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

var formatValue = regexp.MustCompile(`"format"\s*:\s*[0-9]+`)

// configText is M9: format 1, and the time zone when there is none. The
// other items and their order stay as they are.
func configText(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "{\n  \"format\": 1,\n  \"timezone\": \"Asia/Tokyo\"\n}\n", nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &cfg); err != nil {
		return "", fmt.Errorf("%s を読めません: %w", task.ConfigFile, err)
	}
	if _, ok := cfg["format"]; ok {
		text = formatValue.ReplaceAllString(text, `"format": 1`)
	} else {
		open := strings.Index(text, "{")
		sep := ","
		if len(cfg) == 0 {
			sep = ""
		}
		text = text[:open+1] + "\n  \"format\": 1" + sep + text[open+1:]
	}
	if _, ok := cfg["timezone"]; !ok {
		end := strings.LastIndex(text, "}")
		body := strings.TrimRight(text[:end], " \t\n")
		text = body + ",\n  \"timezone\": \"Asia/Tokyo\"\n" + text[end:]
	}
	var check task.Config
	if err := json.Unmarshal([]byte(text), &check); err != nil || check.Format != 1 {
		return "", fmt.Errorf("%s に format を書けませんでした", task.ConfigFile)
	}
	return text, nil
}

// record is one line of the journal.
type record struct {
	Op   string      `json:"op"` // start, moved, removed, created, rewrote
	From string      `json:"from,omitempty"`
	To   string      `json:"to,omitempty"`
	Path string      `json:"path,omitempty"`
	Data []byte      `json:"data,omitempty"`
	Mode os.FileMode `json:"mode,omitempty"`
	Head string      `json:"head,omitempty"`
	At   string      `json:"at,omitempty"`
}

type journal struct {
	f    *os.File
	root string
}

func (j *journal) add(r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(data, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// keep records the file at rel before it is changed or removed.
func (j *journal) keep(op, rel string) error {
	full := filepath.Join(j.root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if os.IsNotExist(err) {
		if op == "rewrote" {
			return j.add(record{Op: "created", Path: rel})
		}
		return nil
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	return j.add(record{Op: op, Path: rel, Data: data, Mode: info.Mode().Perm()})
}

// Run carries out the plan, writing each step to the journal first.
func (p *Plan) Run(now time.Time) error {
	if err := os.MkdirAll(filepath.Join(p.root, "local"), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.root, filepath.FromSlash(JournalFile)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	j := &journal{f: f, root: p.root}
	head, _ := git(p.root, "rev-parse", "HEAD")
	if err := j.add(record{Op: "start", Head: strings.TrimSpace(head), At: now.Format(time.RFC3339)}); err != nil {
		return err
	}
	abs := func(rel string) string { return filepath.Join(p.root, filepath.FromSlash(rel)) }
	var emptied []string
	// M2: removals first, so that nothing moves into a place being emptied.
	for _, r := range p.removes {
		listed, err := git(p.root, "ls-files", "-z", "--", r)
		if err != nil {
			return err
		}
		for _, f := range strings.Split(listed, "\x00") {
			if f == "" {
				continue
			}
			if err := j.keep("removed", f); err != nil {
				return err
			}
		}
		if _, err := git(p.root, "rm", "-r", "-q", "--", r); err != nil {
			return err
		}
		emptied = append(emptied, r)
	}
	// M2 to M4: the moves.
	for _, s := range p.steps {
		if err := os.MkdirAll(filepath.Dir(abs(s.To)), 0o755); err != nil {
			return err
		}
		if err := j.add(record{Op: "moved", From: s.From, To: s.To}); err != nil {
			return err
		}
		if s.Git {
			_, err = git(p.root, "mv", "--", s.From, s.To)
		} else {
			err = os.Rename(abs(s.From), abs(s.To))
		}
		if err != nil {
			return err
		}
		emptied = append(emptied, s.From)
	}
	// M5 to M9: the new contents.
	var names []string
	for name := range p.writes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := j.keep("rewrote", name); err != nil {
			return err
		}
		if err := writeFile(abs(name), p.writes[name]); err != nil {
			return err
		}
	}
	var logs []string
	for name := range p.histories {
		logs = append(logs, name)
	}
	sort.Strings(logs)
	for _, name := range logs {
		jsonl := path.Join(path.Dir(name), task.HistoryFile)
		if err := j.keep("rewrote", jsonl); err != nil {
			return err
		}
		if err := writeFile(abs(jsonl), p.histories[name]); err != nil {
			return err
		}
		if err := j.keep("removed", name); err != nil {
			return err
		}
		if err := os.Remove(abs(name)); err != nil {
			return err
		}
	}
	// M10. The directories of the areas stay, even when empty.
	for _, from := range emptied {
		for dir := from; dir != "." && dir != "areas" && path.Dir(dir) != "areas"; dir = path.Dir(dir) {
			if os.Remove(abs(dir)) != nil && exists(p.root, dir) {
				break
			}
		}
	}
	if p.todo {
		if err := j.keep("rewrote", "TODO.md"); err != nil {
			return err
		}
		store, err := task.Open(p.root)
		if err != nil {
			return err
		}
		if err := todo.Write(store, now); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(full string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(full); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, mode)
}

// removeEmpty removes rel and its parents while they are empty.
func removeEmpty(root, rel string) {
	for rel != "." && rel != "" && rel != "/" {
		if os.Remove(filepath.Join(root, filepath.FromSlash(rel))) != nil {
			return
		}
		rel = path.Dir(rel)
	}
}

// Undo reads the journal backwards and puts every step back, the files
// outside Git included. It works after a failure and after a commit. The
// index is reset to HEAD at the end, so that git status shows the work
// tree as it is.
func Undo(root string) (int, error) {
	if err := checkNotServed(root); err != nil {
		return 0, err
	}
	file := filepath.Join(root, filepath.FromSlash(JournalFile))
	f, err := os.Open(file)
	if err != nil {
		return 0, fmt.Errorf("%s がないので、戻すものがありません", JournalFile)
	}
	var records []record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<30)
	for scanner.Scan() {
		var r record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			f.Close()
			return 0, fmt.Errorf("%s を読めません: %w", JournalFile, err)
		}
		records = append(records, r)
	}
	f.Close()
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	abs := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	done := 0
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		switch r.Op {
		case "created":
			if err := os.Remove(abs(r.Path)); err != nil && !os.IsNotExist(err) {
				return done, err
			}
			removeEmpty(root, path.Dir(r.Path))
		case "rewrote", "removed":
			mode := r.Mode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.MkdirAll(filepath.Dir(abs(r.Path)), 0o755); err != nil {
				return done, err
			}
			if err := os.WriteFile(abs(r.Path), r.Data, mode); err != nil {
				return done, err
			}
			if err := os.Chmod(abs(r.Path), mode); err != nil {
				return done, err
			}
		case "moved":
			if _, err := os.Lstat(abs(r.To)); err != nil {
				continue // the move never happened
			}
			if err := os.MkdirAll(filepath.Dir(abs(r.From)), 0o755); err != nil {
				return done, err
			}
			if err := os.Rename(abs(r.To), abs(r.From)); err != nil {
				return done, err
			}
			removeEmpty(root, path.Dir(r.To))
		}
		done++
	}
	if _, err := git(root, "reset", "-q"); err != nil {
		return done, err
	}
	if err := os.Remove(file); err != nil {
		return done, err
	}
	removeEmpty(root, "local")
	return done, nil
}

// Report writes the result of M11.
func (p *Plan) Report(w io.Writer, dryRun bool) {
	s := p.Stats
	if dryRun {
		fmt.Fprintf(w, "--dry-run のため、形式%dへの移行でどこが変わるかを表示します。ファイルは書き換えていません。\n", p.format)
	} else {
		fmt.Fprintf(w, "形式%dへの移行結果です。\n", p.format)
	}
	fmt.Fprintf(w, "移動: %dか所（Git で管理するファイルは%d個）\n", s.Moves, s.MovedFiles)
	for _, st := range p.steps {
		fmt.Fprintf(w, "  %s → %s\n", st.From, st.To)
	}
	fmt.Fprintf(w, "削除: Git で管理するファイル%d個\n", s.Removed)
	for _, r := range p.removes {
		fmt.Fprintf(w, "  %s\n", r)
	}
	fmt.Fprintf(w, "リンク先の修正: %d件\n", s.Links)
	fmt.Fprintf(w, "本文に書かれたパスの修正: %d件\n", s.Paths)
	fmt.Fprintf(w, "履歴の変換: %dファイル。決まった書式の記録が%d件、それ以外の文章をまとめた記録が%d件で、合わせて%d件です。\n", s.Histories, s.Events, s.Blocks, s.Events+s.Blocks)
	fmt.Fprintf(w, "見出し名の変更: %d件\n", s.Headings)
	if p.format == 1 {
		fmt.Fprintln(w, "変換後の履歴をすべて照合し、文章が元の log.md と一致することを確かめました。")
	}
	if len(p.Attention) == 0 {
		fmt.Fprintln(w, "個別に確かめていただく箇所はありません。")
		return
	}
	fmt.Fprintf(w, "次の%d件を確かめてください。\n", len(p.Attention))
	for _, a := range p.Attention {
		fmt.Fprintln(w, "  "+a)
	}
}

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type mirrorFixture struct {
	t           *testing.T
	server, mac string
	base        string
}

func newMirrorFixture(t *testing.T) mirrorFixture {
	t.Helper()
	dir := t.TempDir()
	f := mirrorFixture{t: t, server: filepath.Join(dir, "server"), mac: filepath.Join(dir, "mac")}
	f.git(dir, "init", "--quiet", "--initial-branch=main", f.server)
	f.put(f.server, ".gitignore", "/local/\n")
	f.put(f.server, "work/T1045/log.md", "base\n")
	f.put(f.server, "work/T1048/log.md", "base\n")
	f.put(f.server, "unrelated.txt", "base\n")
	f.base = f.commit(f.server)
	f.git(dir, "clone", "--quiet", f.server, f.mac)
	f.git(f.mac, "config", "remote.origin.pushurl", "disabled://lacia-local-readonly")
	return f
}

func (f mirrorFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Lacia tests",
		"-c", "user.email=tests@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f mirrorFixture) put(dir, name, text string) {
	f.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f mirrorFixture) read(dir, name string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

func (f mirrorFixture) commit(dir string) string {
	f.t.Helper()
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "--quiet", "-m", "test update")
	return f.git(dir, "rev-parse", "HEAD")
}

func (f mirrorFixture) changes() string {
	localLog, sameLog := "work/T1045/log.md", "work/T1048/log.md"
	artifact := "work/T1048/artifacts/detail.md"
	f.put(f.mac, localLog, "local-only log\n")
	f.put(f.mac, sameLog, "same log\n")
	f.put(f.mac, artifact, "same report\n")
	f.put(f.mac, "unrelated.txt", "keep this local edit\n")
	f.put(f.mac, "unrelated-new.txt", "keep this local file\n")
	f.put(f.server, localLog, "server log\n")
	f.put(f.server, sameLog, "same log\n")
	f.put(f.server, artifact, "same report\n")
	return f.commit(f.server)
}

func TestSyncMirrorCleanFastForward(t *testing.T) {
	f := newMirrorFixture(t)
	f.put(f.server, "new.txt", "server\n")
	target := f.commit(f.server)
	result, err := SyncMirror(context.Background(), f.mac, target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != target || result.Backup != "" || f.read(f.mac, "new.txt") != "server\n" {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestSyncMirrorProtectsConflictAndUnrelatedWork(t *testing.T) {
	f := newMirrorFixture(t)
	target := f.changes()
	staged := "work/T1045/log.md"
	f.git(f.mac, "add", "--", staged)
	f.put(f.mac, staged, "worktree version of local log\n")
	result, err := SyncMirror(context.Background(), f.mac, target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != target || result.Backup == "" ||
		!reflect.DeepEqual(result.LocalOnly, []string{staged}) {
		t.Fatalf("unexpected result %#v", result)
	}
	if f.read(f.mac, staged) != "server log\n" ||
		f.read(f.mac, "work/T1048/artifacts/detail.md") != "same report\n" ||
		f.read(f.mac, "unrelated.txt") != "keep this local edit\n" ||
		f.read(f.mac, "unrelated-new.txt") != "keep this local file\n" {
		t.Fatal("fast-forward did not preserve unrelated work")
	}
	if work := f.read(result.Backup, "working-tree.patch"); !strings.Contains(work, "worktree version of local log") {
		t.Fatal("working-tree patch was not saved")
	}
	if index := f.read(result.Backup, "index.patch"); !strings.Contains(index, "local-only log") {
		t.Fatal("index patch was not saved")
	}
	if raw := f.read(result.Backup, "manifest.json"); !strings.Contains(raw, staged) {
		t.Fatal("backup manifest has no original file path")
	}
}

func TestSyncMirrorResumesInterruptedRecovery(t *testing.T) {
	f := newMirrorFixture(t)
	target := f.changes()
	f.git(f.mac, "fetch", "--quiet", "origin", "main")
	g, release, err := openMirror(context.Background(), f.mac)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := g.plan(f.base, target)
	if err != nil {
		release()
		t.Fatal(err)
	}
	if err := g.saveBackup(journal); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	// Simulate interruption after part of the working-tree changes were removed.
	f.git(f.mac, "restore", "--source", f.base, "--worktree", "--", "work/T1045/log.md")
	if err := os.Remove(filepath.Join(f.mac, "work/T1048/artifacts/detail.md")); err != nil {
		t.Fatal(err)
	}

	first, err := SyncMirror(context.Background(), f.mac, target)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != target || first.Backup != filepath.Join(f.mac, journal.Backup) ||
		!reflect.DeepEqual(first.LocalOnly, []string{"work/T1045/log.md"}) {
		t.Fatalf("unexpected resumed result %#v", first)
	}
	again, err := SyncMirror(context.Background(), f.mac, target)
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatalf("report was not replayed: %#v, %v", again, err)
	}
	if err := AcknowledgeMirrorRecovery(f.mac, first.Backup); err != nil {
		t.Fatal(err)
	}
	final, err := SyncMirror(context.Background(), f.mac, target)
	if err != nil || final.Revision != target || final.Backup != "" {
		t.Fatalf("report was not cleared: %#v, %v", final, err)
	}
	if _, err := os.Stat(first.Backup); err != nil {
		t.Fatal("acknowledgement removed the backup", err)
	}
}

func TestSyncMirrorDoesNotEditWhenUnsafe(t *testing.T) {
	for _, check := range []struct {
		name    string
		prepare func(mirrorFixture)
	}{
		{"backup blocked", func(f mirrorFixture) { f.put(f.mac, "local/mirror-recovery", "blocked\n") }},
		{"merge in progress", func(f mirrorFixture) { f.put(f.mac, ".git/MERGE_HEAD", f.base+"\n") }},
		{"fetch failed", func(f mirrorFixture) {
			f.git(f.mac, "remote", "set-url", "origin", filepath.Join(f.mac, "missing"))
		}},
		{"diverged", func(f mirrorFixture) { f.commit(f.mac) }},
	} {
		t.Run(check.name, func(t *testing.T) {
			f := newMirrorFixture(t)
			target := f.changes()
			check.prepare(f)
			head := f.git(f.mac, "rev-parse", "HEAD")
			if _, err := SyncMirror(context.Background(), f.mac, target); err == nil {
				t.Fatal("unsafe mirror update succeeded")
			}
			if f.git(f.mac, "rev-parse", "HEAD") != head ||
				f.read(f.mac, "work/T1045/log.md") != "local-only log\n" ||
				f.read(f.mac, "work/T1048/artifacts/detail.md") != "same report\n" {
				t.Fatal("unsafe mirror update changed the copy")
			}
		})
	}
}

func TestSyncMirrorRejectsTargetTrackingLocal(t *testing.T) {
	f := newMirrorFixture(t)
	f.put(f.server, "Local/risk.txt", "must not be fetched into working tree\n")
	f.git(f.server, "add", "--force", "--", "Local/risk.txt")
	target := f.commit(f.server)
	if _, err := SyncMirror(context.Background(), f.mac, target); err == nil {
		t.Fatal("target tracking local was accepted")
	}
	if f.git(f.mac, "rev-parse", "HEAD") != f.base {
		t.Fatal("target tracking local changed the copied branch")
	}
	if _, err := os.Lstat(filepath.Join(f.mac, "Local")); !os.IsNotExist(err) {
		t.Fatal("target tracking local changed the working tree")
	}
}

func TestSyncMirrorRejectsDamagedBackupBeforeTouchingCopy(t *testing.T) {
	f := newMirrorFixture(t)
	target := f.changes()
	f.git(f.mac, "fetch", "--quiet", "origin", "main")
	g, release, err := openMirror(context.Background(), f.mac)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := g.plan(f.base, target)
	if err == nil {
		err = g.saveBackup(journal)
	}
	release()
	if err != nil {
		t.Fatal(err)
	}
	f.put(f.mac, filepath.Join(journal.Backup, journal.Entries[0].Saved), "damaged\n")
	if _, err := SyncMirror(context.Background(), f.mac, target); err == nil {
		t.Fatal("damaged backup was accepted")
	}
	if f.git(f.mac, "rev-parse", "HEAD") != f.base ||
		f.read(f.mac, "work/T1045/log.md") != "local-only log\n" ||
		f.read(f.mac, "work/T1048/artifacts/detail.md") != "same report\n" {
		t.Fatal("damaged backup changed the local copy")
	}
}

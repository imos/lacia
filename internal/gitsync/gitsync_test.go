package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestSyncCommitsAndPushes(t *testing.T) {
	base := t.TempDir()
	remote, work := filepath.Join(base, "remote.git"), filepath.Join(base, "work")
	os.MkdirAll(remote, 0o755)
	run(t, remote, "init", "--bare", "--quiet")
	run(t, base, "clone", "--quiet", remote, work)
	run(t, work, "config", "user.name", "test")
	run(t, work, "config", "user.email", "test@example.com")
	os.WriteFile(filepath.Join(work, "README.md"), []byte("x\n"), 0o644)
	run(t, work, "add", ".")
	run(t, work, "commit", "--quiet", "-m", "first")
	run(t, work, "push", "--quiet", "-u", "origin", "HEAD")

	published := strings.TrimSpace(run(t, work, "rev-parse", "HEAD"))
	if got := (&Syncer{Dir: work}).PublishedRevision(context.Background()); got != published {
		t.Fatalf("published revision = %q, want %q", got, published)
	}
	prepared := 0
	s := &Syncer{Dir: work, Prepare: func() error { prepared++; return nil }}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := run(t, work, "rev-list", "--count", "HEAD"); strings.TrimSpace(got) != "1" {
		t.Errorf("a clean repository got a commit: %s", got)
	}

	dir := filepath.Join(work, "work/tasks/2026-11/T1001-x")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "log.md"), []byte("b\n"), 0o644)
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := run(t, work, "log", "-1", "--format=%s"); strings.TrimSpace(got) != "chore(sync): T1001 の変更を保存" {
		t.Errorf("commit message = %q", got)
	}
	if got := run(t, remote, "rev-list", "--count", "HEAD"); strings.TrimSpace(got) != "2" {
		t.Errorf("remote has %s commits", strings.TrimSpace(got))
	}
	afterPush := strings.TrimSpace(run(t, work, "rev-parse", "HEAD"))
	run(t, work, "commit", "--allow-empty", "--quiet", "-m", "not yet pushed")
	if got := s.PublishedRevision(context.Background()); got != afterPush {
		t.Fatalf("published revision includes an unpushed commit: %q, want %q", got, afterPush)
	}
	if prepared != 2 {
		t.Errorf("Prepare ran %d times", prepared)
	}
}

func TestRepeatedWritesToOneTaskAreGathered(t *testing.T) {
	base := t.TempDir()
	remote, work := filepath.Join(base, "remote.git"), filepath.Join(base, "work")
	os.MkdirAll(remote, 0o755)
	run(t, remote, "init", "--bare", "--quiet")
	run(t, base, "clone", "--quiet", remote, work)
	run(t, work, "config", "user.name", "test")
	run(t, work, "config", "user.email", "test@example.com")
	os.WriteFile(filepath.Join(work, "README.md"), []byte("x\n"), 0o644)
	run(t, work, "add", ".")
	run(t, work, "commit", "--quiet", "-m", "first")
	run(t, work, "push", "--quiet", "-u", "origin", "HEAD")

	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	s := &Syncer{Dir: work, Burst: 3, Refill: 5 * time.Minute, Now: func() time.Time { return now }}
	write := func(id, text string) {
		t.Helper()
		dir := filepath.Join(work, "work/tasks/2026-11", id)
		os.MkdirAll(dir, 0o755)
		f, err := os.OpenFile(filepath.Join(dir, "log.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text + "\n")
		f.Close()
	}
	commits := func() int {
		t.Helper()
		n, _ := strconv.Atoi(strings.TrimSpace(run(t, work, "rev-list", "--count", "HEAD")))
		return n - 1
	}
	look := func() {
		t.Helper()
		if err := s.sync(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}

	// A loop writes to one task every ten seconds for ten minutes.
	for i := 0; i < 60; i++ {
		write("T1001", "again")
		look()
		now = now.Add(10 * time.Second)
	}
	// Three at once, then one when five minutes have passed.
	if got := commits(); got != 4 {
		t.Fatalf("60 writes to one task made %d commits, want 4", got)
	}
	if got := strings.TrimSpace(run(t, remote, "rev-list", "--count", "HEAD")); got != "5" {
		t.Errorf("remote has %s commits, want 5", got)
	}

	// Work on another task is saved at once, and carries the held changes.
	write("T1001", "held")
	look()
	if got := commits(); got != 4 {
		t.Fatalf("a held change was committed: %d", got)
	}
	write("T1002", "other")
	look()
	if got := commits(); got != 5 {
		t.Fatalf("another task's change was held: %d", got)
	}
	if got := strings.TrimSpace(run(t, work, "status", "--porcelain")); got != "" {
		t.Errorf("changes were left behind: %s", got)
	}
	if got := strings.TrimSpace(run(t, work, "log", "-1", "--format=%s")); got != "chore(sync): T1001, T1002 の変更を保存" {
		t.Errorf("commit message = %q", got)
	}

	// The last save before the server stops ignores the budget.
	write("T1001", "last")
	look()
	if got := commits(); got != 5 {
		t.Fatalf("a held change was committed: %d", got)
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := commits(); got != 6 {
		t.Fatalf("the forced save made %d commits, want 6", got)
	}

	// After a quiet spell the task has its burst back.
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		write("T1001", "later")
		look()
	}
	if got := commits(); got != 9 {
		t.Fatalf("after a quiet hour: %d commits, want 9", got)
	}
}

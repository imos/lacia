package wait

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, jst)
	s.Now = func() time.Time { return now }
	return s, &now
}

func job(next string) Wait {
	return Wait{TaskID: "T1", Kind: Job, Context: "ctx", Namespace: "ns", Name: "train-1", By: time.Date(2026, 11, 19, 6, 0, 0, 0, jst), Next: next, Then: "結果を回収します。", From: "local-claude"}
}

func TestAddRefusesWhatKubectlShouldNotSee(t *testing.T) {
	s, _ := open(t)
	bad := []func(*Wait){
		func(w *Wait) { w.Name = "x; rm -rf /" },
		func(w *Wait) { w.Context = "--kubeconfig=/tmp/x" },
		func(w *Wait) { w.By = time.Time{} },
		func(w *Wait) { w.By = time.Date(2026, 11, 18, 0, 0, 0, 0, jst) },
		func(w *Wait) { w.Then = " " },
		func(w *Wait) { w.Next = "someone" },
		func(w *Wait) { w.Kind, w.Path = File, "relative/DONE" },
		func(w *Wait) { w.Kind, w.Path, w.Name = File, "/root/DONE", "deploy/work space" },
	}
	for i, change := range bad {
		w := job(NextCodex)
		change(&w)
		if _, _, err := s.Add(w); err == nil {
			t.Errorf("case %d was accepted: %+v", i, w)
		}
	}
	file := job(NextNone)
	file.Kind, file.Name, file.Path, file.Then = File, "deploy/workspace", "/root/run/DONE", ""
	if _, added, err := s.Add(file); err != nil || !added {
		t.Fatalf("file wait: %v", err)
	}
}

func TestAddingTheSameTargetTwiceKeepsOneWait(t *testing.T) {
	s, _ := open(t)
	first, added, err := s.Add(job(NextCodex))
	if err != nil || !added {
		t.Fatal(err)
	}
	again, added, _ := s.Add(job(NextCodex))
	if added || again.ID != first.ID {
		t.Fatalf("second add = %+v, added %v", again, added)
	}
	// Once the first is over, the same job name may be waited for again.
	s.Observe(first.ID, Succeeded, "")
	if _, added, _ = s.Add(job(NextCodex)); !added {
		t.Fatal("a new wait after the end was refused")
	}
}

func TestAnObservedEndClosesTheWaitOnce(t *testing.T) {
	s, _ := open(t)
	w, _, _ := s.Add(job(NextCodex))
	if got, resolved, _ := s.Observe(w.ID, Running, ""); resolved || got.State != Waiting || got.Seen != Running {
		t.Fatalf("running: %+v", got)
	}
	// The watcher not being able to look is not an end.
	if got, resolved, _ := s.Observe(w.ID, Unseen, "Unauthorized"); resolved || got.State != Waiting {
		t.Fatalf("unseen: %+v", got)
	}
	got, resolved, _ := s.Observe(w.ID, Failed, "BackoffLimitExceeded")
	if !resolved || got.State != Broken || !got.Closed() {
		t.Fatalf("failed: %+v", got)
	}
	if got, resolved, _ = s.Observe(w.ID, Succeeded, ""); resolved || got.State != Broken {
		t.Fatalf("a later report changed the end: %+v", got)
	}
	// The waits survive a restart.
	reopened, err := Open(s.dir.Path())
	if err != nil {
		t.Fatal(err)
	}
	if again, err := reopened.Get(w.ID); err != nil || again.State != Broken || again.Detail != "BackoffLimitExceeded" {
		t.Fatalf("after reopen: %+v, %v", again, err)
	}
}

func TestTheDeadlineClosesAnOpenWait(t *testing.T) {
	s, now := open(t)
	w, _, _ := s.Add(job(NextCodex))
	if len(s.Expire()) != 0 {
		t.Fatal("expired early")
	}
	*now = w.By.Add(time.Second)
	expired := s.Expire()
	if len(expired) != 1 || expired[0].State != Overdue || !expired[0].Closed() {
		t.Fatalf("expired = %+v", expired)
	}
	if len(s.Expire()) != 0 {
		t.Fatal("expired twice")
	}
}

func TestUpgradeIgnoresClosedLegacyAndRefusesActiveLegacy(t *testing.T) {
	cases := []struct {
		body   string
		active bool
	}{
		{`{"id":"Wold","state":"open"}`, true},
		{`{"id":"Wold","state":"finished","kind":"tell","next":"codex"}`, true},
		{`{"id":"Wold","state":"finished","kind":"tell","next":"codex","acked":"2026-11-19T01:00:00Z"}`, false},
		{`{"id":"Wold","state":"cancelled"}`, false},
		{`{"id":"Wold","state":"overdue","told":"2026-11-19T01:00:00Z"}`, false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "Wold.json"), []byte(c.body), 0600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(dir)
		if (err != nil) != c.active {
			t.Fatalf("%s: %v", c.body, err)
		}
		if err == nil && len(s.List(nil)) != 0 {
			t.Fatal("legacy wait was reopened")
		}
	}
}

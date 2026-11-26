package schedule

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMigratedRoutineUsesLegacyBoundaryAndFutureSlots(t *testing.T) {
	root := t.TempDir()
	putDefinition(t, root, "schedules/github.md", "---\ndaily: 09:00\n---\n確認します。\n")
	state := filepath.Join(t.TempDir(), "schedules.json")
	var ids []string
	r := &Runner{Root: root, State: state, Submit: func(id string, j Job) error { ids = append(ids, id); return nil }}
	check := func(when string, n int) {
		t.Helper()
		if err := r.Tick(at(when)); err != nil || len(ids) != n {
			t.Fatalf("tick %s: %v %v", when, ids, err)
		}
	}
	check("2026-11-24 09:00", 1)
	active := "---\nid: S1001\nlegacy_name: github\ndaily: 09:00\n---\n確認します。\n"
	routine := "routines/S1001/routine.md"
	putDefinition(t, root, routine, active)
	r = &Runner{Root: root, State: state, Submit: r.Submit}
	check("2026-11-24 09:01", 1)
	check("2026-11-25 09:00", 2)
	if ids[1] != "sched-github-2026-11-25-0900" {
		t.Fatal(ids)
	}
	putDefinition(t, root, routine, "---\nid: S1001\nlegacy_name: github\ndaily: 09:00\nenabled: false\n---\n停止します。\n")
	check("2026-11-26 09:00", 2)
	putDefinition(t, root, routine, active)
	check("2026-11-26 10:00", 2)
	check("2026-11-27 09:00", 3)
}

func TestRoutineWeekdaysAndMissedRunSurviveRetry(t *testing.T) {
	root := t.TempDir()
	job := Job{ID: "S1001", Name: "S1001", LegacyName: "github", Enabled: true, Daily: "09:00", WeekdayMask: 1 << time.Monday}
	if got := job.Next(at("2026-11-24 10:00")); !got.Equal(at("2026-11-30 09:00")) {
		t.Fatalf("next=%v", got)
	}
	job.Slot, job.Skipped = at("2026-11-30 09:00"), 1
	log := &RunLog{Root: root, State: filepath.Join(root, "pending.json")}
	for range 2 {
		if err := log.Begin(job, "sched-github-2026-11-30-0900", job.Slot); err != nil {
			t.Fatal(err)
		}
	}
	runs := ReadRuns(root, job.ID, "2026-11")
	if len(runs) != 1 || !runs[0].At.Equal(at("2026-11-23 09:00")) {
		t.Fatalf("missed=%+v", runs)
	}
}

func TestStoppingRoutineDoesNotDropStartedResult(t *testing.T) {
	dir := t.TempDir()
	routing, err := LoadRouting("", dir, "work", "codex", "codex")
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDispatch(filepath.Join(dir, "dispatch.json"), routing)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	enabled := true
	d.Active = func(Job) bool { return enabled }
	job := Job{ID: "S1001", Name: "S1001"}
	if err := d.Enqueue("waiting", job, "", now); err != nil {
		t.Fatal(err)
	}
	enabled = false
	if skipped, err := d.SkipLocalIfStopped("waiting", now); err != nil || !skipped {
		t.Fatalf("skip=%t %v", skipped, err)
	}
	if err := d.SyncLocal("waiting", "done", Result{Settled: true, Finished: now}); err != nil {
		t.Fatal(err)
	}
	e, _ := d.Get("waiting")
	if e.Status != Skipped {
		t.Fatal(e.Status)
	}
	if err := d.Enqueue("started", job, "", now); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncLocal("started", "running", Result{Started: now}); err != nil {
		t.Fatal(err)
	}
	if skipped, err := d.SkipLocalIfStopped("started", now); err != nil || skipped {
		t.Fatalf("started skipped=%t %v", skipped, err)
	}
}

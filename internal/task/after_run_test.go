package task

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWakeAfterRoutineTogetherWithOtherConditions(t *testing.T) {
	s := newStore(t)
	registered := s.now().Add(123 * time.Nanosecond)
	s.now = func() time.Time { return registered }
	blocker, err := s.Create(NewTask{Area: "work", Title: "先に終えるタスク"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := oldWaiting(s, "定期タスクを待つ")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetField(item.ID, "after_run", "../invalid", ""); err == nil {
		t.Fatal("accepted an invalid routine name")
	}
	for _, field := range []Field{{"after_run", "S1001"}, {"after", blocker.ID}, {"resume_on", "2026-11-12 12:00"}, {"on_ready", "start"}} {
		if _, _, err := s.SetField(item.ID, field.Key, field.Value, ""); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := s.Get(item.ID)
	if err != nil || pending.AfterRunSince != registered.UTC().Format(time.RFC3339Nano) || !pending.Snoozed(registered) || !pending.HasReadyConditions() {
		t.Fatalf("registered wait: %+v, %v", pending, err)
	}
	label := pending.ReadyLabel(false)
	if got := label.String(); got != "2026-11-12 12:00以降かつ"+blocker.ID+"終了後かつ定期タスク S1001次回終了後再開" || label.Routine != "S1001" {
		t.Fatalf("ready label: %+v, %q", label, got)
	}

	readyAt := time.Date(2026, 11, 12, 12, 0, 0, 0, JST)
	ref := "/routines/S1001?month=2026-11#run-example"
	find := func(name string, since time.Time) (RoutineCompletion, bool) {
		if name != "S1001" || !since.Equal(registered) {
			t.Fatalf("lookup got %s after %s", name, since)
		}
		return RoutineCompletion{ID: "routine-run-id", Reference: ref, Outcome: "新着なし"}, true
	}
	if len(s.WakeWithRuns(readyAt, find)) != 0 {
		t.Fatal("woke before the ordinary dependency ended")
	}
	if _, _, err := s.SetStatus(blocker.ID, Done, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.WakeWithRuns(readyAt.Add(-time.Nanosecond), find)) != 0 || len(s.Wake(readyAt)) != 0 {
		t.Fatal("woke before the date or without a routine lookup")
	}
	woken := s.WakeWithRuns(readyAt, find)
	if len(woken) != 1 || woken[0].Status != Waiting || !woken[0].StartsWhenReady() || woken[0].ReadyRunRef != ref || woken[0].HasReadyConditions() {
		t.Fatalf("wake with automatic start: %+v", woken)
	}
	stored, err := s.Get(item.ID)
	if err != nil || stored.AfterRunSince != "" || stored.OnReady != "" || stored.ReadyRunRef != ref || len(s.WakeWithRuns(readyAt, find)) != 0 {
		t.Fatalf("consumed wait: %+v, %v", stored, err)
	}
	working, _, err := s.Claim(item.ID, Owner{Label: "local-codex", Session: "test"}, "", "")
	if err != nil || working.ReadyRunRef != ref {
		t.Fatalf("claim lost the result link: %+v, %v", working, err)
	}
	finished, _, err := s.SetStatus(item.ID, Review, "", "")
	if err != nil || finished.ReadyRunRef != "" {
		t.Fatalf("later review kept a stale result link: %+v, %v", finished, err)
	}
	history, err := ReadEvents(filepath.Join(s.Root(), item.Dir, HistoryFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range history {
		if line.Event.Type == EventWoke && line.Event.Request == "routine-run-id" && line.Event.Path == ref {
			return
		}
	}
	t.Fatalf("run reference missing from history: %+v", history)
}

func TestWakeAfterFailedRoutineAsksForReview(t *testing.T) {
	s := newStore(t)
	for _, outcome := range []string{"失敗", "一部未確認"} {
		t.Run(outcome, func(t *testing.T) {
			item, err := oldWaiting(s, "定期タスクを待つ")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.SetField(item.ID, "after_run", "legacy-name", ""); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.SetField(item.ID, "on_ready", "start", ""); err != nil {
				t.Fatal(err)
			}
			woken := s.WakeWithRuns(s.now().Add(time.Hour), func(string, time.Time) (RoutineCompletion, bool) {
				return RoutineCompletion{ID: "run-id", Reference: "/routines/legacy-name?month=2026-11#run-id", Outcome: outcome, Failed: true}, true
			})
			if len(woken) != 1 || woken[0].Status != Review || woken[0].StartsWhenReady() || woken[0].ReadySince != "" || woken[0].ReadyRunRef == "" {
				t.Fatalf("failure wake: %+v", woken)
			}
			history, err := readTestHistory(t, filepath.Join(s.Root(), item.Dir))
			if err != nil || !strings.Contains(string(history), "実行の結果は「"+outcome+"」です") {
				t.Fatalf("failed history: %s, %v", history, err)
			}
		})
	}
}

func TestRoutineWaitRequiresRegistrationAndRechecksItBeforeWake(t *testing.T) {
	s := newStore(t)
	item, err := oldWaiting(s, "登録前の定期タスクを待つ")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.update(item.ID, func(doc *Doc, _ time.Time) (Event, error) {
		doc.Set("after_run", "S1001")
		return Event{Type: EventField}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.Get(item.ID)
	if err != nil || !strings.Contains(pending.ReadyLabel(false).String(), "未登録") || len(StateWarnings(pending, nil, StateContext{})) != 1 {
		t.Fatalf("missing registration not visible: %+v, %v", pending, err)
	}
	if woken := s.WakeWithRuns(s.now(), func(string, time.Time) (RoutineCompletion, bool) {
		t.Fatal("wait with no timestamp looked up a past run")
		return RoutineCompletion{}, false
	}); len(woken) != 0 {
		t.Fatalf("woke unregistered wait: %+v", woken)
	}
	if _, changed, err := s.SetField(item.ID, "after_run", "S1001", ""); err != nil || !changed {
		t.Fatalf("cannot repair missing registration: %t, %v", changed, err)
	}
	if _, changed, err := s.SetField(item.ID, "after_run", "S1001", ""); err != nil || changed {
		t.Fatalf("registering the same value is not idempotent: %t, %v", changed, err)
	}
	woken := s.WakeWithRuns(s.now().Add(time.Hour), func(string, time.Time) (RoutineCompletion, bool) {
		if _, _, err := s.SetField(item.ID, "after_run", "S1002", ""); err != nil {
			t.Fatal(err)
		}
		return RoutineCompletion{ID: "stale-run"}, true
	})
	pending, err = s.Get(item.ID)
	if len(woken) != 0 || err != nil || pending.AfterRun != "S1002" {
		t.Fatalf("wake consumed a changed condition: %+v, %+v, %v", woken, pending, err)
	}
	// The last condition of a waiting task cannot go; the task stops waiting first.
	if _, _, err := s.SetStatus(item.ID, Doing, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetField(item.ID, "after_run", "", ""); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Get(item.ID)
	if err != nil || pending.AfterRunSince != "" || pending.HasReadyConditions() {
		t.Fatalf("clearing the wait left its registration: %+v, %v", pending, err)
	}
}

func TestReadyRunReferenceOnlyLinksToLocalRoutines(t *testing.T) {
	for _, value := range []string{"javascript:alert(1)", "//example.net/routines/S1001", "/routines/../other", "/routines/S1001%2Fother", `/\example.net/routines/S1001`, "/routines/S1001#run-extra) [link](https://example.net"} {
		if got := readyRunReference(value); got != "" {
			t.Errorf("unsafe reference accepted: %q", got)
		}
	}
	if value := "/routines/S1001?month=2026-11#run-exact"; readyRunReference(value) != value {
		t.Errorf("local reference rejected: %q", value)
	}
}

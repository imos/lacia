package task

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRoutineActionsMergeAndRecover(t *testing.T) {
	s := newRoutineStore(t)
	const id = "S1011"
	if _, err := s.WriteDoc(id, TaskFile, routineBody, "テスト"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 30, 14, 59, 0, 0, time.UTC)
	network := RoutineAction{Key: "run:scope:read", Scope: "read", Revision: "connect", Text: "接続を確かめます。", Technical: true}
	owner := RoutineAction{Key: "matter:123", Revision: "1", Text: "レビューをお願いします。", Reference: "/routines/S1011/receipts/3", Owner: true}
	apply := func(offset time.Duration, actions []RoutineAction, verified, resolved []string) Task {
		t.Helper()
		out, err := s.MergeRoutineActions(id, RoutineObservation{At: at.Add(offset), Actions: actions, Verified: verified, Resolved: resolved})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := apply(0, []RoutineAction{network, owner}, nil, nil)
	if got.Status != Review || got.Next() != owner.Text || !strings.Contains(got.Body, routineBody[:strings.Index(routineBody, "## 次にすること")]) {
		t.Fatalf("first review: %+v", got)
	}
	sequence, _, count := got.RoutineActionNotice()
	if sequence != 1 || count != 1 {
		t.Fatalf("notice: %d, %d", sequence, count)
	}
	if err := s.MarkRoutineActionNotified(id, sequence); err != nil {
		t.Fatal(err)
	}
	got = apply(0, []RoutineAction{network, owner}, nil, nil)
	if got.RoutineActions()[1].Count != 1 {
		t.Fatalf("retry increased count: %+v", got.RoutineActions())
	}
	owner.Revision = "2"
	owner.Text += " 調査結果を追記しました。"
	got = apply(time.Minute, []RoutineAction{network, owner}, nil, nil)
	if sequence, _, _ := got.RoutineActionNotice(); sequence != 0 || got.RoutineActions()[1].Count != 2 {
		t.Fatalf("repeat sent another notice: %+v", got.RoutineActions())
	}
	const manual = "## ひとことで\n\n手動で書いた本文です。\n\n## 次にすること\n\n別の操作もお願いしています。\n"
	if _, err := s.WriteDoc(id, TaskFile, manual, "担当"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetStatus(id, Review, "担当", "手動のお願いを加えました。"); err != nil {
		t.Fatal(err)
	}
	got = apply(2*time.Minute, nil, []string{"read"}, []string{"matter:123"})
	if got.Status != Review || got.Body != manual || len(got.RoutineActions()) != 0 {
		t.Fatalf("recovery must preserve manual review: %s %+v", got.Body, got.RoutineActions())
	}
	got = apply(time.Minute, []RoutineAction{network, owner}, nil, nil)
	if got.Body != manual || len(got.RoutineActions()) != 0 {
		t.Fatalf("late run reopened cleared actions: %+v", got)
	}
	s, _ = Open(s.Root())
	got = apply(3*time.Minute, []RoutineAction{network}, nil, nil)
	if len(got.RoutineActions()) != 1 || got.RoutineActions()[0].Count != 1 {
		t.Fatalf("a new episode should start at one: %+v", got.RoutineActions())
	}
}

func TestRoutineActionUsesTaskForManyFindings(t *testing.T) {
	s := newRoutineStore(t)
	at := time.Now()
	var actions []RoutineAction
	for n := 0; n < 120; n++ {
		actions = append(actions, RoutineAction{Key: "matter:" + string(rune('a'+n)), Text: "対応をお願いします。", Owner: true})
	}
	got, err := s.MergeRoutineActions("S1011", RoutineObservation{At: at, Actions: actions[:100]})
	if err != nil || len(got.RoutineActions()) != 100 || got.Status != Review {
		t.Fatal(err, len(got.RoutineActions()), got.Status)
	}
	again, err := s.MergeRoutineActions("S1011", RoutineObservation{At: at.Add(time.Minute), Actions: actions})
	if err != nil || len(again.RoutineActions()) != 120 || len(s.List()) != 0 {
		t.Fatal(err, len(again.RoutineActions()), s.List())
	}
	if read, err := s.MergeRoutineActions("S1011", RoutineObservation{At: at.Add(time.Minute), Actions: actions}); err != nil || !reflect.DeepEqual(read.RoutineActions(), again.RoutineActions()) {
		t.Fatal("duplicate observation changed items", err)
	}
	s.noTasks = true
	if _, err := s.MergeRoutineActions("S1011", RoutineObservation{At: at.Add(time.Hour), Actions: actions}); !errors.Is(err, ErrNoTasks) {
		t.Fatal(err)
	}
}

package task

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// oldWaiting makes a task that waits with no condition, the way records
// written before a waiting task needed one still do.
func oldWaiting(s *Store, title string) (Task, error) {
	item, err := s.Create(NewTask{Area: "work", Title: title})
	if err != nil {
		return item, err
	}
	item, _, err = s.update(item.ID, func(doc *Doc, _ time.Time) (Event, error) {
		doc.Set("status", string(Waiting))
		return Event{Type: EventStatus, From: string(Todo), To: string(Waiting)}, nil
	})
	return item, err
}

func TestWaitingNeedsACondition(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create(NewTask{Area: "work", Title: "待って作る", Status: Waiting}); !errors.Is(err, ErrWaitingNeedsCondition) {
		t.Fatalf("created a waiting task with no condition: %v", err)
	}
	item, err := s.Create(NewTask{Area: "work", Title: "返事を待つ", Status: Doing})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(NewTask{Area: "work", Title: "先に終えるタスク"})
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err := s.SetStatus(item.ID, Waiting, "テスト", "")
	if !errors.Is(err, ErrWaitingNeedsCondition) || changed {
		t.Fatalf("waiting with no condition: changed=%v err=%v", changed, err)
	}
	// The message alone has to tell a conversation what to do.
	for _, want := range []string{"lacia task field " + item.ID + " review_at", "lacia task field " + item.ID + " after ", "lacia task field " + item.ID + " after_run", "lacia wait add --task " + item.ID, "lacia task set " + item.ID + " review", "lacia task set " + item.ID + " waiting", OwnerName + "の判断や操作を待つとき"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
	if got, _ := s.Get(item.ID); got.Status != Doing {
		t.Fatalf("the refused change moved the task: %+v", got)
	}
	for _, c := range []Field{{"resume_on", "2026-12-01 09:00"}, {"review_at", "2026-12-01"}, {"after", other.ID}, {"after_run", "S1001"}} {
		if _, _, err := s.SetField(item.ID, c.Key, c.Value, ""); err != nil {
			t.Fatal(err)
		}
		if got, changed, err := s.SetStatus(item.ID, Waiting, "テスト", ""); err != nil || !changed || got.Status != Waiting {
			t.Fatalf("waiting with %s: %+v changed=%v err=%v", c.Key, got, changed, err)
		}
		// The only condition of a waiting task stays.
		if _, changed, err := s.SetField(item.ID, c.Key, "", ""); !errors.Is(err, ErrWaitingNeedsCondition) || changed {
			t.Fatalf("removed the last condition %s: changed=%v err=%v", c.Key, changed, err)
		}
		if got, _ := s.Get(item.ID); !got.HasReadyConditions() {
			t.Fatalf("%s was removed although refused: %+v", c.Key, got)
		}
		if _, _, err := s.SetStatus(item.ID, Doing, "テスト", ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SetField(item.ID, c.Key, "", ""); err != nil {
			t.Fatalf("removing %s from a task that does not wait: %v", c.Key, err)
		}
	}
	// One of two conditions may go.
	for _, c := range []Field{{"resume_on", "2026-12-01"}, {"review_at", "2026-12-02"}} {
		if _, _, err := s.SetField(item.ID, c.Key, c.Value, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SetStatus(item.ID, Waiting, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetField(item.ID, "resume_on", "", ""); err != nil {
		t.Fatalf("removing one of two conditions: %v", err)
	}
}

func TestWaitingAcceptsAConditionKeptByTheServer(t *testing.T) {
	s := newStore(t)
	item, err := s.Create(NewTask{Area: "work", Title: "計算を待つ", Status: Doing})
	if err != nil {
		t.Fatal(err)
	}
	watched := false
	s.SetLiveCondition(func(id string) bool { return watched && id == item.ID })
	if _, _, err := s.SetStatus(item.ID, Waiting, "", ""); !errors.Is(err, ErrWaitingNeedsCondition) {
		t.Fatalf("waiting before the wait was registered: %v", err)
	}
	watched = true
	if got, changed, err := s.SetStatus(strings.ToLower(item.ID), Waiting, "", ""); err != nil || !changed || got.Status != Waiting {
		t.Fatalf("waiting for a registered wait: %+v changed=%v err=%v", got, changed, err)
	}
}

func TestOldWaitingWithoutConditionStillLoads(t *testing.T) {
	s := newStore(t)
	item, err := oldWaiting(s, "以前から待っているタスク")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil || len(s.Problems()) != 0 {
		t.Fatalf("reload: %v %+v", err, s.Problems())
	}
	got, err := s.Get(item.ID)
	if err != nil || got.Status != Waiting || !got.MissingWaitingCondition(false) {
		t.Fatalf("old record: %+v %v", got, err)
	}
	// Saying "waiting" again changes nothing and is not an error.
	if _, changed, err := s.SetStatus(item.ID, Waiting, "", ""); err != nil || changed {
		t.Fatalf("repeating the status: changed=%v err=%v", changed, err)
	}
	if _, _, err := s.SetStatus(item.ID, Review, "", ""); err != nil {
		t.Fatalf("leaving: %v", err)
	}
}

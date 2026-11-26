package handover

import (
	"sync"
	"testing"
	"time"
)

func TestWaitOutboxSurvivesRetryAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := Handover{TaskID: "T1", WaitID: "W12345678", Next: "codex", Then: "結果を集計します。"}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, _, err := s.Add(h)
			if err != nil || got.ID != "H12345678" {
				t.Errorf("add: %+v %v", got, err)
			}
		})
	}
	wg.Wait()
	if len(s.List(nil)) != 1 {
		t.Fatal("duplicate outbox messages")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, added, err := reopened.Add(h)
	if err != nil || added || got.ID != "H12345678" {
		t.Fatalf("retry after reopen: %+v %v %v", got, added, err)
	}
}
func TestStateControlsDeliveryAndDeadlines(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		state                  string
		runnable, late, closed bool
	}{
		{Queued, true, true, false}, {Delivered, false, true, false}, {Told, true, false, false}, {ToldDelivered, false, false, false}, {Acked, false, false, true}, {Skipped, false, false, true}, {Cancelled, false, false, true},
	} {
		h := Handover{State: c.state, Created: now.Add(-Grace)}
		if h.Runnable() != c.runnable || h.Late(now) != c.late || h.Closed() != c.closed {
			t.Fatalf("state %s: %+v", c.state, h)
		}
		if h.Stale(now.Add(StartWithin)) != c.runnable {
			t.Fatalf("stale state %s", c.state)
		}
	}
}

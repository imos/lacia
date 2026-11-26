package notify

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestMeetingRechecks(t *testing.T) {
	n := Notice{ID: "old", Fire: 1940, Expires: 2000}
	s := NewState()
	s.Meetings = []Notice{n}
	at := func(sec int64) time.Time { return time.Unix(sec, 0) }
	if len(MeetingRechecks(s, at(1399))) != 0 {
		t.Fatal("early check")
	}
	checks := MeetingRechecks(s, at(1400))
	if checks[n.ID] != 10 {
		t.Fatal(checks)
	}
	RecordMeetingRechecks(s, checks)
	if len(MeetingRechecks(s, at(1939))) != 0 {
		t.Fatal("duplicate ten-minute check")
	}
	checks = MeetingRechecks(s, at(1940))
	if checks[n.ID] != 1 {
		t.Fatal(checks)
	}
	RecordMeetingRechecks(s, checks)
	if len(MeetingRechecks(s, at(1950))) != 0 {
		t.Fatal("duplicate one-minute check")
	}
	// A moved event has a new occurrence ID and both its own checkpoints.
	n.ID = "moved"
	n.Fire = 2540
	n.Expires = 2600
	s.Meetings = []Notice{n}
	checks = MeetingRechecks(s, at(2000))
	if checks[n.ID] != 10 {
		t.Fatal(checks)
	}
	RecordMeetingRechecks(s, checks)
	if _, ok := s.MeetingChecks["old"]; ok {
		t.Fatal("obsolete checkpoint retained")
	}
	if MeetingRechecks(s, at(2540))[n.ID] != 1 {
		t.Fatal("moved event not checked")
	}
	if len(MeetingRechecks(s, at(2600))) != 0 {
		t.Fatal("expired check")
	}
}

func TestMeetingCheckWakeup(t *testing.T) {
	s := NewState()
	s.Meetings = []Notice{{ID: "a", Expires: 2000, Fire: 1940}}
	if got := NextMeetingCheck(s, time.Unix(1390, 0)); got != 10*time.Second {
		t.Fatal(got)
	}
	if got := NextMeetingCheck(s, time.Unix(1939, 0)); got != time.Second {
		t.Fatal(got)
	}
	if got := NextMeetingCheck(s, time.Unix(1000, 0)); got != 30*time.Second {
		t.Fatal(got)
	}
}

func TestMeetingCheckFailureSurvivesRestart(t *testing.T) {
	s := NewState()
	s.Meetings = []Notice{{ID: "a", Expires: 2000, Fire: 1940}}
	// Even a failed request is an attempt; delivery stays in the plan.
	RecordMeetingRechecks(s, MeetingRechecks(s, time.Unix(1940, 0)))
	path := filepath.Join(t.TempDir(), "private", "state.json")
	if err := SaveState(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Meetings) != 1 || loaded.MeetingChecks["a"] != 1 || len(MeetingRechecks(loaded, time.Unix(1945, 0))) != 0 {
		t.Fatal(loaded)
	}
}

func TestMeetingRechecksApplyCancellationAndUnknown(t *testing.T) {
	config := DefaultConfig()
	initialAt := time.Date(2026, 11, 10, 9, 0, 0, 0, time.FixedZone("JST", 9*3600))
	for _, lead := range []time.Duration{10 * time.Minute, time.Minute} {
		for _, test := range []struct {
			name     string
			events   []any
			metadata map[string]any
			keepOld  bool
		}{
			{"cancelled", []any{linkedEvent(map[string]any{"status": "cancelled"})}, nil, false},
			{"complete absence", []any{}, nil, false},
			{"partial absence", []any{}, map[string]any{"status": "partial", "complete": false}, true},
			{"previous value", []any{linkedEvent(nil)}, map[string]any{"updated_at": "2026-11-10T08:00:00+09:00"}, true},
			{"moved", []any{linkedEvent(map[string]any{"start": map[string]any{"dateTime": "2026-11-10T12:00:00+09:00"}, "end": map[string]any{"dateTime": "2026-11-10T13:00:00+09:00"}})}, nil, false},
		} {
			t.Run(test.name+lead.String(), func(t *testing.T) {
				s := NewState()
				raw, _ := json.Marshal(linkedSnapshot([]any{linkedEvent(nil)}, map[string]any{"updated_at": initialAt.Format(time.RFC3339)}))
				initial := Plan(Input{Config: config, Now: initialAt, Snapshot: raw}, s)
				if len(initial.Notices) != 1 {
					t.Fatal(initial)
				}
				old := initial.Notices[0]
				at := time.Unix(int64(old.Expires), 0).Add(-lead)
				metadata := map[string]any{"updated_at": at.Format(time.RFC3339)}
				for k, v := range test.metadata {
					metadata[k] = v
				}
				raw, _ = json.Marshal(linkedSnapshot(test.events, metadata))
				checks := MeetingRechecks(s, at)
				if len(checks) != 1 {
					t.Fatal(checks)
				}
				RecordMeetingRechecks(s, checks)
				got := Plan(Input{Config: config, Now: at, Snapshot: raw}, s)
				retained := false
				for _, n := range got.Notices {
					if n.ID == old.ID {
						retained = true
					}
				}
				if retained != test.keepOld {
					t.Fatalf("retained=%v want=%v: %+v", retained, test.keepOld, got)
				}
			})
		}
	}
}

func TestMeetingMovedIntoDeliveryWindow(t *testing.T) {
	initialAt := testNow
	s := NewState()
	cfg := DefaultConfig()
	raw, _ := json.Marshal(linkedSnapshot([]any{linkedEvent(nil)}, nil))
	original := Plan(Input{Config: cfg, Now: initialAt, Snapshot: raw}, s)
	if len(original.Notices) != 1 {
		t.Fatal(original)
	}
	old := original.Notices[0]
	at := time.Unix(int64(old.Fire), 0)
	newStart := at.Add(30 * time.Second)
	event := linkedEvent(map[string]any{"start": map[string]any{"dateTime": newStart.Format(time.RFC3339)}, "end": map[string]any{"dateTime": newStart.Add(time.Hour).Format(time.RFC3339)}})
	raw, _ = json.Marshal(linkedSnapshot([]any{event}, map[string]any{"updated_at": at.Format(time.RFC3339)}))
	moved := Plan(Input{Config: cfg, Now: at, Snapshot: raw}, s)
	if len(moved.Notices) != 1 || moved.Notices[0].ID == old.ID || moved.Notices[0].Expires != seconds(newStart) {
		t.Fatal(moved)
	}
	if got := Plan(Input{Config: cfg, Now: at, Snapshot: raw}, NewState()); len(got.Notices) != 0 {
		t.Fatal("unplanned late event", got)
	}
}

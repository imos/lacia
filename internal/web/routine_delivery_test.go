package web

import (
	"strings"
	"testing"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/schedule"
)

func TestRoutineNativeNoticesPreserveTheAssignedTextRoute(t *testing.T) {
	s, h := setupRoutine(t)
	q, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	s.Requests = q
	job := schedule.Job{ID: "S1001", Name: "S1001", ReplyTo: "discord:new-route"}
	s.Schedules = func() ([]schedule.Job, map[string]string) { return []schedule.Job{job}, nil }
	for _, test := range []struct{ id, route string }{{"sched-text-2026-11-25-0600", "discord:original"}, {"sched-native-2026-11-25-0600", ""}} {
		if _, _, err := q.Add(request.Request{ID: test.id, Kind: request.System, ReplyTo: test.route, Text: "確認します。"}); err != nil {
			t.Fatal(err)
		}
		acceptRoutine(t, s, "S1001", test.id)
	}
	code, body, _ := get(t, h, "/api/routine-notices")
	if code != 200 || strings.Count(body, `"routine_id":"S1001"`) != 1 || !strings.Contains(body, `"sequence":2`) {
		t.Fatalf("duplicate text/native route: %d %s", code, body)
	}
	snapshot, err := s.Routines.Snapshot("S1001")
	if err != nil || len(snapshot.Pending) != 2 || snapshot.Receipts[0].Notified || snapshot.Receipts[1].Notified {
		t.Fatal("reading notices acknowledged or reviewed results", snapshot, err)
	}
}

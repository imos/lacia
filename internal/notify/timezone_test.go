package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMeetingZoneMetadataAndOverride(t *testing.T) {
	snap := linkedSnapshot([]any{linkedEvent(map[string]any{"start": map[string]any{"dateTime": "2026-11-10T09:02:00+09:00"}})}, nil)
	snap["timezone"] = "UTC"
	raw, _ := json.Marshal(snap)
	notices, _, err := PlanMeetings(raw, Config{}, &State{}, testNow, testSite)
	if err != nil || len(notices) != 1 || !strings.HasPrefix(notices[0].Body, "00:") {
		t.Fatalf("%v %v", notices, err)
	}
	loc, _ := time.LoadLocation("America/New_York")
	notices, _, err = PlanMeetings(raw, Config{Location: loc}, &State{}, testNow, testSite)
	if err != nil || len(notices) != 1 || !strings.HasPrefix(notices[0].Body, "19:") {
		t.Fatalf("%v %v", notices, err)
	}
}

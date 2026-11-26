package calendar

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAlternateSnapshotZoneAndDSTLayout(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	raw := fixture(obj{"id": "morning", "title": "朝", "start": obj{"dateTime": "2026-03-08T09:00:00-04:00"}, "end": obj{"dateTime": "2026-03-08T10:00:00-04:00"}})
	raw["timezone"] = "America/New_York"
	raw["range_start"] = "2026-03-08"
	raw["range_end"] = "2026-03-15"
	data, _ := json.Marshal(raw)
	snap, err := parse(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RangeStart.Location().String() != loc.String() {
		t.Fatal(snap.RangeStart)
	}
	now := time.Date(2026, 3, 8, 9, 0, 0, 0, loc)
	view := BuildIn(snap, now, loc)
	if view.AsOf.Hour() != 9 || view.AsOf.Location() != loc {
		t.Fatal(view.AsOf)
	}
	event := Event{Start: now, End: now.Add(time.Hour)}
	seg := clipDay(&event, time.Date(2026, 3, 8, 0, 0, 0, 0, loc))
	if seg.From != 540 || seg.To != 600 {
		t.Fatalf("DST axis=%+v", seg)
	}
	old := fixture()
	data, _ = json.Marshal(old)
	if _, err := parse(data, nil); err != nil {
		t.Fatal("legacy Tokyo snapshot rejected", err)
	}
	for _, zone := range []string{"UTC", "Europe/London"} {
		raw["timezone"] = zone
		data, _ = json.Marshal(raw)
		if _, err := parse(data, nil); err != nil {
			t.Fatalf("%s: %v", zone, err)
		}
	}
}

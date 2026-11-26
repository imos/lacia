package calendar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgendaSharesScopeAndKeepsCoverageAndTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calendar.json")
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, JST)
	raw := fixture(obj{"id": "one", "title": "非公開の架空の題名", "visibility": "private",
		"start": obj{"date": "2026-11-10"}, "end": obj{"date": "2026-11-11"},
		"html_link": "https://calendar.google.com/calendar/event?eid=fixture"})
	raw["updated_at"] = "2026-11-10T08:00:00+09:00"
	s := source(raw, 0)
	s["status"], s["complete"] = "partial", false
	s["attempted_at"], s["range_start"], s["range_end"] = raw["updated_at"], "2026-11-09", "2026-11-11"
	data, _ := json.Marshal(raw)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReader(path)
	previous := Scope
	Scope = nil
	t.Cleanup(func() { Scope = previous })
	agenda, err := r.List(now, "today", "", "")
	if err != nil || agenda.Status != "partial" || len(agenda.Events) != 1 || agenda.Events[0].Title != "非公開の予定" {
		t.Fatalf("today = %+v, %v", agenda, err)
	}
	if src := agenda.Sources[0]; !src.Covered || src.AttemptedAt == nil || src.SucceededAt == nil || !src.AttemptedAt.After(*src.SucceededAt) {
		t.Fatalf("source = %+v", src)
	}
	tomorrow, err := r.List(now, "tomorrow", "", "")
	if err != nil || tomorrow.Status == "ok" || tomorrow.Sources[0].Covered || len(tomorrow.Events) != 0 {
		t.Fatalf("tomorrow = %+v, %v", tomorrow, err)
	}
}

func TestHolidaySettingKeepsWeekendAndShowsCoverage(t *testing.T) {
	now := time.Date(2027, 11, 23, 12, 0, 0, 0, JST)
	with := buildView(Snapshot{}, now, false, JST, false)
	without := buildView(Snapshot{}, now, false, JST, true)
	if with.Week.Days[0].HolidayName == "" || without.Week.Days[0].HolidayName != "" ||
		without.Week.Days[4].DateClass != "is-saturday" {
		t.Fatalf("holiday or weekend is wrong: %+v / %+v", with.Week.Days, without.Week.Days)
	}
	lastYear := time.Date(lastHolidayYear(), 1, 1, 0, 0, 0, 0, JST)
	if HolidayDataNote(lastYear.AddDate(0, 0, -1)) != "" || HolidayDataNote(lastYear) == "" {
		t.Fatal("holiday coverage is reported at the wrong time")
	}
}

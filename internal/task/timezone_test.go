package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositoryZoneTaskBoundaryAndHistory(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0755)
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"format":1,"areas":["work"],"timezone":"America/New_York"}`), 0600)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-12-01T02:15:00Z")
	s.now = func() time.Time { return now }
	item, err := s.Create(NewTask{Area: "work", Title: "日付境界"})
	if err != nil {
		t.Fatal(err)
	}
	if item.Created != "2026-11-30" || !strings.Contains(item.Dir, "2026-11/") || item.Timezone != "America/New_York" {
		t.Fatal(item)
	}
	item, _, err = s.SetField(item.ID, "resume_on", "2026-12-01", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !item.Snoozed(now) || len(s.Wake(now)) != 0 {
		t.Fatal("resumed before local midnight")
	}
	now = now.Add(3 * time.Hour)
	if len(s.Wake(now)) != 1 {
		t.Fatal("did not wake at local midnight")
	}
	item, _ = s.Get(item.ID)
	lines, err := s.HistoryLines(item, 0)
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "2026-11-30 21:15") {
		t.Fatalf("%v %v", lines, err)
	}
	events, _ := ReadEvents(filepath.Join(root, item.Dir, HistoryFile))
	if !strings.HasSuffix(events[0].Event.At, "-05:00") {
		t.Fatal(events[0].Event.At)
	}
}

func TestTimezoneValidationAndDSTGap(t *testing.T) {
	for _, zone := range []string{"UTC", "America/New_York", "Invalid/Timezone", "Local"} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, ".lacia"), 0755)
		os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"timezone":"`+zone+`"}`), 0600)
		_, err := LoadConfig(root)
		if (err != nil) != (zone == "Invalid/Timezone" || zone == "Local") {
			t.Errorf("zone %s: %v", zone, err)
		}
	}
	loc, _ := time.LoadLocation("America/New_York")
	if _, err := ParseWhenIn("2026-03-08 02:30", loc); err == nil {
		t.Fatal("accepted nonexistent local time")
	}
	if got := FormatWhenIn(time.Date(2026, 11, 25, 4, 59, 0, 0, time.UTC), loc); got != "2026-11-24 23:59" {
		t.Fatal(got)
	}
}

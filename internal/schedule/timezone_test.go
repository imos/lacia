package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func zoneTime(t *testing.T, loc *time.Location, value string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
func scheduleFixture(t *testing.T, zone, definition string) (string, string) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0755)
	os.MkdirAll(filepath.Join(root, "schedules"), 0755)
	os.WriteFile(filepath.Join(root, ".lacia/config.json"), []byte(`{"format":1,"timezone":"`+zone+`"}`), 0600)
	file := filepath.Join(root, "schedules/check.md")
	os.WriteFile(file, []byte("---\n"+definition+"---\n\n確認します。\n"), 0600)
	return root, file
}

func TestMultipleTimesAndClosedTask(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	root, _ := scheduleFixture(t, loc.String(), "daily: 17:00, 09:00\ntask: T1001\n")
	jobs := Load(root)
	if jobs[0].Daily != "09:00, 17:00" || jobs[0].Task != "T1001" {
		t.Fatal(jobs)
	}
	var got []Job
	closed := false
	r := &Runner{Root: root, State: filepath.Join(t.TempDir(), "state.json"), TaskClosed: func(string) bool { return closed }, Submit: func(id string, j Job) error { got = append(got, j); return nil }}
	for _, value := range []string{"2026-11-24 09:00", "2026-11-24 09:00", "2026-11-24 17:00"} {
		if err := r.Tick(zoneTime(t, loc, value)); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0].Slot.Hour() != 9 || got[1].Slot.Hour() != 17 {
		t.Fatal(got)
	}
	closed = true
	r.Tick(zoneTime(t, loc, "2026-11-25 09:00"))
	if len(got) != 2 {
		t.Fatal("closed task ran")
	}
}

func TestSituationExpiryUsesRepositoryDate(t *testing.T) {
	root, _ := scheduleFixture(t, "America/New_York", "daily: 09:00\n")
	os.MkdirAll(filepath.Join(root, "memory"), 0755)
	text := "- 2026-11-23〜2026-11-24：旅行です。\n- 2026-11-01〜2026-11-23：古い予定です。\n- 2026-99-01〜2026-99-02：日付が不正です。\n通常の行です。\n"
	file := filepath.Join(root, "memory/now.md")
	os.WriteFile(file, []byte(text), 0600)
	now, _ := time.Parse(time.RFC3339, "2026-11-25T02:00:00Z")
	got := Situation(root, "memory/now.md", now)
	if !strings.Contains(got, "旅行です") || strings.Contains(got, "古い予定です") || !strings.Contains(got, "日付が不正") || !strings.Contains(got, "きょうは 2026-11-24") {
		t.Fatal(got)
	}
	got = Situation(root, "memory/now.md", now.Add(4*time.Hour))
	if strings.Contains(got, "旅行です") {
		t.Fatal(got)
	}
	after, _ := os.ReadFile(file)
	if string(after) != text {
		t.Fatal("source changed")
	}
}

func TestInvalidTimesAndDefinition(t *testing.T) {
	for _, definition := range []string{"daily: 09:00, 09:00\n", "daily: 09:00, 25:00\n", "at: 2026-11-24 17:00\n", "daily: 09:00\ntask: invalid\n"} {
		root, _ := scheduleFixture(t, "America/New_York", definition)
		if job := Load(root)[0]; job.Err == "" || job.Enabled {
			t.Errorf("accepted %s", definition)
		}
	}
}

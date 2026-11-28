package daylog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfiguredDayAndClock(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	log := Notices(records(t, t.TempDir()), "")
	log.Location = loc
	at, _ := time.Parse(time.RFC3339, "2026-12-01T02:15:00Z")
	if _, err := log.Add(Entry{Key: "test", At: at, Who: "test", Text: "記録します。"}); err != nil {
		t.Fatal(err)
	}
	path := log.Path(at)
	if filepath.Base(path) != "2026-11-30.md" {
		t.Fatal(path)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "時刻は America/New_York で表示します。") || !strings.Contains(string(data), "21:15") {
		t.Fatal(string(data))
	}
}

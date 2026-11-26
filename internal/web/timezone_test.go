package web

import (
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/task"
)

func TestHistoryDatesUseConfiguredZone(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	at := "2026-12-01T02:15:00Z"
	if got := eventDate(at, loc); got != "2026-11-30 21:15" {
		t.Fatal(got)
	}
	if got := eventDate(at); got != "2026-12-01 11:15" {
		t.Fatal(got)
	}
	lines := []task.Line{{OK: true, Event: task.Event{Type: task.EventStatus, At: at, From: "doing", To: "review", Text: "見ていただきます。"}}}
	if got := reviewSince(lines, loc); got != "2026-11-30 21:15" {
		t.Fatal(got)
	}
	groups := groupedHistory(lines, "task", loc)
	if len(groups) != 1 || !strings.Contains(groups[0].Entries[0].Text, "2026-11-30 21:15") {
		t.Fatal(groups)
	}
}

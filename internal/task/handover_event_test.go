package task

import (
	"strings"
	"testing"
)

func TestHandoverMessagesAndBodySnapshotsHaveDistinctHistoryText(t *testing.T) {
	for _, c := range []struct {
		event Event
		want  string
	}{
		{Event{Type: EventHandover, Body: "## 本文"}, "このときの本文を残しました。"},
		{Event{Type: EventHandover, Handover: "H123", Text: "申し送り H123 を受け取りました。"}, "申し送り H123 を受け取りました。"},
	} {
		c.event.At = "2026-11-24T14:00:00+09:00"
		line := Line{Event: c.event, OK: true}
		if got := line.Format(); !strings.Contains(got, c.want) {
			t.Fatalf("history = %s", got)
		}
	}
}

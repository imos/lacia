package main

import (
	"context"
	"testing"
	"time"

	"github.com/imos/lacia/internal/request"
)

func TestRolloverWindow(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	for _, tt := range []struct {
		window string
		nowUTC string
		night  string
	}{
		{"", "2026-11-26T18:00:00Z", ""},
		{"03:00-05:00", "2026-11-26T17:59:59Z", ""},
		{"03:00-05:00", "2026-11-26T18:00:00Z", "2026-11-27"},
		{"03:00-05:00", "2026-11-26T20:00:00Z", ""},
		{"23:00-02:00", "2026-11-26T14:00:00Z", "2026-11-26"},
		{"23:00-02:00", "2026-11-26T16:59:59Z", "2026-11-26"},
		{"23:00-02:00", "2026-11-26T17:00:00Z", ""},
	} {
		w, err := parseRolloverWindow(tt.window)
		if err != nil || w.enabled() != (tt.window != "") {
			t.Fatalf("parse %q: %+v, %v", tt.window, w, err)
		}
		now, err := time.Parse(time.RFC3339, tt.nowUTC)
		if err != nil {
			t.Fatal(err)
		}
		if got := w.night(now.In(loc)); got != tt.night {
			t.Errorf("%s in %q: got %q, want %q", tt.nowUTC, tt.window, got, tt.night)
		}
	}
	for _, text := range []string{"夜", "03:00-03:00", "22:00-24:00", "3:00-04:00", "01:00", "00:00-02:04:00"} {
		if _, err := parseRolloverWindow(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}

func TestMainRolloverBeforeFirstRequestAfterWake(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	start := time.Date(2026, 11, 27, 5, 0, 0, 0, loc)
	q, err := request.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := parseRolloverWindow("05:00-12:00")
	if err != nil {
		t.Fatal(err)
	}
	at := start.Add(-time.Second)
	calls := 0
	sessions := []windowSession{{kind: "codex", beforeRequest: func(_ context.Context, day string, ready func() bool) (bool, error) {
		calls++
		if day != "2026-11-27" || !ready() {
			t.Errorf("unexpected rollover: %q", day)
		}
		return true, nil
	}}}
	check := func(r request.Request, want int) {
		t.Helper()
		w.beforeMain(t.Context(), func() time.Time { return at }, q, sessions, "codex", r)
		if calls != want {
			t.Fatalf("at %s: got %d attempts, want %d", at.Format(time.TimeOnly), calls, want)
		}
	}
	waiting := request.Request{Created: start.Add(-time.Minute)}
	check(waiting, 0)
	at = start
	check(waiting, 0)
	check(request.Request{Created: start}, 1)
	// The first request after wake must check wall time without a timer tick.
	at = start.Add(4 * time.Hour)
	check(request.Request{Created: at}, 2)
	check(request.Request{ID: "child-subagent", Kind: request.System, Source: "Codex", Created: at}, 2)
	if _, _, err := q.Add(request.Request{ID: "child-buffered", Kind: request.System, Source: "Codex", Text: "子の完了"}); err != nil {
		t.Fatal(err)
	}
	check(request.Request{Created: at}, 2)
	at = start.Add(7 * time.Hour)
	check(request.Request{Created: at}, 2)
	w = rolloverWindow{}
	check(request.Request{Created: start}, 2)
}

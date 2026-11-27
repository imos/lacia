package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/imos/lacia/internal/request"
)

type rolloverWindow struct{ start, end int }

func parseRolloverWindow(text string) (rolloverWindow, error) {
	if strings.TrimSpace(text) == "" {
		return rolloverWindow{}, nil
	}
	start, end, ok := strings.Cut(text, "-")
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	from, fromErr := time.Parse("15:04", start)
	to, toErr := time.Parse("15:04", end)
	if !ok || fromErr != nil || toErr != nil || from.Format("15:04") != start || to.Format("15:04") != end || from.Equal(to) {
		return rolloverWindow{}, errors.New("--window-rollover には、開始と終了が異なる時間帯を HH:MM-HH:MM の形で指定してください")
	}
	return rolloverWindow{start: from.Hour()*60 + from.Minute(), end: to.Hour()*60 + to.Minute()}, nil
}

func (w rolloverWindow) enabled() bool { return w.start != w.end }

// night identifies a window by the local date on which it starts.
func (w rolloverWindow) night(now time.Time) string {
	minute := now.Hour()*60 + now.Minute()
	switch {
	case w.start < w.end && minute >= w.start && minute < w.end:
	case w.start > w.end && minute >= w.start:
	case w.start > w.end && minute < w.end:
		now = now.AddDate(0, 0, -1)
	default:
		return ""
	}
	return now.Format(time.DateOnly)
}

// startOf returns the local start of the window that contains now.
func (w rolloverWindow) startOf(now time.Time) time.Time {
	if w.start > w.end && now.Hour()*60+now.Minute() < w.end {
		now = now.AddDate(0, 0, -1)
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, w.start/60, w.start%60, 0, 0, now.Location())
}

func (w rolloverWindow) run(ctx context.Context, location *time.Location, attempt func(string)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if night := w.night(time.Now().In(location)); night != "" {
			attempt(night)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type windowSession struct {
	kind          string
	rollover      func(night string, ready func() bool) (bool, error)
	beforeRequest func(context.Context, string, func() bool) (bool, error)
}

func isChildNotice(r request.Request) bool {
	return r.Kind == request.System && r.Source == "Codex" && strings.HasPrefix(r.ID, "child-")
}

func (w rolloverWindow) beforeMain(ctx context.Context, now func() time.Time, q *request.Queue, sessions []windowSession, kind string, r request.Request) {
	at := now()
	night := w.night(at)
	if night == "" {
		return
	}
	start := w.startOf(at)
	keep := func(pending request.Request) bool {
		return pending.Created.Before(start) || isChildNotice(pending)
	}
	ready := func() bool { return w.night(now()) == night && !q.MainQueued(keep) }
	if keep(r) || !ready() {
		return
	}
	for _, session := range sessions {
		if session.kind == kind {
			attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
			changed, err := session.beforeRequest(attempt, night, ready)
			cancel()
			logRollover(kind, night, changed, err)
			return
		}
	}
}

func rolloverSessions(sessions []windowSession, night string, ready func() bool) {
	for _, session := range sessions {
		changed, err := session.rollover(night, ready)
		logRollover(session.kind, night, changed, err)
	}
}

func logRollover(kind, night string, changed bool, err error) {
	switch {
	case err != nil:
		slog.Error("window session rollover", "detail", "窓口で使う会話を新しくできませんでした。", "kind", kind, "night", night, "error", err)
	case changed:
		slog.Info("window session rolled over", "kind", kind, "night", night)
	}
}

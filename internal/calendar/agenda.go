package calendar

import (
	"errors"
	"time"
)

// Agenda is the presentation available to conversations. It excludes
// authenticated event links, account IDs, and private event titles.
type Agenda struct {
	Timezone    string         `json:"timezone"`
	Start       string         `json:"start"`
	Until       string         `json:"until"`
	Status      string         `json:"status"`
	AttemptedAt *time.Time     `json:"attempted_at,omitempty"`
	Sources     []AgendaSource `json:"sources"`
	Events      []AgendaEvent  `json:"events"`
}

type AgendaSource struct {
	Label       string     `json:"label"`
	Status      string     `json:"status"`
	Stale       bool       `json:"stale"`
	Covered     bool       `json:"covered"`
	SucceededAt *time.Time `json:"succeeded_at,omitempty"`
	AttemptedAt *time.Time `json:"attempted_at,omitempty"`
	RangeStart  string     `json:"range_start,omitempty"`
	RangeEnd    string     `json:"range_end,omitempty"`
}

type AgendaEvent struct {
	Calendar string    `json:"calendar"`
	Title    string    `json:"title"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	AllDay   bool      `json:"all_day,omitempty"`
	Point    bool      `json:"point,omitempty"`
}

// List applies the Web calendar's scope, display rules and time zone.
func (r *Reader) List(now time.Time, period, from, until string) (Agenda, error) {
	snap, loc, readErr := r.snapshot(now)
	begin, end, err := agendaPeriod(now.In(loc), period, from, until)
	if err != nil {
		return Agenda{}, err
	}
	sources, events := observedEvents(snap, now, loc)
	result := Agenda{
		Timezone: loc.String(), Start: ymd(begin), Until: ymd(end), Status: "unavailable",
		AttemptedAt: nonzeroTime(snap.UpdatedAt.In(loc)), Sources: []AgendaSource{}, Events: []AgendaEvent{},
	}
	confirmed := 0
	for _, source := range sources {
		s := AgendaSource{
			Label: source.Label, Status: source.Status, Stale: source.Stale || readErr != nil,
			SucceededAt: nonzeroTime(source.UpdatedAt), AttemptedAt: nonzeroTime(source.AttemptedAt),
		}
		if !source.RangeStart.IsZero() && !source.RangeEnd.IsZero() {
			s.RangeStart, s.RangeEnd = ymd(source.RangeStart), ymd(source.RangeEnd)
			s.Covered = (!source.ScopeRequired || source.ScopeVerified) && !source.RangeStart.After(begin) && !source.RangeEnd.Before(end)
		}
		if s.Covered && !s.Stale && s.Status == "ok" {
			confirmed++
		}
		result.Sources = append(result.Sources, s)
	}
	if confirmed > 0 && confirmed == len(sources) {
		result.Status = "ok"
	} else if confirmed > 0 || snap.Available {
		result.Status = "partial"
	}
	visible, _ := selectEvents(events, begin, end, period == "week")
	for _, e := range visible {
		if e.Private && e.Point {
			continue
		}
		title := titleOf(&e)
		if e.Private {
			title = "非公開の予定"
		}
		result.Events = append(result.Events, AgendaEvent{
			Calendar: e.Source, Title: title, Start: e.Start, End: e.End, AllDay: e.AllDay, Point: e.Point,
		})
	}
	return result, nil
}

func agendaPeriod(now time.Time, period, from, until string) (time.Time, time.Time, error) {
	today := startOfDay(now)
	var zero time.Time
	if from != "" || until != "" {
		if period != "" || from == "" {
			return zero, zero, errors.New("期間は今日・明日・7日間、または開始日と終了日で指定してください")
		}
		begin, err := parseDate(from, now.Location())
		if err != nil {
			return zero, zero, errors.New("開始日は YYYY-MM-DD で指定してください")
		}
		end := begin.AddDate(0, 0, 1)
		if until != "" {
			end, err = parseDate(until, now.Location())
		}
		if err != nil || !end.After(begin) || end.After(begin.AddDate(0, 0, 7)) {
			return zero, zero, errors.New("終了日は開始日の翌日から7日後までを YYYY-MM-DD で指定してください")
		}
		return begin, end, nil
	}
	switch period {
	case "", "today":
		return today, today.AddDate(0, 0, 1), nil
	case "tomorrow":
		return today.AddDate(0, 0, 1), today.AddDate(0, 0, 2), nil
	case "week":
		return today, today.AddDate(0, 0, 7), nil
	default:
		return zero, zero, errors.New("period は today、tomorrow、week のいずれかを指定してください")
	}
}

func nonzeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

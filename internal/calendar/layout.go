package calendar

import (
	"math"
	"sort"
	"time"
)

// The shared vertical axis of today and tomorrow, in minutes after midnight.
const (
	AxisStart = 8 * 60
	AxisEnd   = 20 * 60
	axisSpan  = AxisEnd - AxisStart
)

type segmentKind int

const (
	kindTimed segmentKind = iota
	kindAllDay
	kindPoint
)

// segment is the part of one event that falls on one JST day.
type segment struct {
	Event      *Event
	Kind       segmentKind
	Start, End time.Time
	From, To   float64 // minutes after that day's midnight, clipped to the day
	Before     bool    // starts on an earlier day
	After      bool    // ends on a later day
	Early      bool    // reaches before the axis
	Late       bool    // reaches after the axis
	Axis       bool    // drawn on the axis
	// Set for axis blocks only.
	Top, Bottom float64 // From and To clipped to the axis
	Col, Cols   int
}

type overlapSpan struct {
	From, To float64
	Count    int
}

type dayLayout struct {
	Day      time.Time
	All      []*segment
	AllDay   []*segment
	Early    []*segment
	Late     []*segment
	Blocks   []*segment
	Points   []*segment
	Overlaps []overlapSpan
}

func minutesBetween(from, to time.Time) float64 {
	return float64(to.Sub(from)) / float64(time.Minute)
}

// clipDay intersects an event with the JST day starting at day, or returns nil.
func clipDay(event *Event, day time.Time) *segment {
	dayEnd := day.AddDate(0, 0, 1)
	start, end := event.Start, event.End
	seg := &segment{Event: event, Start: start, End: start, From: 0, To: 1440}
	switch {
	case event.AllDay:
		// The end date is exclusive.
		if !end.After(start) || !start.Before(dayEnd) || !end.After(day) {
			return nil
		}
		seg.Kind, seg.End = kindAllDay, end
		seg.Before, seg.After = start.Before(day), end.After(dayEnd)
	case event.Point:
		if start.Before(day) || !start.Before(dayEnd) {
			return nil
		}
		at := wallMinutesBetween(day, start)
		seg.Kind, seg.From, seg.To = kindPoint, at, at
		seg.Early, seg.Late = at < AxisStart, at > AxisEnd
		seg.Axis = at >= AxisStart && at <= AxisEnd
	default:
		if !end.After(start) || !start.Before(dayEnd) || !end.After(day) {
			return nil
		}
		seg.End = end
		seg.From = math.Max(0, wallMinutesBetween(day, start))
		seg.To = math.Min(1440, wallMinutesBetween(day, end))
		seg.Before, seg.After = start.Before(day), end.After(dayEnd)
		seg.Early, seg.Late = seg.From < AxisStart, seg.To > AxisEnd
		seg.Axis = seg.From < AxisEnd && seg.To > AxisStart
	}
	return seg
}

// layoutDay sorts one day's events into all-day, before-axis, after-axis,
// axis blocks and point marks, and assigns columns to overlapping blocks.
func layoutDay(events []Event, day time.Time) dayLayout {
	out := dayLayout{Day: day}
	for i := range events {
		seg := clipDay(&events[i], day)
		if seg == nil {
			continue
		}
		out.All = append(out.All, seg)
		if seg.Kind == kindAllDay {
			out.AllDay = append(out.AllDay, seg)
			continue
		}
		if seg.Early {
			out.Early = append(out.Early, seg)
		}
		if seg.Late {
			out.Late = append(out.Late, seg)
		}
		if seg.Axis {
			if seg.Kind == kindPoint {
				out.Points = append(out.Points, seg)
			} else {
				out.Blocks = append(out.Blocks, seg)
			}
		}
	}
	for _, list := range [][]*segment{out.All, out.AllDay, out.Early, out.Late, out.Blocks, out.Points} {
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if (a.Kind == kindAllDay) != (b.Kind == kindAllDay) {
				return a.Kind == kindAllDay
			}
			if a.From != b.From {
				return a.From < b.From
			}
			if a.To != b.To {
				return a.To > b.To
			}
			return a.Start.Before(b.Start)
		})
	}

	// Blocks that touch form a cluster; every block in a cluster shares the
	// cluster's column count, and a block takes the first column that is free.
	var cluster []*segment
	var ends []float64
	limit := 0.0
	flush := func() {
		for _, seg := range cluster {
			seg.Cols = len(ends)
		}
		cluster, ends = nil, nil
	}
	for _, seg := range out.Blocks {
		seg.Top, seg.Bottom = math.Max(AxisStart, seg.From), math.Min(AxisEnd, seg.To)
		if len(cluster) > 0 && seg.Top >= limit {
			flush()
		}
		col := len(ends)
		for i, end := range ends {
			if end <= seg.Top {
				col = i
				break
			}
		}
		if col == len(ends) {
			ends = append(ends, seg.Bottom)
		} else {
			ends[col] = seg.Bottom
		}
		seg.Col = col
		if len(cluster) > 0 {
			limit = math.Max(limit, seg.Bottom)
		} else {
			limit = seg.Bottom
		}
		cluster = append(cluster, seg)
	}
	flush()

	type mark struct {
		at   float64
		step int
	}
	var marks []mark
	for _, seg := range out.Blocks {
		marks = append(marks, mark{seg.Top, 1}, mark{seg.Bottom, -1})
	}
	sort.Slice(marks, func(i, j int) bool {
		if marks[i].at != marks[j].at {
			return marks[i].at < marks[j].at
		}
		return marks[i].step < marks[j].step
	})
	active := 0
	var open *overlapSpan
	for _, m := range marks {
		active += m.step
		if active >= 2 {
			if open == nil {
				open = &overlapSpan{From: m.at, To: m.at, Count: active}
			} else if active > open.Count {
				open.Count = active
			}
		} else if open != nil {
			open.To = m.at
			if open.To > open.From {
				out.Overlaps = append(out.Overlaps, *open)
			}
			open = nil
		}
	}
	return out
}

// eventKey identifies an event across panels: the same ID in another account
// or calendar is a different event, and each day of an all-day event is too.
func eventKey(e *Event) string {
	return e.Account + "\x00" + e.CalendarID + "\x00" + e.ID + "\x00" + e.DisplayDate
}

// numberEvents gives axis events their reference numbers, in time order
// through today and then tomorrow.
func numberEvents(layouts ...dayLayout) map[string]int {
	numbers := map[string]int{}
	for _, layout := range layouts {
		segs := append(append([]*segment{}, layout.Blocks...), layout.Points...)
		sort.SliceStable(segs, func(i, j int) bool {
			if segs[i].From != segs[j].From {
				return segs[i].From < segs[j].From
			}
			return segs[i].To > segs[j].To
		})
		for _, seg := range segs {
			key := eventKey(seg.Event)
			if _, ok := numbers[key]; !ok {
				numbers[key] = len(numbers) + 1
			}
		}
	}
	return numbers
}

// wallMinutesBetween locates an event on the local clock axis across DST.
func wallMinutesBetween(from, to time.Time) float64 {
	loc := from.Location()
	to = to.In(loc)
	a := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), from.Minute(), from.Second(), from.Nanosecond(), time.UTC)
	b := time.Date(to.Year(), to.Month(), to.Day(), to.Hour(), to.Minute(), to.Second(), to.Nanosecond(), time.UTC)
	return minutesBetween(a, b)
}

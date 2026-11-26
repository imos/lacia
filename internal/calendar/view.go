package calendar

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// View is everything the "calendar" template shows.
type View struct {
	Status    string // ok, partial, pending or unavailable
	ReadError bool   // the latest read failed; the previous snapshot is shown
	Notice    string // warning above the calendar; empty when all is well
	AsOf      time.Time
	UpdatedAt time.Time // last fetch attempt; zero when unknown

	Sources     []SourceView
	Legend      []LegendItem
	SourceNotes []string // lines of the "取得の詳細" fold

	Panels []Panel // today, tomorrow, week: the selected events before layout

	Days      []DayView // today and tomorrow
	BandTicks []Tick    // hour labels of the overview band
	Hours     []Tick    // hour labels of the axis
	// Which optional rows of the two-day grid have content.
	HasNotes, HasAllDay, HasEarly, HasPoints, HasLate bool

	Week    WeekView
	Details []Detail // one entry of the detail pane per selectable event
	Now     NowMarker
}

// SourceView is the freshness of one calendar.
type SourceView struct {
	Account       string
	CalendarID    string
	Label         string
	Color         string
	Status        string
	StatusLabel   string
	ScopeRequired bool
	ScopeVerified bool
	UpdatedAt     time.Time
	AttemptedAt   time.Time
	RangeStart    time.Time
	RangeEnd      time.Time
	// Stale: no success since the latest 06:00 JST, or a timestamp from the future.
	Stale bool
}

type LegendItem struct {
	Text  string
	Style template.CSS
}

// Event is one displayable entry of a panel.
type Event struct {
	ID            string
	SourceEventID string // set on points derived from an all-day event
	Title         string
	Start         time.Time
	End           time.Time
	AllDay        bool
	Private       bool
	Point         bool   // a clock time without duration
	DisplayDate   string // the day an all-day event was expanded to
	SourceStart   time.Time
	SourceEnd     time.Time // the all-day event's own range
	Source        string    // calendar label
	Account       string
	CalendarID    string
	Color         string
	Status        string
	SelfResponse  string
	SourceStatus  string
	Stale         bool
	URL           string
	DisplayBasis  string // "", user_confirmed_day or lacia_interpretation
	Important     *bool
	Recurring     *bool
	Frequent      bool
}

// Panel is the event selection for one period.
type Panel struct {
	Key         string // today, tomorrow or week
	Title       string
	Start       string // first day, YYYY-MM-DD
	End         string // last day, inclusive
	Covered     bool   // the snapshot's range includes the whole period
	Stale       bool
	HiddenCount int // events left out of the week list
	Events      []Event
}

type Tick struct {
	Label string
	Style template.CSS
}

// Note is a line under a panel heading.
type Note struct {
	Text  string
	Empty bool // the "no events" line, styled differently from hints
}

// Chip is an event shown as a text row outside the axis.
type Chip struct {
	EventID string
	For     string // id of the detail radio this chip selects
	Label   string // full spoken description
	Number  int    // axis reference number; shown for points only
	Point   bool
	Time    string
	Title   string
	Source  string
	Style   template.CSS
}

// Block is an event drawn on the axis.
type Block struct {
	EventID   string
	For       string
	Label     string
	Number    int
	Title     string
	Meta      string  // time and calendar; only for blocks of 50 minutes or more
	From, To  float64 // minutes after midnight, clipped to the axis
	TopPct    float64
	HeightPct float64
	LeftPct   float64
	WidthPct  float64
	Col, Cols int
	Short     bool // under 30 minutes: drawn as a bar with a number callout
	Shift     int  // sideways callout offset so neighbours do not collide
	CutTop    bool // continues before the axis
	CutBottom bool // continues after the axis
	Style     template.CSS
}

// PointMark is the line of a point at its exact time.
type PointMark struct {
	EventID string
	Minute  float64
	TopPct  float64
	Style   template.CSS
}

// PointRef labels a group of close points with the first one's number.
type PointRef struct {
	Label  string
	TopPct float64
	Style  template.CSS
}

// Overlap marks a time span where two or more blocks run together.
type Overlap struct {
	From, To  float64
	Count     int
	Text      string
	ShowCount bool
	Style     template.CSS
}

// BandBar is a block in the small two-day overview band.
type BandBar struct {
	EventID string
	Style   template.CSS
}

type DayView struct {
	Key       string
	Title     string
	Short     string // short day label for the overview band
	Date      string // YYYY-MM-DD
	DateLabel string
	IsToday   bool
	Covered   bool
	Stale     bool
	Notes     []Note
	AxisLabel string

	AllDay []Chip
	Early  []Chip // reaching before 08:00
	Late   []Chip // reaching after 20:00
	Points []Chip

	Blocks     []Block
	PointMarks []PointMark
	PointRefs  []PointRef
	Overlaps   []Overlap

	BandBars   []BandBar
	BandPoints []BandBar
	EarlyLabel string
	LateLabel  string
}

type WeekDay struct {
	DateClass   string
	HolidayName string
	Date        string
	Label       string
	Chips       []Chip
}

type WeekView struct {
	Title       string
	Start, End  string
	RangeLabel  string
	PeriodNote  string
	Covered     bool
	Stale       bool
	HiddenCount int
	Notes       []Note
	Days        []WeekDay
}

type Flag struct {
	Text string
	Warn bool
}

// Detail is the full description of one event, shown when it is selected.
type Detail struct {
	ID       string
	EventID  string
	Source   string
	Number   int
	Title    string
	Label    string // full spoken description of the radio option
	URL      string
	Time     string
	Duration string
	Flags    []Flag
	Style    template.CSS
}

// NowMarker is the current-time line as of rendering. calendar.js keeps it
// moving afterwards.
type NowMarker struct {
	Day       string // the day shown as today
	Visible   bool
	Inside    bool // within the axis
	Label     string
	Pct       float64
	LineStyle template.CSS
	BandStyle template.CSS
}

var statusLabels = map[string]string{"ok": "取得済み", "partial": "一部未取得", "error": "取得失敗", "pending": "取得中", "unavailable": "未取得"}

// Build selects and lays out what to show at now.
func Build(snap Snapshot, now time.Time) View {
	return build(snap, now, false)
}

func build(snap Snapshot, now time.Time, readError bool) View {
	return buildIn(snap, now, readError, JST)
}

// BuildIn lays out a calendar in the repository time zone.
func BuildIn(snap Snapshot, now time.Time, loc *time.Location) View {
	return buildIn(snap, now, false, loc)
}

func buildIn(snap Snapshot, now time.Time, readError bool, loc *time.Location) View {
	return buildView(snap, now, readError, loc, false)
}

func buildView(snap Snapshot, now time.Time, readError bool, loc *time.Location, hideHolidays bool) View {
	if loc == nil {
		loc = JST
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	sources, events := observedEvents(snap, now, loc)
	view := View{Status: overallStatus(sources), ReadError: readError, AsOf: now, UpdatedAt: snap.UpdatedAt.In(loc), Sources: sources}
	anyStale := false
	for _, src := range view.Sources {
		anyStale = anyStale || src.Stale
	}

	periods := []struct {
		key, title string
		begin, end time.Time
	}{
		{"today", "きょうの予定", today, today.AddDate(0, 0, 1)},
		{"tomorrow", "あしたの予定", today.AddDate(0, 0, 1), today.AddDate(0, 0, 2)},
		{"week", "きょうから7日間の予定", today, today.AddDate(0, 0, 7)},
	}
	for _, p := range periods {
		panel := Panel{Key: p.key, Title: p.title, Start: ymd(p.begin), End: ymd(p.end.AddDate(0, 0, -1)), Stale: anyStale}
		panel.Covered = snap.Available && !snap.RangeStart.After(p.begin) && !snap.RangeEnd.Before(p.end)
		panel.Events, panel.HiddenCount = selectEvents(events, p.begin, p.end, p.key == "week")
		view.Panels = append(view.Panels, panel)
	}

	b := builder{view: &view, detailIDs: map[string]string{}, hideHolidays: hideHolidays}
	b.layout(today, now)
	b.status()
	return view
}

func observedEvents(snap Snapshot, now time.Time, loc *time.Location) ([]SourceView, []Event) {
	var sources []SourceView
	var events []Event
	today := now.In(loc)
	deadline := time.Date(today.Year(), today.Month(), today.Day(), 6, 0, 0, 0, loc)
	if now.Before(deadline) {
		deadline = deadline.AddDate(0, 0, -1)
	}
	for _, src := range snap.Sources {
		stale := src.UpdatedAt.IsZero() || src.UpdatedAt.Before(deadline) || src.UpdatedAt.After(now.Add(5*time.Minute))
		label := statusLabels[src.Status]
		if label == "" {
			label = "未取得"
		}
		sources = append(sources, SourceView{
			Account: src.Account, CalendarID: src.CalendarID, Label: src.Label, Color: safeColor(src.Color),
			Status: src.Status, StatusLabel: label, ScopeRequired: src.ScopeRequired,
			ScopeVerified: src.ScopeVerified, UpdatedAt: src.UpdatedAt.In(loc), AttemptedAt: src.AttemptedAt.In(loc),
			RangeStart: src.RangeStart.In(loc), RangeEnd: src.RangeEnd.In(loc), Stale: stale,
		})
		for _, e := range src.Events {
			if e.AllDay {
				e.Start = time.Date(e.Start.Year(), e.Start.Month(), e.Start.Day(), 0, 0, 0, 0, loc)
				e.End = time.Date(e.End.Year(), e.End.Month(), e.End.Day(), 0, 0, 0, 0, loc)
			}
			event := Event{
				ID: e.ID, Title: e.Title, Start: e.Start.In(loc), End: e.End.In(loc), AllDay: e.AllDay, Private: e.Private,
				Source: src.Label, Account: src.Account, CalendarID: src.CalendarID, Color: safeColor(src.Color),
				Status: e.Status, SelfResponse: e.SelfResponse, SourceStatus: src.Status, Stale: stale,
				URL: e.URL, DisplayBasis: e.DisplayBasis, Important: e.Important, Recurring: e.Recurring,
				Frequent: e.Frequent,
			}
			events = append(events, event)
			for _, m := range e.Markers {
				point := event
				at := startOfDay(event.Start).Add(time.Duration(m.Minute) * time.Minute)
				point.ID, point.SourceEventID, point.Title = e.ID+":marker:"+m.Time, e.ID, m.Title
				point.Start, point.End, point.AllDay, point.Point = at, at, false, true
				point.DisplayBasis = "lacia_interpretation"
				events = append(events, point)
			}
		}
	}
	return sources, events
}

func overallStatus(sources []SourceView) string {
	if len(sources) == 0 {
		return "unavailable"
	}
	allOK, allPending, some := true, true, false
	for _, s := range sources {
		allOK = allOK && s.Status == "ok"
		allPending = allPending && s.Status == "pending"
		some = some || s.Status == "ok" || s.Status == "partial"
	}
	switch {
	case allOK:
		return "ok"
	case allPending:
		return "pending"
	case some:
		return "partial"
	}
	return "unavailable"
}

// selectEvents picks the events that touch [begin, end), expands all-day
// events to one entry per day, and for the week list drops routine ones.
func selectEvents(events []Event, begin, end time.Time, week bool) ([]Event, int) {
	var out []Event
	hidden := 0
	for _, e := range events {
		start := startOfDay(e.Start)
		if !start.Before(end) || !(e.End.After(begin) || (e.Point && !start.Before(begin))) {
			continue
		}
		if week {
			// An explicit importance wins; otherwise weekly-or-more series are dropped.
			keep := !Hidden(e.Title, true) &&
				((e.Important != nil && *e.Important) || (e.Important == nil && !e.Frequent))
			if !keep {
				hidden++
				continue
			}
		}
		if !e.AllDay {
			out = append(out, e)
			continue
		}
		// The end date of an all-day event is exclusive.
		day, last := startOfDay(e.Start), startOfDay(e.End)
		if day.Before(begin) {
			day = begin
		}
		if last.After(end) {
			last = end
		}
		for ; day.Before(last); day = day.AddDate(0, 0, 1) {
			daily := e
			daily.DisplayDate, daily.SourceStart, daily.SourceEnd = ymd(day), e.Start, e.End
			daily.Start, daily.End = day, day.AddDate(0, 0, 1)
			out = append(out, daily)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		if !a.End.Equal(b.End) {
			return a.End.Before(b.End)
		}
		return a.Title < b.Title
	})
	return out, hidden
}

// builder turns panels into positioned elements and registers detail entries.
type builder struct {
	view         *View
	numbers      map[string]int
	detailIDs    map[string]string
	hideHolidays bool
}

func (b *builder) layout(today, now time.Time) {
	v := b.view
	todayPanel, tomorrowPanel, weekPanel := &v.Panels[0], &v.Panels[1], &v.Panels[2]
	layouts := []dayLayout{
		layoutDay(todayPanel.Events, today),
		layoutDay(tomorrowPanel.Events, today.AddDate(0, 0, 1)),
	}
	b.numbers = numberEvents(layouts...)

	for minute := AxisStart; minute <= AxisEnd; minute += 120 {
		v.BandTicks = append(v.BandTicks, Tick{Label: strconv.Itoa(minute / 60), Style: css("left:%s", at(float64(minute)))})
	}
	for minute := AxisStart; minute <= AxisEnd; minute += 60 {
		v.Hours = append(v.Hours, Tick{Label: fmt.Sprintf("%02d", minute/60), Style: css("top:%s", at(float64(minute)))})
	}
	for i, layout := range layouts {
		panel := &v.Panels[i]
		day := DayView{
			Key: panel.Key, Title: panel.Title, Short: []string{"きょう", "あした"}[i], Date: panel.Start,
			DateLabel: fullDate(layout.Day), IsToday: i == 0, Covered: panel.Covered, Stale: panel.Stale,
			Notes:     b.notes(panel),
			AxisLabel: panel.Title + " " + clockOfMinute(AxisStart) + "〜" + clockOfMinute(AxisEnd),
			AllDay:    b.chips(layout.AllDay), Early: b.chips(layout.Early),
			Late: b.chips(layout.Late), Points: b.chips(layout.Points),
		}
		if n := len(layout.Early); n > 0 {
			day.EarlyLabel = "前" + strconv.Itoa(n)
		}
		if n := len(layout.Late); n > 0 {
			day.LateLabel = "後" + strconv.Itoa(n)
		}
		for _, lap := range layout.Overlaps {
			day.Overlaps = append(day.Overlaps, Overlap{
				From: lap.From, To: lap.To, Count: lap.Count,
				Text:      fmt.Sprintf("%s〜%s に%d件の予定が重なっています。", clockOfMinute(lap.From), clockOfMinute(lap.To), lap.Count),
				ShowCount: lap.To-lap.From >= 20,
				Style:     css("top:%s;height:%s", at(lap.From), pct((lap.To-lap.From)/axisSpan)),
			})
		}
		last, shift := -99.0, 0
		for _, seg := range layout.Blocks {
			e := seg.Event
			minutes := seg.Bottom - seg.Top
			block := Block{
				EventID: e.ID, For: b.detail(e), Label: b.label(e), Number: b.numbers[eventKey(e)],
				Title: titleOf(e), From: seg.Top, To: seg.Bottom,
				TopPct: ratio((seg.Top - AxisStart) / axisSpan), HeightPct: ratio(minutes / axisSpan),
				LeftPct: ratio(float64(seg.Col) / float64(seg.Cols)), WidthPct: ratio(1 / float64(seg.Cols)),
				Col: seg.Col, Cols: seg.Cols, CutTop: seg.From < AxisStart, CutBottom: seg.To > AxisEnd,
			}
			if minutes < 30 {
				// Step the callouts of close short blocks sideways.
				middle := (seg.Top + seg.Bottom) / 2
				if middle-last < 32 {
					shift = (shift + 1) % 3
				} else {
					shift = 0
				}
				last = middle
				block.Short, block.Shift = true, shift
			} else if minutes >= 50 {
				block.Meta = segmentTime(seg) + " · " + sourceOf(e)
			}
			block.Style = css("top:%s%%;height:%s%%;left:%s%%;width:%s%%;--calendar-color:%s;--calendar-shift:%d",
				num(block.TopPct), num(block.HeightPct), num(block.LeftPct), num(block.WidthPct), e.Color, block.Shift)
			day.Blocks = append(day.Blocks, block)
			day.BandBars = append(day.BandBars, BandBar{EventID: e.ID, Style: css(
				"left:%s%%;width:%s%%;top:%s%%;height:%s%%;--calendar-color:%s",
				num(block.TopPct), num(block.HeightPct), num(block.LeftPct), num(block.WidthPct), e.Color)})
		}
		var group []*segment
		flush := func() {
			if len(group) == 0 {
				return
			}
			label := strconv.Itoa(b.numbers[eventKey(group[0].Event)])
			if len(group) > 1 {
				label += "+"
			}
			middle := (group[0].From + group[len(group)-1].From) / 2
			top := ratio((middle - AxisStart) / axisSpan)
			day.PointRefs = append(day.PointRefs, PointRef{Label: label, TopPct: top, Style: css("top:%s%%", num(top))})
			group = nil
		}
		for _, seg := range layout.Points {
			top := ratio((seg.From - AxisStart) / axisSpan)
			day.PointMarks = append(day.PointMarks, PointMark{EventID: seg.Event.ID, Minute: seg.From, TopPct: top,
				Style: css("top:%s%%;--calendar-color:%s", num(top), seg.Event.Color)})
			day.BandPoints = append(day.BandPoints, BandBar{EventID: seg.Event.ID,
				Style: css("left:%s%%;--calendar-color:%s", num(top), seg.Event.Color)})
			// Points closer than 32 minutes share one reference label.
			if len(group) > 0 && seg.From-group[len(group)-1].From >= 32 {
				flush()
			}
			group = append(group, seg)
		}
		flush()
		v.HasNotes = v.HasNotes || len(day.Notes) > 0
		v.HasAllDay = v.HasAllDay || len(day.AllDay) > 0
		v.HasEarly = v.HasEarly || len(day.Early) > 0
		v.HasPoints = v.HasPoints || len(day.Points) > 0
		v.HasLate = v.HasLate || len(day.Late) > 0
		v.Days = append(v.Days, day)
	}

	week := WeekView{
		Title: weekPanel.Title, Start: weekPanel.Start, End: weekPanel.End,
		RangeLabel: weekPanel.Start + " 〜 " + weekPanel.End,
		PeriodNote: "題名が非表示の条件に合う予定と、重要でないと指定した予定は省きます。重要度を指定していない定期予定は、繰り返す間隔が7日以内か、間隔が分からない場合に省きます。",
		Covered:    weekPanel.Covered, Stale: weekPanel.Stale, HiddenCount: weekPanel.HiddenCount,
		Notes: b.notes(weekPanel),
	}
	if weekPanel.HiddenCount > 0 {
		week.PeriodNote += fmt.Sprintf("この欄で省いた予定は%d件です。", weekPanel.HiddenCount)
	}
	for i := 0; i < 7; i++ {
		day := today.AddDate(0, 0, i)
		holiday := ""
		if !b.hideHolidays {
			holiday = holidays[ymd(day)]
		}
		week.Days = append(week.Days, WeekDay{
			DateClass:   dateClass(day, holiday),
			HolidayName: holiday,
			Date:        ymd(day),
			Label:       fmt.Sprintf("%d/%d(%s)", int(day.Month()), day.Day(), weekday(day)),
			Chips:       b.chips(layoutDay(weekPanel.Events, day).All),
		})
	}
	v.Week = week
	v.Now = nowMarker(today, now)
}

func (b *builder) notes(panel *Panel) []Note {
	var notes []Note
	if !panel.Covered {
		notes = append(notes, Note{Text: "この期間には、まだ予定を取得できていない日があります。"})
	}
	if panel.Stale {
		notes = append(notes, Note{Text: "古い情報を含みます。"})
	}
	if len(panel.Events) == 0 {
		text := "予定があるかどうかは、まだ分かりません。"
		if panel.Covered && b.view.Status == "ok" && !panel.Stale && !b.view.ReadError {
			text = "取得できた範囲に、表示する予定はありません。"
			if panel.Key == "week" {
				text = "この欄に表示する予定はありません。"
			}
		}
		notes = append(notes, Note{Text: text, Empty: true})
	}
	return notes
}

func (b *builder) chips(segs []*segment) []Chip {
	var chips []Chip
	for _, seg := range segs {
		e := seg.Event
		chip := Chip{
			EventID: e.ID, For: b.detail(e), Label: b.label(e), Point: seg.Kind == kindPoint,
			Time: segmentTime(seg), Title: titleOf(e), Source: sourceOf(e),
			Style: css("--calendar-color:%s", e.Color),
		}
		if chip.Point {
			chip.Number = b.numbers[eventKey(e)]
		}
		chips = append(chips, chip)
	}
	return chips
}

// detail registers the event's entry in the detail pane and returns its id.
func (b *builder) detail(e *Event) string {
	key := eventKey(e)
	if id, ok := b.detailIDs[key]; ok {
		return id
	}
	id := "calendar-event-" + strconv.Itoa(len(b.detailIDs)+1)
	b.detailIDs[key] = id
	d := Detail{
		ID: id, EventID: e.ID, Source: sourceOf(e), Number: b.numbers[key], Title: titleOf(e), Label: b.label(e),
		URL: SafeLink(e.URL), Time: eventTime(e), Duration: duration(e),
		Style: css("--calendar-color:%s", e.Color),
	}
	for _, text := range flags(e) {
		d.Flags = append(d.Flags, Flag{Text: text, Warn: text == "古い情報" || text == "最新の取得は未完了"})
	}
	b.view.Details = append(b.view.Details, d)
	return id
}

func (b *builder) label(e *Event) string {
	var parts []string
	if n := b.numbers[eventKey(e)]; n > 0 {
		parts = append(parts, strconv.Itoa(n)+"番")
	}
	parts = append(parts, eventTime(e), titleOf(e), sourceOf(e))
	return strings.Join(append(parts, flags(e)...), "、")
}

// status fills the notice, the legend and the fetch details.
func (b *builder) status() {
	v := b.view
	switch {
	case v.ReadError:
		v.Notice = "最新の予定データを読み込めませんでした。"
		if !v.UpdatedAt.IsZero() {
			v.Notice += "前回読み込んだ予定を表示しています。"
		}
	case v.Status == "pending":
		v.Notice = "予定を取得しています。"
	}
	if !v.UpdatedAt.IsZero() {
		v.SourceNotes = append(v.SourceNotes, "最後に予定の取得を試みたのは "+stamp(v.UpdatedAt)+" です。")
	}
	for _, s := range v.Sources {
		text := s.Label + " · " + s.StatusLabel
		if s.Stale {
			text += " · 古い情報"
		}
		v.Legend = append(v.Legend, LegendItem{Text: text, Style: css("--calendar-color:%s", s.Color)})
		last := "最後に予定を取得できた時刻は、まだ分かりません。"
		if !s.UpdatedAt.IsZero() {
			last = "最後に予定を取得できたのは " + stamp(s.UpdatedAt) + " です。"
		}
		if !s.AttemptedAt.IsZero() && !s.AttemptedAt.Equal(s.UpdatedAt) {
			last += "このカレンダーを最後に確認したのは " + stamp(s.AttemptedAt) + " です。"
		}
		if s.Status == "partial" && !s.RangeStart.IsZero() {
			last += "前回取得できた範囲は " + ymd(s.RangeStart) + " から " + ymd(s.RangeEnd.AddDate(0, 0, -1)) + " です。"
		}
		v.SourceNotes = append(v.SourceNotes, fmt.Sprintf("「%s」（%s）の取得状況は「%s」です。%s", s.Label, s.Account, s.StatusLabel, last))
		if s.ScopeRequired && !s.ScopeVerified {
			v.SourceNotes = append(v.SourceNotes, "予定の取得元が「"+s.Label+"」かどうかを確かめられないため、このカレンダーの予定は表示していません。")
		}
	}
	if len(v.Sources) == 0 {
		for _, entry := range Scope {
			v.Legend = append(v.Legend, LegendItem{Text: entry.Label + " · 未取得"})
		}
		v.SourceNotes = append(v.SourceNotes, "予定を取得できたカレンダーはまだありません。")
	}
	v.SourceNotes = append(v.SourceNotes, "色は、どのカレンダーの予定かを表します。重要な予定を選ぶと、詳細に「重要」と表示します。")
}

func nowMarker(today, now time.Time) NowMarker {
	m := NowMarker{Day: ymd(today), Visible: true}
	clock := now.Format("15:04")
	minute := now.Hour()*60 + now.Minute()
	m.Label = "現在 " + clock
	switch {
	case minute < AxisStart:
		m.Label += " · 08:00より前"
	case minute > AxisEnd:
		m.Label += " · 20:00より後"
	default:
		m.Inside = true
		m.Pct = float64(minute-AxisStart) / axisSpan * 100
		m.LineStyle = css("top:%s%%", num(m.Pct))
		m.BandStyle = css("left:%s%%", num(m.Pct))
	}
	return m
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func ymd(t time.Time) string { return t.Format("2006-01-02") }

func clock(t time.Time) string { return t.Format("15:04") }

func clockOfMinute[T int | float64](minute T) string {
	m := int(math.Max(0, math.Floor(float64(minute))))
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

func weekday(t time.Time) string {
	return string([]rune("日月火水木金土")[t.Weekday()])
}

func shortDate(t time.Time) string {
	return fmt.Sprintf("%d/%d", int(t.Month()), t.Day())
}

func fullDate(t time.Time) string {
	return fmt.Sprintf("%d月%d日(%s)", int(t.Month()), t.Day(), weekday(t))
}

func sameDay(a, b time.Time) bool { return ymd(a) == ymd(b) }

func stamp(t time.Time) string {
	return fmt.Sprintf("%d/%d/%d %d:%02d:%02d %s", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), zoneLabel(t))
}

func titleOf(e *Event) string {
	if t := strings.TrimSpace(e.Title); t != "" {
		return t
	}
	return "（無題）"
}

func sourceOf(e *Event) string {
	if s := strings.TrimSpace(e.Source); s != "" {
		return s
	}
	return "カレンダー不明"
}

// segmentTime is the short time shown on a chip, relative to the chip's day.
func segmentTime(seg *segment) string {
	switch seg.Kind {
	case kindAllDay:
		last := seg.End.AddDate(0, 0, -1)
		if last.Before(seg.Start) || sameDay(last, seg.Start) {
			return "終日"
		}
		return "終日 " + shortDate(seg.Start) + "〜" + shortDate(last)
	case kindPoint:
		return clock(seg.Start)
	}
	text := ""
	if seg.Before {
		text += shortDate(seg.Start) + " "
	}
	text += clock(seg.Start) + "〜"
	if seg.After {
		text += shortDate(seg.End) + " "
	}
	return text + clock(seg.End)
}

// eventTime is the full date and time shown in the detail pane.
func eventTime(e *Event) string {
	start, end := e.Start, e.End
	if e.AllDay && !e.SourceStart.IsZero() {
		start, end = e.SourceStart, e.SourceEnd
	}
	switch {
	case e.AllDay:
		last := end.AddDate(0, 0, -1)
		if last.Before(start) || sameDay(last, start) {
			return "終日 " + fullDate(start)
		}
		return "終日 " + fullDate(start) + " 〜 " + fullDate(last)
	case e.Point:
		return fullDate(start) + " " + clock(start)
	}
	text := fullDate(start) + " " + clock(start) + " 〜 "
	if !sameDay(start, end) {
		text += fullDate(end) + " "
	}
	return text + clock(end)
}

func duration(e *Event) string {
	if e.AllDay || e.Point || !e.End.After(e.Start) {
		return ""
	}
	minutes := int(math.Round(minutesBetween(e.Start, e.End)))
	hours, rest := minutes/60, minutes%60
	text := ""
	if hours > 0 {
		text = strconv.Itoa(hours) + "時間"
	}
	if rest > 0 || hours == 0 {
		text += strconv.Itoa(rest) + "分"
	}
	return text
}

func flags(e *Event) []string {
	var out []string
	if e.DisplayBasis == "lacia_interpretation" {
		out = append(out, "わたしの解釈")
	}
	if e.Important != nil && *e.Important {
		out = append(out, "重要")
	}
	if e.Stale {
		out = append(out, "古い情報")
	}
	if e.SourceStatus != "ok" {
		out = append(out, "最新の取得は未完了")
	}
	return out
}

func safeColor(color string) string {
	if colorPattern.MatchString(color) {
		return color
	}
	return defaultColor
}

// ratio converts a fraction to a percentage rounded to two decimals.
func ratio(fraction float64) float64 { return math.Round(fraction*1e4) / 100 }

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func pct(fraction float64) string { return num(ratio(fraction)) + "%" }

func at(minute float64) string { return pct((minute - AxisStart) / axisSpan) }

// css formats a style value. Every argument is a number or a validated colour.
func css(format string, args ...any) template.CSS {
	return template.CSS(fmt.Sprintf(format, args...))
}

func zoneLabel(t time.Time) string { zone, _ := t.Zone(); return zone }

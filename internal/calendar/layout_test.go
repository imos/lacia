package calendar

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const layoutDate = "2026-11-10"

func jst(t *testing.T, value string) time.Time {
	t.Helper()
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, JST); err == nil {
			return parsed
		}
	}
	t.Fatalf("bad time %q", value)
	return time.Time{}
}

// mk is a timed event on layoutDate; an end before the start means the next day.
func mk(t *testing.T, id, start, end string) Event {
	t.Helper()
	e := Event{ID: id, Title: id, CalendarID: "work", Account: "fixture@example.invalid",
		Source: "仕事", SourceStatus: "ok", Color: "#0b8043",
		Start: jst(t, layoutDate+"T"+start), End: jst(t, layoutDate+"T"+end)}
	if e.End.Before(e.Start) {
		e.End = e.End.AddDate(0, 0, 1)
	}
	return e
}

func point(t *testing.T, id, clock string) Event {
	e := mk(t, id, clock, clock)
	e.Point, e.DisplayBasis = true, "lacia_interpretation"
	return e
}

func segIDs(segs []*segment) []string {
	out := []string{}
	for _, seg := range segs {
		out = append(out, seg.Event.ID)
	}
	return out
}

func block(t *testing.T, layout dayLayout, id string) *segment {
	t.Helper()
	for _, seg := range layout.Blocks {
		if seg.Event.ID == id {
			return seg
		}
	}
	t.Fatalf("no block %q", id)
	return nil
}

func geometryEvents(t *testing.T) []Event {
	all := Event{ID: "all", Title: "all", CalendarID: "work", Account: "fixture@example.invalid", Source: "仕事",
		SourceStatus: "ok", AllDay: true, Start: jst(t, "2026-11-09"), End: jst(t, "2026-11-13")}
	return []Event{
		mk(t, "early", "07:15", "07:45"), mk(t, "morning", "09:00", "09:15"), mk(t, "long", "09:15", "11:00"),
		mk(t, "overlap", "10:30", "11:15"), mk(t, "five", "15:00", "15:05"), point(t, "point", "13:30"),
		mk(t, "late", "20:30", "21:15"), mk(t, "cross", "23:30", "00:30"), all,
	}
}

func TestLayoutDayGeometry(t *testing.T) {
	events := geometryEvents(t)
	day := jst(t, layoutDate)
	l := layoutDay(events, day)

	if AxisStart != 480 || AxisEnd != 1200 {
		t.Errorf("axis = %d..%d", AxisStart, AxisEnd)
	}
	if got := segIDs(l.Blocks); !reflect.DeepEqual(got, []string{"morning", "long", "overlap", "five"}) {
		t.Errorf("blocks = %v", got)
	}
	if len(l.Points) != 1 || len(l.AllDay) != 1 {
		t.Errorf("points = %d, all-day = %d", len(l.Points), len(l.AllDay))
	}
	if got := segIDs(l.Early); !reflect.DeepEqual(got, []string{"early"}) {
		t.Errorf("before the axis = %v", got)
	}
	if got := segIDs(l.Late); !reflect.DeepEqual(got, []string{"late", "cross"}) {
		t.Errorf("after the axis = %v", got)
	}
	if five := block(t, l, "five"); five.Bottom-five.Top != 5 || five.Cols != 1 || five.Col != 0 {
		t.Errorf("five-minute block = %+v (a later separate cluster starts again at one column)", five)
	}
	if p := l.Points[0]; p.From != p.To || p.From != 13*60+30 {
		t.Errorf("point = %+v", p)
	}
	if want := []overlapSpan{{From: 630, To: 660, Count: 2}}; !reflect.DeepEqual(l.Overlaps, want) {
		t.Errorf("overlaps = %+v", l.Overlaps)
	}
	if long, over := block(t, l, "long"), block(t, l, "overlap"); long.Cols != 2 || long.Col != 0 || over.Cols != 2 || over.Col != 1 {
		t.Errorf("columns: long=%d/%d overlap=%d/%d", long.Col, long.Cols, over.Col, over.Cols)
	}
	// morning ends exactly when long starts: they touch but do not overlap.
	if morning := block(t, l, "morning"); morning.Cols != 1 {
		t.Errorf("touching blocks share a column: %+v", morning)
	}
	var cross *segment
	for _, seg := range l.Late {
		if seg.Event.ID == "cross" {
			cross = seg
		}
	}
	if !cross.After || cross.To != 1440 {
		t.Errorf("cross on the first day = %+v", cross)
	}

	next := layoutDay(events, day.AddDate(0, 0, 1))
	if got := segIDs(next.Early); !reflect.DeepEqual(got, []string{"cross"}) {
		t.Errorf("next day before the axis = %v", got)
	}
	if c := next.Early[0]; c.From != 0 || c.To != 30 || !c.Before {
		t.Errorf("cross on the next day = %+v", c)
	}
	if len(next.AllDay) != 1 || len(next.All) != 2 {
		t.Errorf("next day: all-day=%d all=%d", len(next.AllDay), len(next.All))
	}
}

func TestClipDayUsesJSTAndExclusiveAllDayEnd(t *testing.T) {
	events := geometryEvents(t)
	all := &events[len(events)-1]
	if clipDay(all, jst(t, "2026-11-12")) == nil {
		t.Error("the last covered day is the 12th")
	}
	if clipDay(all, jst(t, "2026-11-13")) != nil {
		t.Error("the all-day end date is exclusive")
	}
	if clipDay(all, jst(t, "2026-11-08")) != nil {
		t.Error("all-day event shown before it starts")
	}
	utc := Event{ID: "utc", Start: time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC), End: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	if seg := clipDay(&utc, jst(t, layoutDate)); seg == nil || seg.From != 510 || seg.To != 540 {
		t.Errorf("23:30Z is 08:30 JST the next day: %+v", seg)
	}
	if clipDay(&utc, jst(t, "2026-11-09")) != nil {
		t.Error("the UTC date must not decide the day")
	}
	bad := mk(t, "bad", "11:00", "11:00")
	if clipDay(&bad, jst(t, layoutDate)) != nil {
		t.Error("a zero-length timed event is not drawn")
	}
	// The axis edges: a point at 08:00 or 20:00 is on the axis, a block ending at 08:00 is not.
	edgeStart, edgeEnd := point(t, "s", "08:00"), point(t, "e", "20:00")
	before := mk(t, "before", "07:00", "08:00")
	spanning := mk(t, "spanning", "07:30", "20:30")
	l := layoutDay([]Event{edgeStart, edgeEnd, before, spanning}, jst(t, layoutDate))
	if len(l.Points) != 2 || !reflect.DeepEqual(segIDs(l.Blocks), []string{"spanning"}) {
		t.Errorf("points=%v blocks=%v", segIDs(l.Points), segIDs(l.Blocks))
	}
	if !reflect.DeepEqual(segIDs(l.Early), []string{"before", "spanning"}) || !reflect.DeepEqual(segIDs(l.Late), []string{"spanning"}) {
		t.Errorf("early=%v late=%v", segIDs(l.Early), segIDs(l.Late))
	}
	if b := l.Blocks[0]; b.Top != 480 || b.Bottom != 1200 {
		t.Errorf("spanning block is clipped to the axis: %+v", b)
	}
}

func TestLayoutFourSimultaneousCalendarsAndPoints(t *testing.T) {
	var many, points []Event
	for i := 0; i < 4; i++ {
		e := mk(t, "same", "10:00", "11:00")
		e.CalendarID = fmt.Sprintf("source%d", i)
		many = append(many, e)
	}
	four := layoutDay(many, jst(t, layoutDate))
	cols := map[int]bool{}
	for _, seg := range four.Blocks {
		if seg.Cols != 4 {
			t.Errorf("cols = %d", seg.Cols)
		}
		cols[seg.Col] = true
	}
	if len(four.Blocks) != 4 || len(cols) != 4 {
		t.Errorf("blocks=%d distinct columns=%d", len(four.Blocks), len(cols))
	}
	if want := []overlapSpan{{From: 600, To: 660, Count: 4}}; !reflect.DeepEqual(four.Overlaps, want) {
		t.Errorf("overlaps = %+v", four.Overlaps)
	}
	if got := len(numberEvents(four)); got != 4 {
		t.Errorf("the same ID in four calendars gets four numbers, got %d", got)
	}
	for i := 0; i < 8; i++ {
		p := point(t, fmt.Sprintf("p%d", i), "14:00")
		p.CalendarID = fmt.Sprintf("source%d", i)
		points = append(points, p)
	}
	pl := layoutDay(points, jst(t, layoutDate))
	if len(pl.Points) != 8 {
		t.Errorf("points = %d", len(pl.Points))
	}
	for _, p := range pl.Points {
		if p.From != p.To {
			t.Errorf("point with duration: %+v", p)
		}
	}
}

// displaySnapshot is a day with every kind of placement, for the view and the template.
func displaySnapshot() obj {
	timed := func(id, title, start, end string) obj {
		return obj{"id": id, "title": title, "start": obj{"dateTime": start}, "end": obj{"dateTime": end}}
	}
	d := "2026-11-10T"
	return fixture(
		with(timed("same", "打ち合わせ<b>", d+"10:00:00+09:00", d+"11:00:00+09:00"),
			obj{"html_link": "https://www.google.com/calendar/event?eid=fixture&token=private", "important": true, "important_basis": "user_override"}),
		timed("second", "並行の予定", d+"10:30:00+09:00", d+"11:00:00+09:00"),
		timed("short", "5分の確認", d+"15:00:00+09:00", d+"15:05:00+09:00"),
		timed("short2", "続けて確認", d+"15:10:00+09:00", d+"15:20:00+09:00"),
		timed("early", "朝の予定", d+"07:00:00+09:00", d+"07:30:00+09:00"),
		timed("late", "夜の予定", d+"21:00:00+09:00", d+"21:30:00+09:00"),
		timed("night", "夜行", d+"23:30:00+09:00", "2026-11-11T00:30:00+09:00"),
		allDay("multi", "複数日の予定", "2026-11-09", "2026-11-12"),
		with(allDay("school", "学校の予定", "2026-11-10", "2026-11-11"), obj{"display_basis": "user_confirmed_day",
			"display_markers": []any{obj{"time": "13:30", "title": "下校", "basis": "lacia_interpretation"},
				obj{"time": "13:50", "title": "迎え", "basis": "lacia_interpretation"},
				obj{"time": "16:00", "title": "習い事", "basis": "lacia_interpretation"}}}),
		timed("javascript", "危険なリンク", "2026-11-11T09:00:00+09:00", "2026-11-11T10:00:00+09:00"),
	)
}

func TestViewPlacesBlocksChipsAndDetails(t *testing.T) {
	snap := displaySnapshot()
	source(snap, 0)["events"].([]any)[9].(obj)["html_link"] = "javascript:alert(1)"
	v := view(t, snap, "2026-11-10T14:00:00+09:00")
	today, tomorrow := v.Days[0], v.Days[1]

	byID := map[string]Block{}
	for _, b := range today.Blocks {
		byID[b.EventID] = b
	}
	same, second, short, short2 := byID["same"], byID["second"], byID["short"], byID["short2"]
	if same.TopPct != 16.67 || same.HeightPct != 8.33 || same.LeftPct != 0 || same.WidthPct != 50 || same.Cols != 2 {
		t.Errorf("same = %+v", same)
	}
	if second.LeftPct != 50 || second.WidthPct != 50 || second.Col != 1 || second.Meta != "" {
		t.Errorf("second = %+v", second)
	}
	if same.Meta != "10:00〜11:00 · テスト" || same.Number != 1 || second.Number != 2 {
		t.Errorf("meta=%q numbers=%d,%d", same.Meta, same.Number, second.Number)
	}
	if math.Abs(short.HeightPct-5.0/720*100) > 0.005 || !short.Short || short.Shift != 0 || !short2.Short || short2.Shift != 1 {
		t.Errorf("short blocks = %+v %+v", short, short2)
	}
	if want := template.CSS("top:16.67%;height:8.33%;left:0%;width:50%;--calendar-color:#1a73e8;--calendar-shift:0"); same.Style != want {
		t.Errorf("style = %q", same.Style)
	}
	if len(today.Overlaps) != 1 || today.Overlaps[0].Text != "10:30〜11:00 に2件の予定が重なっています。" || !today.Overlaps[0].ShowCount {
		t.Errorf("overlaps = %+v", today.Overlaps)
	}

	chipText := func(chips []Chip) []string {
		out := []string{}
		for _, c := range chips {
			out = append(out, c.Time+" "+c.Title)
		}
		return out
	}
	if got := chipText(today.Early); !reflect.DeepEqual(got, []string{"07:00〜07:30 朝の予定"}) {
		t.Errorf("early = %v", got)
	}
	if got := chipText(today.Late); !reflect.DeepEqual(got, []string{"21:00〜21:30 夜の予定", "23:30〜11/11 00:30 夜行"}) {
		t.Errorf("late = %v", got)
	}
	if got := chipText(tomorrow.Early); !reflect.DeepEqual(got, []string{"11/10 23:30〜00:30 夜行"}) {
		t.Errorf("tomorrow early = %v", got)
	}
	if got := chipText(today.AllDay); !reflect.DeepEqual(got, []string{"終日 学校の予定", "終日 複数日の予定"}) {
		t.Errorf("all-day = %v", got)
	}
	if got := chipText(tomorrow.AllDay); !reflect.DeepEqual(got, []string{"終日 複数日の予定"}) {
		t.Errorf("tomorrow all-day = %v", got)
	}
	if today.EarlyLabel != "前1" || today.LateLabel != "後2" || tomorrow.EarlyLabel != "前1" || tomorrow.LateLabel != "" {
		t.Errorf("band edges = %q %q %q %q", today.EarlyLabel, today.LateLabel, tomorrow.EarlyLabel, tomorrow.LateLabel)
	}
	if !v.HasAllDay || !v.HasEarly || !v.HasPoints || !v.HasLate || v.HasNotes {
		t.Errorf("rows = %v %v %v %v %v", v.HasAllDay, v.HasEarly, v.HasPoints, v.HasLate, v.HasNotes)
	}

	// 13:30 and 13:50 are closer than 32 minutes and share one reference; 16:00 has its own.
	if len(today.PointMarks) != 3 || len(today.PointRefs) != 2 || today.PointRefs[0].Label != "3+" || today.PointRefs[1].Label != "7" {
		t.Errorf("point marks = %+v refs = %+v", today.PointMarks, today.PointRefs)
	}
	if got := today.PointRefs[0].TopPct; got != ratio((13*60+40-480)/720.0) {
		t.Errorf("reference position = %v", got)
	}
	if c := today.Points[0]; c.Number != 3 || c.Time != "13:30" || !c.Point {
		t.Errorf("point chip = %+v", c)
	}

	details := map[string]Detail{}
	for _, d := range v.Details {
		details[d.EventID+"|"+d.Time] = d
	}
	d := details["same|11月10日(火) 10:00 〜 11:00"]
	if d.URL != "https://www.google.com/calendar/event?eid=fixture" || d.Duration != "1時間" || d.Number != 1 ||
		len(d.Flags) != 1 || d.Flags[0].Text != "重要" || d.Flags[0].Warn {
		t.Errorf("detail = %+v", d)
	}
	if d := details["night|11月10日(火) 23:30 〜 11月11日(水) 00:30"]; d.ID == "" || d.Duration != "1時間" {
		t.Errorf("overnight detail missing: %+v", details)
	}
	if d := details["multi|終日 11月9日(月) 〜 11月11日(水)"]; d.ID == "" {
		t.Errorf("multi-day detail missing")
	}
	if d := details["short|11月10日(火) 15:00 〜 15:05"]; d.Duration != "5分" {
		t.Errorf("short detail = %+v", d)
	}
	if d := details["javascript|11月11日(水) 09:00 〜 10:00"]; d.ID == "" || d.URL != "" {
		t.Errorf("unsafe link must be dropped: %+v", d)
	}
	// The overnight event is one selectable event on both days.
	if today.Late[1].For != tomorrow.Early[0].For {
		t.Error("an event crossing midnight must keep one detail entry")
	}
	if got := today.Blocks[0].Label; got != "1番、11月10日(火) 10:00 〜 11:00、打ち合わせ<b>、テスト、重要" {
		t.Errorf("label = %q", got)
	}

	if !v.Now.Visible || !v.Now.Inside || v.Now.Label != "現在 14:00" || v.Now.Pct != 50 || v.Now.Day != "2026-11-10" {
		t.Errorf("now = %+v", v.Now)
	}
}

func TestNowMarkerEdges(t *testing.T) {
	today := jst(t, layoutDate)
	for _, c := range []struct {
		clock  string
		inside bool
		pct    float64
		suffix string
	}{{"07:59", false, 0, " · 08:00より前"}, {"08:00", true, 0, ""}, {"14:00", true, 50, ""},
		{"20:00", true, 100, ""}, {"20:01", false, 0, " · 20:00より後"}} {
		m := nowMarker(today, jst(t, layoutDate+"T"+c.clock))
		if !m.Visible || m.Inside != c.inside || m.Pct != c.pct || m.Label != "現在 "+c.clock+c.suffix {
			t.Errorf("%s: %+v", c.clock, m)
		}
	}
	// The clock is JST whatever zone the server time carries.
	v := Build(Snapshot{}, time.Date(2026, 11, 10, 5, 0, 0, 0, time.UTC))
	if v.Now.Pct != 50 || v.Now.Day != layoutDate {
		t.Errorf("now from UTC = %+v", v.Now)
	}
}

func render(t *testing.T, v View) string {
	t.Helper()
	tmpl, err := AddTemplate(template.New("page"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "calendar", v); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestTemplateRendersTheView(t *testing.T) {
	snap := displaySnapshot()
	// Two meetings with the same title must be distinguishable by ear.
	source(snap, 0)["events"].([]any)[1].(obj)["title"] = "打ち合わせ<b>"
	html := render(t, view(t, snap, "2026-11-10T14:00:00+09:00"))
	for _, want := range []string{
		`data-calendar-day="2026-11-10"`,
		`style="top:16.67%;height:8.33%;left:0%;width:50%;--calendar-color:#1a73e8;--calendar-shift:0"`,
		`<span class="calendar-key" style="--calendar-color:#1a73e8">テスト · 取得済み</span>`,
		`aria-label="1番、11月10日(火) 10:00 〜 11:00、打ち合わせ&lt;b&gt;、テスト、重要"`,
		`aria-label="2番、11月10日(火) 10:30 〜 11:00、打ち合わせ&lt;b&gt;、テスト"`,
		`<a class="calendar-title" href="https://www.google.com/calendar/event?eid=fixture" target="_blank" rel="noopener noreferrer">`,
		`<div class="calendar-rail">8時より前</div>`, `<div class="calendar-rail">20時より後</div>`,
		`<div class="calendar-rail">終日</div>`, `<div class="calendar-rail">時刻</div>`,
		`aria-label="きょうの予定 08:00〜20:00"`, `aria-label="あしたの予定 08:00〜20:00"`,
		`<span class="calendar-now-status" role="timer" aria-live="off">現在 14:00</span>`,
		`<div class="calendar-now-line" aria-hidden="true" style="top:50%"></div>`,
		`<i class="calendar-band-now" style="left:50%"></i>`,
		`<p class="calendar-week-date">11/16(月)</p>`,
		`題名が非表示の条件に合う予定と、重要でないと指定した予定は省きます。重要度を指定していない定期予定は、繰り返す間隔が7日以内か、間隔が分からない場合に省きます。`,
		`<span class="calendar-flag">わたしの解釈</span>`,
		`<p class="calendar-detail-time">11月10日(火) 15:00 〜 15:05（5分）</p>`,
		`予定を選ぶと、その内容を見られます。`,
		`色は、どのカレンダーの予定かを表します。`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, unwanted := range []string{"<b>", "token=private", "ZgotmplZ", "role=\"status\"", "calendar-day-notes"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("unexpected %q in output", unwanted)
		}
	}
	if got := strings.Count(html, `class="calendar-axis"`); got != 2 {
		t.Errorf("axes = %d", got)
	}
	if got := strings.Count(html, `class="calendar-week-day"`); got != 7 {
		t.Errorf("week days = %d", got)
	}
	if got := strings.Count(html, "calendar-now-line"); got != 1 {
		t.Errorf("the current-time line belongs to today only: %d", got)
	}
	// Every chip and block selects an existing detail entry.
	for _, part := range strings.Split(html, ` for="`)[1:] {
		id := part[:strings.Index(part, `"`)]
		if !strings.Contains(html, ` id="`+id+`"`) {
			t.Errorf("label points at missing detail %q", id)
		}
	}

	// Outside the axis hours the line is hidden but the badge says why.
	early := render(t, view(t, displaySnapshot(), "2026-11-10T07:30:00+09:00"))
	if !strings.Contains(early, `<div class="calendar-now-line" aria-hidden="true" style="" hidden></div>`) ||
		!strings.Contains(early, "現在 07:30 · 08:00より前") {
		t.Error("before 08:00 the line is hidden and the badge explains")
	}

	// Without a snapshot the page still renders, with warnings instead of events.
	parsed, _ := Parse(nil)
	empty := render(t, build(parsed, instant(t, "2026-11-10T14:00:00+09:00"), true))
	for _, want := range []string{`<p class="calendar-status" role="status">最新の予定データを読み込めませんでした。</p>`,
		`<span class="calendar-key" style="">会社 · 未取得</span>`, `Family · 未取得`,
		`<p class="calendar-hint">この期間には、まだ予定を取得できていない日があります。</p>`, `<p class="calendar-empty">予定があるかどうかは、まだ分かりません。</p>`,
		`予定を取得できたカレンダーはまだありません。`} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty view missing %s", want)
		}
	}
}

func TestEmbeddedAssets(t *testing.T) {
	if !strings.Contains(CSS, ".calendar-now-line") || !strings.Contains(JS, "data-calendar-day") {
		t.Error("embedded assets are incomplete")
	}
	if strings.Contains(JS, "fetch(") || strings.Contains(JS, "XMLHttpRequest") {
		t.Error("calendar.js must not load data")
	}
}

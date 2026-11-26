package calendar

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"
)

type obj = map[string]any

// fixture is a snapshot with one unscoped test calendar holding events.
func fixture(events ...obj) obj {
	list := []any{}
	for _, e := range events {
		list = append(list, e)
	}
	return obj{"schema_version": 1, "timezone": "Asia/Tokyo",
		"updated_at":  "2026-11-10T06:00:00+09:00",
		"range_start": "2026-11-09", "range_end": "2026-11-17",
		"sources": []any{obj{"calendar_id": "test", "account": "fixture@example.invalid",
			"label": "テスト", "status": "ok", "complete": true,
			"updated_at": "2026-11-10T06:00:00+09:00", "events": list}}}
}

func source(snap obj, i int) obj { return snap["sources"].([]any)[i].(obj) }

func with(base obj, changes obj) obj {
	out := obj{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range changes {
		out[k] = v
	}
	return out
}

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func parseWith(snap any, scope []ScopeEntry) (Snapshot, error) {
	data, err := json.Marshal(snap)
	if err != nil {
		return Snapshot{}, err
	}
	return parse(data, scope)
}

// view builds without a scope at 07:00 on 2026-11-10 unless told otherwise.
func view(t *testing.T, snap any, when ...string) View {
	t.Helper()
	parsed, err := parseWith(snap, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	now := "2026-11-10T07:00:00+09:00"
	if len(when) > 0 {
		now = when[0]
	}
	return Build(parsed, instant(t, now))
}

func ids(events []Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.ID)
	}
	sort.Strings(out)
	return out
}

func sorted(values ...string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func allDay(id, title, start, end string) obj {
	return obj{"id": id, "title": title, "start": obj{"date": start}, "end": obj{"date": end}}
}

func TestRangesEmptyAndUnavailable(t *testing.T) {
	v := view(t, fixture())
	if v.Status != "ok" {
		t.Errorf("status = %q", v.Status)
	}
	for _, p := range v.Panels {
		if !p.Covered || len(p.Events) != 0 {
			t.Errorf("panel %s: covered=%v events=%d", p.Key, p.Covered, len(p.Events))
		}
	}
	if v.Panels[2].Start != "2026-11-10" || v.Panels[2].End != "2026-11-16" {
		t.Errorf("week = %s..%s", v.Panels[2].Start, v.Panels[2].End)
	}
	if got := v.Days[0].Notes; len(got) != 1 || got[0].Text != "取得できた範囲に、表示する予定はありません。" {
		t.Errorf("today notes = %+v", got)
	}
	if got := v.Week.Notes; len(got) != 1 || got[0].Text != "この欄に表示する予定はありません。" {
		t.Errorf("week notes = %+v", got)
	}

	for _, empty := range []any{nil, obj{}} {
		v = view(t, empty)
		if v.Status != "unavailable" {
			t.Errorf("empty status = %q", v.Status)
		}
		for _, p := range v.Panels {
			if p.Covered {
				t.Errorf("panel %s covered without a snapshot", p.Key)
			}
		}
		if len(v.Legend) != 2 || v.Legend[0].Text != "会社 · 未取得" {
			t.Errorf("fallback legend = %+v", v.Legend)
		}
		if got := v.Days[0].Notes; len(got) != 2 || got[0].Text != "この期間には、まだ予定を取得できていない日があります。" || got[1].Text != "予定があるかどうかは、まだ分かりません。" {
			t.Errorf("unavailable notes = %+v", got)
		}
	}
}

func TestOverlapTimezoneAllDayCancelledAndIdentity(t *testing.T) {
	// 2026-11-09T15:30Z is 00:30 JST on the 10th; it runs to 01:00 on the 11th.
	event := obj{"id": "one", "title": "<script>same title</script>",
		"start":     obj{"dateTime": "2026-11-09T15:30:00Z"},
		"end":       obj{"dateTime": "2026-11-10T16:00:00Z"},
		"html_link": "javascript:alert(1)"}
	snap := fixture(event, event, with(event, obj{"id": "two"}),
		allDay("all", "終日", "2026-11-09", "2026-11-11"),
		with(event, obj{"id": "cancel", "status": "cancelled"}))
	v := view(t, snap)
	var counts []int
	for _, p := range v.Panels {
		counts = append(counts, len(p.Events))
	}
	if !reflect.DeepEqual(counts, []int{3, 2, 3}) {
		t.Errorf("event counts = %v, want [3 2 3]", counts)
	}
	if first := v.Panels[0].Events[0]; !first.AllDay || first.ID != "all" {
		t.Errorf("all-day event sorts first, got %+v", first)
	}
	if got := v.Panels[0].Events[1]; got.URL != "" || got.Start.Format(time.RFC3339) != "2026-11-10T00:30:00+09:00" {
		t.Errorf("timed event = %+v", got)
	}
	// The all-day event ends on the 11th exclusive: it is not on tomorrow.
	for _, e := range v.Panels[1].Events {
		if e.AllDay {
			t.Errorf("all-day event leaked past its exclusive end: %+v", e)
		}
	}

	sources := snap["sources"].([]any)
	snap["sources"] = append(sources, with(source(snap, 0), obj{"calendar_id": "other", "events": []any{event}}))
	if got := len(view(t, snap).Panels[0].Events); got != 4 {
		t.Errorf("same ID in another calendar is a separate event: %d events", got)
	}
}

func TestIdentityAndResponseMetadata(t *testing.T) {
	event := obj{"id": "instance", "start": obj{"dateTime": "2026-11-10T08:00:00+09:00"},
		"end": obj{"dateTime": "2026-11-10T09:00:00+09:00"}}
	v := view(t, fixture(event, with(event, obj{"id": "declined", "self_response": "declined"}),
		with(event, obj{"id": "malformed", "self_response": []any{}})))
	if v.Sources[0].CalendarID != "test" {
		t.Errorf("source = %+v", v.Sources[0])
	}
	responses := map[string]string{}
	for _, e := range v.Panels[0].Events {
		if e.CalendarID != "test" || e.Status != "confirmed" || e.Title != "予定" {
			t.Errorf("event = %+v", e)
		}
		responses[e.ID] = e.SelfResponse
	}
	want := map[string]string{"instance": "", "declined": "declined", "malformed": ""}
	if !reflect.DeepEqual(responses, want) {
		t.Errorf("responses = %v", responses)
	}
}

func TestSixAMAndMidnightBoundaries(t *testing.T) {
	snap := fixture()
	if view(t, snap, "2026-11-11T05:59:59+09:00").Panels[0].Stale {
		t.Error("still fresh one second before 06:00")
	}
	stale := view(t, snap, "2026-11-11T06:00:00+09:00")
	if !stale.Panels[0].Stale || !stale.Sources[0].Stale {
		t.Error("stale from 06:00 the next day")
	}
	if got := stale.Legend[0].Text; got != "テスト · 取得済み · 古い情報" {
		t.Errorf("legend = %q", got)
	}
	if got := view(t, snap, "2026-11-11T00:00:00+09:00").Panels[0].Start; got != "2026-11-11" {
		t.Errorf("today at midnight = %s", got)
	}
	// The same instant given in UTC still belongs to the JST day.
	if got := view(t, snap, "2026-11-10T15:00:00Z").Panels[0].Start; got != "2026-11-11" {
		t.Errorf("today from a UTC clock = %s", got)
	}
	sunday := view(t, snap, "2026-11-15T12:00:00+09:00")
	if !sunday.Panels[1].Covered || sunday.Panels[2].Covered {
		t.Errorf("coverage on Sunday: tomorrow=%v week=%v", sunday.Panels[1].Covered, sunday.Panels[2].Covered)
	}
	// A success timestamp more than five minutes in the future is not trusted.
	source(snap, 0)["updated_at"] = "2026-11-10T07:05:01+09:00"
	if !view(t, snap).Sources[0].Stale {
		t.Error("future timestamp must be stale")
	}
	source(snap, 0)["updated_at"] = "2026-11-10T07:05:00+09:00"
	if view(t, snap).Sources[0].Stale {
		t.Error("five minutes of clock skew is allowed")
	}
}

func TestPartialPendingFailedAndMissingSuccess(t *testing.T) {
	snap := fixture()
	source(snap, 0)["complete"] = false
	if got := view(t, snap).Status; got != "partial" {
		t.Errorf("incomplete ok = %q", got)
	}
	source(snap, 0)["status"] = "pending"
	pending := view(t, snap)
	if pending.Status != "pending" || pending.Notice != "予定を取得しています。" {
		t.Errorf("pending = %q, %q", pending.Status, pending.Notice)
	}
	source(snap, 0)["status"] = "error"
	source(snap, 0)["updated_at"] = nil
	failed := view(t, snap)
	if failed.Status != "unavailable" || !failed.Panels[0].Stale {
		t.Errorf("failed = %q stale=%v", failed.Status, failed.Panels[0].Stale)
	}
	if got := failed.Legend[0].Text; got != "テスト · 取得失敗 · 古い情報" {
		t.Errorf("legend = %q", got)
	}
}

func TestEventLinkKeepsOnlyAllowedParts(t *testing.T) {
	event := allDay("link", "仮予定", "2026-11-10", "2026-11-11")
	event["description"], event["location"] = "表示しない", "表示しない"
	event["html_link"] = "https://www.google.com/calendar/event?eid=fixture&token=private"
	v := view(t, fixture(event))
	if got := v.Panels[0].Events[0].URL; got != "https://www.google.com/calendar/event?eid=fixture" {
		t.Errorf("url = %q", got)
	}
	if got := v.Details[0].URL; got != "https://www.google.com/calendar/event?eid=fixture" {
		t.Errorf("detail url = %q", got)
	}

	rejected := []string{
		"javascript:alert(1)",
		"http://www.google.com/calendar/event?eid=x",
		"https://google.com.evil.invalid/calendar/event?eid=x",
		"https://user@www.google.com/calendar/event?eid=x",
		"https://www.google.com:8443/calendar/event?eid=x",
		"https://www.google.com/other?eid=x",
		"https://www.google.com/calendar/event?eid=x#bad",
		"https://www.google.com/calendar/event?ctz=Asia/Tokyo",
		"https://www.google.com/calendar/event?eid=",
		"https://www.google.com/calendar/event/../event?eid=x",
		"",
	}
	for _, link := range rejected {
		if got := SafeLink(link); got != "" {
			t.Errorf("SafeLink(%q) = %q, want none", link, got)
		}
	}
	accepted := map[string]string{
		"https://calendar.google.com/calendar/event?eid=a&ctz=Asia%2FTokyo": "https://calendar.google.com/calendar/event?eid=a&ctz=Asia%2FTokyo",
		"https://WWW.GOOGLE.COM:443/calendar/event?x=1&eid=a":               "https://www.google.com/calendar/event?eid=a",
	}
	for link, want := range accepted {
		if got := SafeLink(link); got != want {
			t.Errorf("SafeLink(%q) = %q, want %q", link, got, want)
		}
	}
}

func TestRollingWeekImportanceAndExplicitRecurrence(t *testing.T) {
	event := allDay("base", "会議", "2026-11-10", "2026-11-11")
	v := view(t, fixture(
		with(event, obj{"id": "known", "is_recurring": true}),
		with(event, obj{"id": "override", "is_recurring": true, "important": true, "important_basis": "user_override"}),
		with(event, obj{"id": "excluded", "is_recurring": false, "important": false, "important_basis": "user_override"}),
		// Without the user's decision behind it, the flag is ignored.
		with(event, obj{"id": "baseless", "is_recurring": false, "important": false}),
		with(event, obj{"id": "unknown"}),
		with(event, obj{"id": "single", "is_recurring": false}),
		with(event, obj{"id": "raw", "recurringEventId": "series"}),
		with(event, obj{"id": "rule", "recurrence": []any{"RRULE:FREQ=WEEKLY"}}),
		with(event, obj{"id": "not_evidence", "recurrence": obj{"unknown": true}}),
		allDay("last", "会議", "2026-11-16", "2026-11-17"),
		allDay("outside", "会議", "2026-11-17", "2026-11-18")))
	if got := len(v.Panels[0].Events); got != 9 {
		t.Errorf("today shows every event: %d", got)
	}
	week := v.Panels[2]
	if got, want := ids(week.Events), sorted("override", "unknown", "single", "last", "not_evidence", "baseless"); !reflect.DeepEqual(got, want) {
		t.Errorf("week = %v, want %v", got, want)
	}
	if week.HiddenCount != 4 {
		t.Errorf("hidden = %d", week.HiddenCount)
	}
	if got := v.Week.PeriodNote; got != "題名が非表示の条件に合う予定と、重要でないと指定した予定は省きます。重要度を指定していない定期予定は、繰り返す間隔が7日以内か、間隔が分からない場合に省きます。この欄で省いた予定は4件です。" {
		t.Errorf("period note = %q", got)
	}
	if len(v.Week.Days) != 7 || v.Week.Days[0].Label != "11/10(火)" || v.Week.Days[6].Label != "11/16(月)" {
		t.Errorf("week days = %+v", v.Week.Days)
	}
	if got := len(v.Week.Days[0].Chips); got != 5 {
		t.Errorf("first day chips = %d", got)
	}
	if got := v.Week.Days[6].Chips; len(got) != 1 || got[0].EventID != "last" || got[0].Time != "終日" {
		t.Errorf("last day chips = %+v", got)
	}
}

func TestTitleExclusionsAndTypographicVariants(t *testing.T) {
	// The patterns come from the record repository's settings.
	const boundary = `(?:$|[^\p{L}\p{N}_])`
	if err := SetHidden(
		[]string{`^lunch break\s*\(\s*booking block\s*\)$`, `^no regular meeting day$`, `読書会`},
		[]string{`^ooo` + boundary, `^sync\s*:?\s*x[- ]san` + boundary, `^all[-–— ]?hands(?:\s+meeting)?\s*(?:$|[(@:])`,
			`^ext\s*[-–—]?\s*xさん(?:\s|$)`, `^team biweekly` + boundary},
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { SetHidden(nil, nil) })
	global := []string{"Lunch break（Booking Block)", "lunch break (Booking Block)", "No regular meeting day",
		"読書会", "読書会 (お気軽にご参加ください！）＠Google meet", "技術書の読書会"}
	weekOnly := []string{"OoO (用事)", "Sync: X-san", "Sync X san",
		"ALL HANDS Meeting (Weekly)", "All-Hands @Zoom", "ext - Xさん", "Team biweekly", "ext Xさん"}
	keep := []string{"Lunch with colleagues", "Sync: someone else", "Other All hands planning",
		"All Hands planning", "Monthly review"}
	var events []obj
	for i, title := range append(append(append([]string{}, global...), weekOnly...), keep...) {
		e := allDay(string(rune('a'+i)), title, "2026-11-10", "2026-11-11")
		e["important"] = true
		e["important_basis"] = "user_override"
		events = append(events, e)
	}
	v := view(t, fixture(events...))
	titles := func(list []Event) []string {
		var out []string
		for _, e := range list {
			out = append(out, e.Title)
		}
		sort.Strings(out)
		return out
	}
	if got, want := titles(v.Panels[0].Events), sorted(append(append([]string{}, weekOnly...), keep...)...); !reflect.DeepEqual(got, want) {
		t.Errorf("today = %v, want %v", got, want)
	}
	if got, want := titles(v.Panels[2].Events), sorted(keep...); !reflect.DeepEqual(got, want) {
		t.Errorf("week = %v, want %v", got, want)
	}
	if v.Panels[2].HiddenCount != len(weekOnly) {
		t.Errorf("hidden = %d", v.Panels[2].HiddenCount)
	}
	// A word boundary is required: Japanese letters right after the name do not match.
	for title, want := range map[string]bool{"OOO": true, "ooo休暇": false, "OOOps": false, "Team biweekly2": false, "ＯＯＯ　（半休）": true} {
		if got := Hidden(title, true); got != want {
			t.Errorf("Hidden(%q, weekly) = %v, want %v", title, got, want)
		}
	}
}

func TestRecurrenceFrequencyAndImportance(t *testing.T) {
	base := with(allDay("", "予定", "2026-11-10", "2026-11-11"), obj{"is_recurring": true})
	var events []obj
	for _, c := range [][2]string{{"weekly", "RRULE:FREQ=WEEKLY"}, {"daily", "RRULE:FREQ=DAILY"},
		{"biweekly", "RRULE:FREQ=WEEKLY;INTERVAL=2"}, {"monthly", "RRULE:FREQ=MONTHLY"},
		{"yearly", "RRULE:FREQ=YEARLY"}, {"eight_days", "RRULE:FREQ=DAILY;INTERVAL=8"}} {
		events = append(events, with(base, obj{"id": c[0], "recurrence": []any{c[1]}}))
	}
	events = append(events, with(base, obj{"id": "important", "important": true, "important_basis": "user_override"}), with(base, obj{"id": "unknown"}))
	v := view(t, fixture(events...))
	if got, want := ids(v.Panels[2].Events), sorted("biweekly", "monthly", "yearly", "eight_days", "important"); !reflect.DeepEqual(got, want) {
		t.Errorf("week = %v, want %v", got, want)
	}
	if got := len(v.Panels[0].Events); got != 8 {
		t.Errorf("today = %d", got)
	}
}

func TestConfirmedDayExpansionAndInterpretedPoint(t *testing.T) {
	school := with(allDay("school", "学校の予定", "2026-11-10", "2026-11-11"), obj{
		"display_basis":   "user_confirmed_day",
		"display_markers": []any{obj{"time": "13:30", "title": "下校", "basis": "lacia_interpretation"}}})
	trip := with(allDay("trip", "旅行", "2026-11-11", "2026-11-14"), obj{"display_basis": "user_confirmed_day"})
	snap := fixture(school, school, trip)
	v := view(t, snap)
	today := v.Panels[0].Events
	if len(today) != 2 {
		t.Fatalf("duplicates are removed before points are derived: %d events", len(today))
	}
	marker := today[1]
	if !marker.Point || marker.Start.Format(time.RFC3339) != "2026-11-10T13:30:00+09:00" || !marker.Start.Equal(marker.End) ||
		marker.SourceEventID != "school" || marker.DisplayBasis != "lacia_interpretation" || marker.Title != "下校" {
		t.Errorf("marker = %+v", marker)
	}
	var days []string
	for _, e := range v.Panels[2].Events {
		if e.ID == "trip" {
			days = append(days, e.DisplayDate)
			if e.SourceEnd.Format(time.RFC3339) != "2026-11-14T00:00:00+09:00" {
				t.Errorf("source end = %s", e.SourceEnd)
			}
		}
	}
	if want := []string{"2026-11-11", "2026-11-12", "2026-11-13"}; !reflect.DeepEqual(days, want) {
		t.Errorf("trip days = %v, want %v (the end date is exclusive)", days, want)
	}
	if got := v.Panels[1].Events[0].DisplayDate; got != "2026-11-11" {
		t.Errorf("tomorrow = %q", got)
	}

	// The point sits on the axis at its exact time and has no duration.
	day := v.Days[0]
	if len(day.PointMarks) != 1 || day.PointMarks[0].Minute != 13*60+30 || len(day.Blocks) != 0 {
		t.Fatalf("point marks = %+v blocks = %+v", day.PointMarks, day.Blocks)
	}
	if chip := day.Points[0]; chip.Time != "13:30" || chip.Number != 1 || chip.Title != "下校" {
		t.Errorf("point chip = %+v", chip)
	}
	var detail Detail
	for _, d := range v.Details {
		if d.EventID == "school:marker:13:30" {
			detail = d
		}
	}
	if detail.Time != "11月10日(火) 13:30" || detail.Duration != "" || len(detail.Flags) != 1 || detail.Flags[0].Text != "わたしの解釈" {
		t.Errorf("point detail = %+v", detail)
	}
	for _, d := range v.Details {
		if d.EventID == "trip" && d.Time != "終日 11月11日(水) 〜 11月13日(金)" {
			t.Errorf("trip detail time = %q", d.Time)
		}
	}

	marker0 := school["display_markers"].([]any)[0].(obj)
	for _, bad := range []any{"24:00", "13:99", 1330} {
		marker0["time"] = bad
		if _, err := parseWith(snap, nil); err == nil {
			t.Errorf("marker time %v must be rejected", bad)
		}
	}
}

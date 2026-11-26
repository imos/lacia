package notify

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

const testSite = "https://lacia.example"

var testNow = time.Date(2026, 11, 10, 9, 0, 0, 0, time.FixedZone("JST", 9*3600))

// The expected values below were printed once by the earlier implementation
// in Python, which is gone. Notices already scheduled on macOS are told apart
// by these identifiers, so they must not change.
func TestIdentityMatchesPython(t *testing.T) {
	for _, c := range []struct {
		parts []string
		want  string
	}{
		{[]string{"owner@example.com", "primary"}, "c768df436d85e70515bbfcc755ca4595b0d6ee92151e3e5e5131216c3467b7f3"},
		{[]string{"family@example.com", "example-shared@group.calendar.google.com"}, "7c9c487ff8662b21ac5ead42a01f51edffc380b85c66d2a2d4f11d5850f0c4cb"},
		{[]string{"fixture"}, "b074a6afd500bbf0891340a1cdda223c1516d7bb52518b820848e1d29d12788a"},
		{[]string{`週次"定例" 会議`}, "37ed7c3da1d232692b0dca6c12e593455626bd0233abfa7b2f8ba59982d88f3f"},
		{[]string{"a\\b\n\t\x01\x7f/<>&😀"}, "b46578ba1d4f237d26c6e4da420cf6608d95e345d96cede004723943d5b73a20"},
	} {
		if got := identity(c.parts...); got != c.want {
			t.Errorf("identity(%q) = %s, want %s", c.parts, got, c.want)
		}
	}
}

func TestMeetingNoticesMatchPython(t *testing.T) {
	const company = "meeting-c768df436d85e70515bbfcc755ca4595b0d6ee92151e3e5e5131216c3467b7f3-"
	const family = "meeting-7c9c487ff8662b21ac5ead42a01f51edffc380b85c66d2a2d4f11d5850f0c4cb-"
	events := []any{
		linkedEvent(nil),
		linkedEvent(map[string]any{
			"id": `週次"定例" 会議`, "title": `週次 "定例"`,
			"start": map[string]any{"dateTime": "2026-11-10T04:30:00.250Z"},
			"end":   map[string]any{"dateTime": "2026-11-10T05:00:00Z"},
		}),
	}
	snapshot := linkedSnapshot(events, nil)
	snapshot["sources"].([]any)[1].(map[string]any)["events"] = events[:1]
	got, complete := planLinked(t, snapshot, Config{}, &State{}, testNow)
	want := []Notice{
		{ID: company + "b074a6afd500bbf0891340a1cdda223c1516d7bb52518b820848e1d29d12788a-1794272400000", Kind: "meeting",
			Title: "会議が1分後に始まります", Body: "10:00 No regular meeting day", URL: testSite + "/", Fire: 1794272340, Expires: 1794272400},
		{ID: company + "37ed7c3da1d232692b0dca6c12e593455626bd0233abfa7b2f8ba59982d88f3f-1794285000250", Kind: "meeting",
			Title: "会議が1分後に始まります", Body: `13:30 週次 "定例"`, URL: testSite + "/", Fire: 1794284940.25, Expires: 1794285000.25},
		{ID: family + "b074a6afd500bbf0891340a1cdda223c1516d7bb52518b820848e1d29d12788a-1794272400000", Kind: "meeting",
			Title: "会議が1分後に始まります", Body: "10:00 No regular meeting day", URL: testSite + "/", Fire: 1794272340, Expires: 1794272400},
	}
	if !complete || !reflect.DeepEqual(got, want) {
		t.Errorf("complete = %v, notices = %+v\nwant %+v", complete, got, want)
	}
}

func TestMeetingNoticesUseEventLinks(t *testing.T) {
	const work = "https://www.google.com/calendar/event?eid=instance"
	const family = "https://calendar.google.com/calendar/event?eid=family&ctz=Asia%2FTokyo"
	state := &State{}
	snapshot := linkedSnapshot([]any{
		linkedEvent(map[string]any{"html_link": work + "&token=discarded"}),
		linkedEvent(map[string]any{"id": "unsafe", "html_link": "https://meet.google.com/fixture"}),
		linkedEvent(map[string]any{"id": "legacy"}),
	}, nil)
	snapshot["sources"].([]any)[1].(map[string]any)["events"] = []any{
		linkedEvent(map[string]any{"htmlLink": family}),
	}
	got, complete := planLinked(t, snapshot, Config{}, state, testNow)
	want := []string{work, testSite + "/", testSite + "/", family}
	if !complete || len(got) != len(want) {
		t.Fatalf("complete = %v, notices = %+v", complete, got)
	}
	for i, url := range want {
		if got[i].URL != url {
			t.Errorf("notice %d: URL = %q, want %q", i, got[i].URL, url)
		}
	}
}

func TestMeetingGetsEventLinkAtDeliveryWithoutChangingID(t *testing.T) {
	state := &State{}
	event := linkedEvent(nil)
	snapshot := linkedSnapshot([]any{event}, nil)
	planned, _ := planLinked(t, snapshot, Config{}, state, testNow)
	if len(planned) != 1 || planned[0].URL != testSite+"/" {
		t.Fatalf("prior plan = %+v", planned)
	}
	want := planned[0]
	want.URL = "https://www.google.com/calendar/event?eid=late-link"
	event["html_link"] = want.URL
	now := testNow.Add(59 * time.Minute)
	for _, raw := range snapshot["sources"].([]any) {
		raw.(map[string]any)["updated_at"] = now.Format(time.RFC3339)
	}
	if got, complete := planLinked(t, snapshot, Config{}, state, now); !complete || !reflect.DeepEqual(got, []Notice{want}) {
		t.Errorf("complete = %v, notices = %+v, want %+v", complete, got, want)
	}
}

// TestGoldenScenarios replays polls recorded from the earlier planner in
// Python: random snapshots, including malformed ones, fed to one state across
// several polls. LACIA_NOTIFY_GOLDEN selects a larger file.
func TestGoldenScenarios(t *testing.T) {
	path := os.Getenv("LACIA_NOTIFY_GOLDEN")
	if path == "" {
		path = "testdata/meetings_golden.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Base       string
		Identities [][2]json.RawMessage
		Scenarios  [][]struct {
			Now      float64
			Snapshot string
			Config   json.RawMessage
			Error    bool
			Complete bool
			Notices  []Notice
			State    []string
		}
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	for _, pair := range golden.Identities {
		var parts []string
		var want string
		if json.Unmarshal(pair[0], &parts) != nil || json.Unmarshal(pair[1], &want) != nil {
			t.Fatalf("bad identity entry %s", pair)
		}
		if got := identity(parts...); got != want {
			t.Errorf("identity(%q) = %s, want %s", parts, got, want)
		}
	}
	steps := 0
	for number, scenario := range golden.Scenarios {
		state := &State{}
		for index, step := range scenario {
			steps++
			config, err := ParseConfig(step.Config)
			if err != nil {
				t.Fatal(err)
			}
			now := time.UnixMicro(int64(step.Now * 1e6))
			notices, complete, err := PlanMeetings([]byte(step.Snapshot), config, state, now, golden.Base)
			ids := []string{}
			for _, n := range state.Meetings {
				ids = append(ids, n.ID)
			}
			if !reflect.DeepEqual(ids, step.State) {
				t.Fatalf("scenario %d step %d: state = %q, want %q\nsnapshot: %s", number, index, ids, step.State, step.Snapshot)
			}
			if (err != nil) != step.Error {
				t.Fatalf("scenario %d step %d: err = %v, want error %v\nsnapshot: %s\nconfig: %s", number, index, err, step.Error, step.Snapshot, step.Config)
			}
			if err != nil {
				continue
			}
			if step.Notices == nil {
				step.Notices = []Notice{}
			}
			if complete != step.Complete || !reflect.DeepEqual(notices, step.Notices) {
				t.Fatalf("scenario %d step %d: complete = %v, notices = %+v\nwant %v, %+v\nsnapshot: %s\nconfig: %s",
					number, index, complete, notices, step.Complete, step.Notices, step.Snapshot, step.Config)
			}
		}
	}
	if steps == 0 || len(golden.Identities) == 0 {
		t.Fatal("golden file is empty")
	}
}

func linkedEvent(changes map[string]any) map[string]any {
	event := map[string]any{
		"id": "fixture", "title": "No regular meeting day", "meeting_candidate": true,
		"start": map[string]any{"dateTime": "2026-11-10T10:00:00+09:00"},
		"end":   map[string]any{"dateTime": "2026-11-10T11:00:00+09:00"},
	}
	for key, value := range changes {
		event[key] = value
	}
	return event
}

// linkedSnapshot builds a snapshot of both calendars; changes apply to the
// company calendar, which also holds the events.
func linkedSnapshot(events []any, changes map[string]any) map[string]any {
	company := map[string]any{
		"account": "owner@example.com", "calendar_id": "primary", "scope_verified": true,
		"status": "ok", "complete": true, "updated_at": "2026-11-10T09:00:00+09:00", "events": events,
	}
	for key, value := range changes {
		company[key] = value
	}
	family := map[string]any{
		"account": "family@example.com", "calendar_id": "example-shared@group.calendar.google.com", "scope_verified": true,
		"status": "ok", "complete": true, "updated_at": "2026-11-10T09:00:00+09:00", "events": []any{},
	}
	return map[string]any{"schema_version": 1, "timezone": "Asia/Tokyo", "sources": []any{company, family}}
}

func planLinked(t *testing.T, snapshot map[string]any, config Config, state *State, now time.Time) ([]Notice, bool) {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	notices, complete, err := PlanMeetings(raw, config, state, now, testSite)
	if err != nil {
		t.Fatal(err)
	}
	return notices, complete
}

func TestMeetingRemovalNeedsCoverageOrRevocation(t *testing.T) {
	state := &State{}
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	events := []any{linkedEvent(nil)}
	notices, _ := planLinked(t, linkedSnapshot(events, nil), Config{}, state, now)
	if len(notices) != 1 {
		t.Fatalf("planned notices = %v", notices)
	}
	missing := linkedSnapshot([]any{}, map[string]any{"range_start": "2026-11-11", "range_end": "2026-11-18"})
	if notices, _ := planLinked(t, missing, Config{}, state, now); len(notices) != 1 {
		t.Fatalf("out-of-range meeting removed: %v", notices)
	}
	revoked := linkedSnapshot([]any{}, map[string]any{"status": "unavailable", "complete": false, "access_revoked": true})
	if notices, complete := planLinked(t, revoked, Config{}, state, now); len(notices) != 0 || complete {
		t.Fatalf("revoked calendar: notices = %v, complete = %v", notices, complete)
	}
}

func rules(t *testing.T, raw string) Config {
	t.Helper()
	config, err := ParseConfig([]byte(`{"exclude_meetings":` + raw + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestReadingGroupRuleRemovesExistingNoticeAndKeepsOtherMeetings(t *testing.T) {
	state := &State{}
	snapshot := linkedSnapshot([]any{
		linkedEvent(map[string]any{"id": "reading", "title": "読書会 (お気軽にご参加ください！）＠Google meet"}),
		linkedEvent(map[string]any{"id": "regular", "title": "通常の会議"}),
	}, nil)
	if notices, _ := planLinked(t, snapshot, Config{}, state, testNow); len(notices) != 2 {
		t.Fatalf("before exclusion: got %d notices", len(notices))
	}
	config := rules(t, `[{"title_contains":"読書会"}]`)
	for i := 0; i < 2; i++ {
		notices, complete := planLinked(t, snapshot, config, state, testNow)
		if !complete || len(notices) != 1 || notices[0].Body != "10:00 通常の会議" {
			t.Fatalf("poll %d: complete=%v notices=%+v", i, complete, notices)
		}
	}
}

func TestLinkedMeetingOnlyIndependentOfDisplayAndDedup(t *testing.T) {
	state := &State{}
	event := linkedEvent(nil)
	notices, complete := planLinked(t, linkedSnapshot([]any{event, event}, nil), Config{}, state, testNow)
	if !complete || len(notices) != 1 {
		t.Fatalf("complete = %v, notices = %+v; a title hidden on the Web page must still notify once", complete, notices)
	}
	if notices[0].Fire != seconds(testNow)+3540 || notices[0].Expires != seconds(testNow)+3600 {
		t.Errorf("fire = %v, expires = %v", notices[0].Fire, notices[0].Expires)
	}
	same, _ := planLinked(t, linkedSnapshot([]any{event}, nil), Config{}, state, testNow)
	if len(same) != 1 || same[0].ID != notices[0].ID {
		t.Errorf("second poll = %+v", same)
	}
	for _, changes := range []map[string]any{
		{"meeting_candidate": nil}, {"meeting_candidate": false}, {"self_response": "declined"},
		{"status": "cancelled"}, {"display_basis": "lacia_interpretation"},
		{"start": map[string]any{"date": "2026-11-10"}},
	} {
		got, _ := planLinked(t, linkedSnapshot([]any{linkedEvent(changes)}, nil), Config{}, &State{}, testNow)
		if len(got) != 0 {
			t.Errorf("%v: notices = %+v", changes, got)
		}
	}
}

func TestLinkedChangesCancelPartialFailureScopeAndFreshness(t *testing.T) {
	state := &State{}
	planLinked(t, linkedSnapshot([]any{linkedEvent(nil)}, nil), Config{}, state, testNow)
	for _, changes := range []map[string]any{
		{"complete": false, "status": "partial"},
		{"status": "error"},
		{"updated_at": "2026-11-10T08:00:00+09:00"},
	} {
		notices, complete := planLinked(t, linkedSnapshot([]any{}, changes), Config{}, state, testNow)
		if complete || len(notices) != 1 {
			t.Errorf("%v: complete = %v, notices = %+v", changes, complete, notices)
		}
	}
	count := func(events []any, changes map[string]any) int {
		t.Helper()
		notices, _ := planLinked(t, linkedSnapshot(events, changes), Config{}, state, testNow)
		return len(notices)
	}
	partial := map[string]any{"complete": false, "status": "partial"}
	if count([]any{}, map[string]any{"scope_verified": false}) != 1 {
		t.Error("an unverified scope must not cancel")
	}
	unknown := linkedEvent(nil)
	delete(unknown, "meeting_candidate")
	if count([]any{unknown}, partial) != 1 || count([]any{unknown}, nil) != 1 {
		t.Error("unknown meeting metadata must not cancel")
	}
	if _, complete := planLinked(t, linkedSnapshot([]any{unknown}, nil), Config{}, state, testNow); complete {
		t.Error("unknown meeting metadata must make the result incomplete")
	}
	if count([]any{linkedEvent(map[string]any{"status": "cancelled"})}, partial) != 0 {
		t.Error("an explicit cancellation must cancel even in a partial source")
	}
	planLinked(t, linkedSnapshot([]any{linkedEvent(nil)}, nil), Config{}, state, testNow)
	moved := linkedEvent(map[string]any{
		"start": map[string]any{"dateTime": "2026-11-10T12:00:00+09:00"},
		"end":   map[string]any{"dateTime": "2026-11-10T13:00:00+09:00"},
	})
	notices, _ := planLinked(t, linkedSnapshot([]any{moved}, partial), Config{}, state, testNow)
	if len(notices) != 1 || notices[0].Fire != seconds(testNow)+10740 {
		t.Errorf("moved = %+v", notices)
	}
	if count([]any{}, nil) != 0 {
		t.Error("a complete source without the event must cancel")
	}
}

func TestLinkedKeepsSubmittedOccurrenceUntilStartWithoutLateCreation(t *testing.T) {
	start := testNow.Add(time.Hour)
	snapshot := linkedSnapshot([]any{linkedEvent(nil)}, map[string]any{"updated_at": "2026-11-10T09:58:00+09:00"})
	snapshot["sources"].([]any)[1].(map[string]any)["updated_at"] = "2026-11-10T09:58:00+09:00"
	state := &State{}
	planned, _ := planLinked(t, snapshot, Config{}, state, start.Add(-120*time.Second))
	if len(planned) != 1 {
		t.Fatalf("planned = %+v", planned)
	}
	for _, before := range []int{60, 45, 1} {
		now := start.Add(-time.Duration(before) * time.Second)
		if got, _ := planLinked(t, snapshot, Config{}, state, now); !reflect.DeepEqual(got, planned) {
			t.Errorf("%ds before: %+v", before, got)
		}
		if got, _ := planLinked(t, snapshot, Config{}, &State{}, now); len(got) != 0 {
			t.Errorf("%ds before: late notice %+v", before, got)
		}
	}
	if got, _ := planLinked(t, snapshot, Config{}, state, start); len(got) != 0 {
		t.Errorf("at start: %+v", got)
	}
}

func TestLinkedDeliveryWindowStillObeysCancelDeclineAndExclusion(t *testing.T) {
	start := testNow.Add(time.Hour)
	fresh := map[string]any{"updated_at": "2026-11-10T09:58:00+09:00"}
	for _, c := range []struct {
		change map[string]any
		config Config
	}{
		{map[string]any{"status": "cancelled"}, Config{}},
		{map[string]any{"self_response": "declined"}, Config{}},
		{map[string]any{"meeting_candidate": false}, Config{}},
		{nil, rules(t, `[{"event_id":"fixture"}]`)},
	} {
		state := &State{}
		planLinked(t, linkedSnapshot([]any{linkedEvent(nil)}, fresh), Config{}, state, start.Add(-120*time.Second))
		got, _ := planLinked(t, linkedSnapshot([]any{linkedEvent(c.change)}, fresh), c.config, state, start.Add(-30*time.Second))
		if len(got) != 0 {
			t.Errorf("%v: %+v", c.change, got)
		}
	}
}

func TestLiteralExclusionAndInvalidRules(t *testing.T) {
	plan := func(config Config) ([]Notice, error) {
		raw, _ := json.Marshal(linkedSnapshot([]any{linkedEvent(map[string]any{"title": "Fixture meeting"})}, nil))
		notices, _, err := PlanMeetings(raw, config, &State{}, testNow, testSite)
		return notices, err
	}
	for raw, want := range map[string]int{
		`[{"title_contains":"Fixture"}]`:                               0,
		`[{"title_contains":".*"}]`:                                    1,
		`[{"calendar_id":"primary","account":"owner@example.com"}]`:    0,
		`[{"calendar_id":"primary","account":"fixture@example.test"}]`: 1,
		`[{"event_id":"other"},{"event_id":"fixture"}]`:                0,
	} {
		if got, err := plan(rules(t, raw)); err != nil || len(got) != want {
			t.Errorf("%s: notices = %+v, err = %v", raw, got, err)
		}
	}
	for _, raw := range []string{`[{"regex":".*"}]`, `[{}]`, `[{"account":""}]`, `[{"event_id":3}]`, `["x"]`, `null`, `{}`} {
		if _, err := plan(rules(t, raw)); err == nil {
			t.Errorf("%s: no error", raw)
		}
	}
}

func TestPrivateTitleHorizonAndBadSnapshots(t *testing.T) {
	private := linkedEvent(map[string]any{"visibility": "private"})
	untitled := linkedEvent(map[string]any{"id": "untitled", "title": ""})
	far := linkedEvent(map[string]any{
		"id":    "far",
		"start": map[string]any{"dateTime": "2026-11-11T09:01:01+09:00"},
		"end":   map[string]any{"dateTime": "2026-11-11T10:00:00+09:00"},
	})
	edge := linkedEvent(map[string]any{
		"id":    "edge",
		"start": map[string]any{"dateTime": "2026-11-11T09:01:00+09:00"},
		"end":   map[string]any{"dateTime": "2026-11-11T10:00:00+09:00"},
	})
	got, _ := planLinked(t, linkedSnapshot([]any{private, untitled, far, edge}, nil), Config{}, &State{}, testNow)
	if len(got) != 3 || got[0].Body != "10:00 非公開の予定" || got[1].Body != "10:00 会議" || got[2].Body != "09:01 No regular meeting day" || got[2].Fire != seconds(testNow)+86400 {
		t.Errorf("notices = %+v", got)
	}

	state := &State{}
	planLinked(t, linkedSnapshot([]any{linkedEvent(nil)}, nil), Config{}, state, testNow)
	for _, raw := range []string{
		``, `[]`, `{"schema_version":2,"timezone":"Asia/Tokyo","sources":[]}`,
		`{"schema_version":1,"timezone":"Invalid/Timezone","sources":[]}`, `{"schema_version":1,"timezone":"Asia/Tokyo"}`,
		"{\"schema_version\":1,\"timezone\":\"Asia/Tokyo\",\"sources\":[],\"x\":\"\xff\"}",
	} {
		if _, _, err := PlanMeetings([]byte(raw), Config{}, state, testNow, testSite); err == nil {
			t.Errorf("%q: no error", raw)
		}
		if len(state.Meetings) != 1 {
			t.Fatalf("%q: an unreadable snapshot changed the state", raw)
		}
	}
	// A naive start time is an error and leaves the earlier plan in place.
	naive := linkedEvent(map[string]any{"start": map[string]any{"dateTime": "2026-11-10T10:00:00"}})
	raw, _ := json.Marshal(linkedSnapshot([]any{naive}, nil))
	if _, _, err := PlanMeetings(raw, Config{}, state, testNow, testSite); err == nil || len(state.Meetings) != 1 {
		t.Errorf("naive start: err = %v, state = %+v", err, state.Meetings)
	}
}

func TestUnansweredInvitationsAndExplicitBlacklist(t *testing.T) {
	for _, response := range []string{"needsAction", "accepted", "tentative", ""} {
		state := &State{}
		got, _ := planLinked(t, linkedSnapshot([]any{linkedEvent(map[string]any{"self_response": response})}, nil), Config{}, state, testNow)
		if len(got) != 1 {
			t.Fatalf("response %q was excluded", response)
		}
		got, complete := planLinked(t, linkedSnapshot([]any{linkedEvent(map[string]any{"meeting_candidate": nil, "self_response": response})}, nil), Config{}, state, testNow)
		if len(got) != 1 || complete {
			t.Fatal("unknown evidence lost the existing notification")
		}
		got, _ = planLinked(t, linkedSnapshot([]any{linkedEvent(map[string]any{"meeting_candidate": nil, "self_response": response})}, nil), rules(t, `[{"account":"owner@example.com","title_contains":"No regular"}]`), state, testNow)
		if len(got) != 0 {
			t.Fatal("explicit blacklist did not cancel an unknown meeting")
		}
	}
}

func TestMeetingSourcesComeFromTheServer(t *testing.T) {
	event := linkedEvent(nil)
	// The calendars the server shows replace the built-in pair.
	one := Config{Sources: [][2]string{{"owner@example.com", "primary"}}}
	notices, complete := planLinked(t, linkedSnapshot([]any{event}, nil), one, &State{}, testNow)
	if !complete || len(notices) != 1 {
		t.Errorf("one calendar: complete = %v, notices = %+v", complete, notices)
	}
	other := Config{Sources: [][2]string{{"tanaka@example.com", "primary"}}}
	notices, complete = planLinked(t, linkedSnapshot([]any{event}, nil), other, &State{}, testNow)
	if complete || len(notices) != 0 {
		t.Errorf("a calendar the snapshot lacks: complete = %v, notices = %+v", complete, notices)
	}
	// A server that shows no calendar plans nothing, and nothing is missing.
	notices, complete = planLinked(t, linkedSnapshot([]any{event}, nil), Config{Sources: [][2]string{}}, &State{}, testNow)
	if !complete || len(notices) != 0 {
		t.Errorf("no calendar: complete = %v, notices = %+v", complete, notices)
	}
}

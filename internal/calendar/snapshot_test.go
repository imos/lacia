package calendar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scoped builds with the production scope at 07:00 on 2026-11-10.
func scoped(t *testing.T, snap any) View {
	t.Helper()
	parsed, err := parseWith(snap, Scope)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Build(parsed, instant(t, "2026-11-10T07:00:00+09:00"))
}

func TestScopeShowsTheTwoCalendarsAndFillsMissingOnes(t *testing.T) {
	snap := fixture()
	company := Scope[0]
	snap["sources"] = append(snap["sources"].([]any), with(source(snap, 0), obj{
		"account": company.Account, "calendar_id": company.CalendarID, "color": "#aa00ff", "scope_verified": true}))
	v := scoped(t, snap)
	if len(v.Sources) != 2 {
		t.Fatalf("sources = %+v", v.Sources)
	}
	if v.Sources[0].Color != "#aa00ff" || v.Sources[0].Label != "会社" {
		t.Errorf("company = %+v", v.Sources[0])
	}
	if v.Sources[1].Status != "unavailable" || v.Sources[1].Label != "Family" || v.Sources[1].Color != "#0b8043" {
		t.Errorf("family = %+v", v.Sources[1])
	}
	if v.Status != "partial" {
		t.Errorf("status = %q", v.Status)
	}
	dump, _ := json.Marshal(v)
	if strings.Contains(string(dump), "fixture@example.invalid") {
		t.Error("a calendar outside the scope leaked into the view")
	}

	// The same calendar listed twice is ambiguous and rejected.
	snap["sources"] = append(snap["sources"].([]any), snap["sources"].([]any)[1])
	if _, err := parseWith(snap, Scope); err == nil {
		t.Error("duplicate scoped source must be rejected")
	}
}

func TestCompanyCalendarRequiresVerifiedScope(t *testing.T) {
	snap := fixture()
	company := Scope[0]
	snap["sources"] = []any{with(source(snap, 0), obj{
		"account": company.Account, "calendar_id": company.CalendarID,
		"events": []any{allDay("cross-calendar", "範囲未確認の予定", "2026-11-10", "2026-11-11")}})}
	v := scoped(t, snap)
	if s := v.Sources[0]; s.Status != "unavailable" || s.ScopeVerified || !s.ScopeRequired || !s.UpdatedAt.IsZero() {
		t.Errorf("unverified company source = %+v", s)
	}
	for _, p := range v.Panels {
		if len(p.Events) != 0 {
			t.Errorf("panel %s shows events of an unverified calendar", p.Key)
		}
	}
	found := false
	for _, note := range v.SourceNotes {
		found = found || note == "予定の取得元が「会社」かどうかを確かめられないため、このカレンダーの予定は表示していません。"
	}
	if !found {
		t.Errorf("missing scope warning: %v", v.SourceNotes)
	}

	source(snap, 0)["scope_verified"] = true
	if got := len(scoped(t, snap).Panels[0].Events); got != 1 {
		t.Errorf("verified company calendar shows %d events", got)
	}
}

func TestRejectsMalformedSnapshots(t *testing.T) {
	timed := func(start, end string) obj {
		return obj{"id": "x", "start": obj{"dateTime": start}, "end": obj{"dateTime": end}}
	}
	withSource := func(changes obj) obj {
		snap := fixture()
		snap["sources"] = []any{with(source(snap, 0), changes)}
		return snap
	}
	cases := map[string]any{
		"not an object":         []any{},
		"wrong schema":          with(fixture(), obj{"schema_version": 2}),
		"invalid timezone":      with(fixture(), obj{"timezone": "Invalid/Timezone"}),
		"naive updated_at":      with(fixture(), obj{"updated_at": "2026-11-10T06:00:00"}),
		"updated_at object":     with(fixture(), obj{"updated_at": obj{}}),
		"numeric range_start":   with(fixture(), obj{"range_start": 42}),
		"impossible date":       with(fixture(), obj{"range_start": "2026-02-30"}),
		"empty range":           with(fixture(), obj{"range_end": "2026-11-09"}),
		"source not an object":  with(fixture(), obj{"sources": []any{"bad source"}}),
		"sources not a list":    with(fixture(), obj{"sources": obj{}}),
		"missing identity":      withSource(obj{"account": ""}),
		"status list":           withSource(obj{"status": []any{}}),
		"complete missing":      withSource(obj{"complete": nil}),
		"source updated_at":     withSource(obj{"updated_at": []any{}}),
		"label number":          withSource(obj{"label": 1}),
		"event not an object":   withSource(obj{"events": []any{"bad event"}}),
		"event boundary":        withSource(obj{"events": []any{obj{"id": "bad", "start": "invalid", "end": obj{}}}}),
		"all-day zero length":   fixture(allDay("bad", "x", "2026-11-10", "2026-11-10")),
		"timed reversed":        fixture(timed("2026-11-10T11:00:00+09:00", "2026-11-10T10:00:00+09:00")),
		"naive event time":      fixture(timed("2026-11-10T10:00:00", "2026-11-10T11:00:00")),
		"year out of range":     fixture(timed("0001-01-01T00:00:00+23:59", "2026-11-11T00:00:00+09:00")),
		"title number":          fixture(with(allDay("x", "", "2026-11-10", "2026-11-11"), obj{"title": 1})),
		"importance string":     fixture(with(allDay("x", "x", "2026-11-10", "2026-11-11"), obj{"important": "yes"})),
		"recurrence flag":       fixture(with(allDay("x", "x", "2026-11-10", "2026-11-11"), obj{"is_recurring": 1})),
		"unknown display basis": fixture(with(allDay("x", "x", "2026-11-10", "2026-11-11"), obj{"display_basis": "guess"})),
		"basis on timed event": fixture(with(timed("2026-11-10T10:00:00+09:00", "2026-11-10T11:00:00+09:00"),
			obj{"display_basis": "user_confirmed_day"})),
		"marker without basis": fixture(with(allDay("x", "x", "2026-11-10", "2026-11-11"),
			obj{"display_markers": []any{obj{"time": "10:00", "title": "x", "basis": "lacia_interpretation"}}})),
	}
	for name, snap := range cases {
		if _, err := parseWith(snap, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte("invalid json")); err == nil {
		t.Error("invalid JSON accepted")
	}

	// Events that are skipped are not validated, and unknown statuses degrade.
	tolerant := fixture(obj{"id": 7, "start": "invalid"}, obj{"id": "", "start": "invalid"},
		obj{"id": "gone", "status": "cancelled"})
	source(tolerant, 0)["status"] = "surprising"
	parsed, err := parseWith(tolerant, nil)
	if err != nil {
		t.Fatalf("tolerant snapshot rejected: %v", err)
	}
	if len(parsed.Sources[0].Events) != 0 || parsed.Sources[0].Status != "unavailable" {
		t.Errorf("tolerant source = %+v", parsed.Sources[0])
	}
}

func scopedFile(now time.Time) obj {
	stamp := now.Format(time.RFC3339)
	var sources []any
	for _, entry := range Scope {
		sources = append(sources, obj{"calendar_id": entry.CalendarID, "account": entry.Account,
			"label": entry.Label, "status": "ok", "complete": true, "scope_verified": true,
			"updated_at": stamp, "events": []any{}})
	}
	return obj{"schema_version": 1, "timezone": "Asia/Tokyo", "updated_at": stamp,
		"range_start": now.AddDate(0, 0, -7).Format("2006-01-02"),
		"range_end":   now.AddDate(0, 0, 8).Format("2006-01-02"), "sources": sources}
}

func TestLoadAndReaderKeepThePreviousSnapshot(t *testing.T) {
	now := instant(t, "2026-11-10T09:00:00+09:00")
	path := filepath.Join(t.TempDir(), "calendar-web-snapshot.md")
	write := func(content any) {
		t.Helper()
		data, ok := content.(string)
		if !ok {
			encoded, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			data = string(encoded)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reader := NewReader(path)
	missing, err := reader.View(now)
	if err == nil || !missing.ReadError || missing.Status != "unavailable" || missing.Notice != "最新の予定データを読み込めませんでした。" {
		t.Errorf("missing file: err=%v view=%q %q", err, missing.Status, missing.Notice)
	}

	good := scopedFile(now)
	write(good)
	snap, err := Load(path)
	if err != nil || !snap.Available || len(snap.Sources) != 2 {
		t.Fatalf("Load = %+v, %v", snap, err)
	}
	// Within 30 seconds the earlier failure is still the answer.
	if v, _ := reader.View(now.Add(29 * time.Second)); !v.ReadError {
		t.Error("re-read before the interval elapsed")
	}
	now = now.Add(30 * time.Second)
	ok, err := reader.View(now)
	if err != nil || ok.ReadError || ok.Status != "ok" || ok.Notice != "" {
		t.Fatalf("good file: err=%v status=%q notice=%q", err, ok.Status, ok.Notice)
	}

	first := good["sources"].([]any)[0].(obj)
	broken := []any{
		"invalid json",
		with(good, obj{"updated_at": obj{}}),
		with(good, obj{"range_start": 42}),
		with(good, obj{"sources": []any{"bad source"}}),
		with(good, obj{"sources": []any{with(first, obj{"status": []any{}})}}),
		with(good, obj{"sources": []any{with(first, obj{"updated_at": []any{}})}}),
		with(good, obj{"sources": []any{with(first, obj{"events": []any{"bad event"}})}}),
		with(good, obj{"sources": []any{with(first, obj{"events": []any{obj{"id": "bad", "start": "invalid", "end": obj{}}}})}}),
		with(good, obj{"sources": []any{with(first, obj{"events": []any{obj{"id": "overflow",
			"start": obj{"dateTime": "0001-01-01T00:00:00+23:59"}, "end": obj{"dateTime": "2026-11-11T00:00:00+09:00"}}}})}}),
	}
	for i, content := range broken {
		write(content)
		now = now.Add(30 * time.Second)
		v, err := reader.View(now)
		if err == nil || !v.ReadError {
			t.Errorf("broken[%d]: err=%v read_error=%v", i, err, v.ReadError)
		}
		if !v.UpdatedAt.Equal(ok.UpdatedAt) || len(v.Sources) != 2 || v.Sources[0].Label != "会社" {
			t.Errorf("broken[%d]: previous snapshot not kept: %+v", i, v.Sources)
		}
		if v.Notice != "最新の予定データを読み込めませんでした。前回読み込んだ予定を表示しています。" {
			t.Errorf("broken[%d]: notice = %q", i, v.Notice)
		}
		if got := v.Days[0].Notes; len(got) != 1 || got[0].Text != "予定があるかどうかは、まだ分かりません。" {
			t.Errorf("broken[%d]: an unreadable file must not claim an empty day: %+v", i, got)
		}
	}

	write(good)
	now = now.Add(30 * time.Second)
	if v, err := reader.View(now); err != nil || v.ReadError {
		t.Errorf("recovery: err=%v read_error=%v", err, v.ReadError)
	}
}

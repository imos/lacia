// Package calendar turns the saved calendar snapshot into a ready-to-render
// view: today and tomorrow on a shared 08:00-20:00 axis, a seven-day list, and
// per-calendar freshness. All placement is computed here so the template only
// places the results.
package calendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imos/lacia/internal/task"
)

// JST is the zone every day boundary and clock time is computed in.
var JST = task.JST

// ScopeEntry is one calendar the page is allowed to show.
type ScopeEntry struct {
	Account    string `json:"account"`
	CalendarID string `json:"calendar_id"`
	Label      string `json:"label"`
	Color      string `json:"color,omitempty"`
	// RequireVerified hides the calendar unless the collector recorded
	// scope_verified, so events of another calendar never appear under it.
	RequireVerified bool `json:"require_verified,omitempty"`
}

// LoadScope reads the calendars to show from a JSON file: a list of
// entries with account, calendar_id ("primary" for the account's own),
// label, and optionally color and require_verified.
func LoadScope(path string) ([]ScopeEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var scope []ScopeEntry
	if err := json.Unmarshal(data, &scope); err != nil {
		return nil, fmt.Errorf("%s を読めませんでした。%w", path, err)
	}
	seen := map[[2]string]bool{}
	for i, entry := range scope {
		key := [2]string{entry.Account, entry.CalendarID}
		if entry.Account == "" || entry.CalendarID == "" || entry.Label == "" {
			return nil, fmt.Errorf("%s の%d件目に account、calendar_id、label をすべて書いてください", path, i+1)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s の%d件目に、すでに指定したカレンダーがあります", path, i+1)
		}
		seen[key] = true
	}
	return scope, nil
}

// Scope is the set of calendars shown, in display order. The server may
// replace it once at start, before anything reads it.
var Scope []ScopeEntry

const (
	defaultColor = "#1a73e8"
	maxSnapshot  = 16 << 20
	maxSources   = 100
	maxEvents    = 10000
	maxMarkers   = 10
)

// Snapshot is a validated snapshot narrowed to the display scope.
// The zero value means "nothing fetched yet".
type Snapshot struct {
	Available    bool
	DirectGoogle bool
	UpdatedAt    time.Time
	RangeStart   time.Time // JST midnight, inclusive
	RangeEnd     time.Time // JST midnight, exclusive
	Sources      []Source
}

// Source is one calendar of one account.
type Source struct {
	Account       string
	CalendarID    string
	Label         string
	Color         string
	Status        string // ok, partial, error, pending or unavailable
	ScopeRequired bool
	ScopeVerified bool
	UpdatedAt     time.Time // zero when no fetch has succeeded
	AttemptedAt   time.Time
	RangeStart    time.Time
	RangeEnd      time.Time
	Events        []SourceEvent
}

// SourceEvent is an event that passed validation. Cancelled, duplicated and
// always-hidden events are already dropped.
type SourceEvent struct {
	ID           string
	Title        string
	Start        time.Time
	End          time.Time // exclusive; a date for all-day events
	AllDay       bool
	Private      bool
	Status       string
	SelfResponse string // accepted, declined, tentative, needsAction or ""
	Important    *bool
	Recurring    *bool
	Frequent     bool   // repeats weekly or more often
	URL          string // allow-listed Google Calendar link or ""
	DisplayBasis string // "" or "user_confirmed_day"
	Markers      []Marker
}

// Marker is a clock time read out of an all-day event by Lacia.
type Marker struct {
	Time   string // "HH:MM"
	Minute int    // minutes after midnight
	Title  string
}

func Load(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > maxSnapshot {
		return Snapshot{}, errors.New("calendar_snapshot_too_large")
	}
	return Parse(data)
}

// Parse validates snapshot JSON and narrows it to Scope.
func Parse(data []byte) (Snapshot, error) {
	return parse(data, Scope)
}

type object = map[string]any

// parse mirrors the validation order of the original gateway. A nil scope
// keeps every source as written.
func parse(data []byte, scope []ScopeEntry) (Snapshot, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return Snapshot{}, fmt.Errorf("invalid_calendar_json: %w", err)
	}
	if raw == nil {
		return Snapshot{}, nil
	}
	top, ok := raw.(object)
	if !ok {
		return Snapshot{}, errors.New("invalid_calendar_snapshot")
	}
	if len(top) == 0 {
		return Snapshot{}, nil
	}
	zone, _ := top["timezone"].(string)
	loc, zoneErr := time.LoadLocation(zone)
	if v, _ := top["schema_version"].(float64); v != 1 || zoneErr != nil || zone == "" || zone == "Local" {
		return Snapshot{}, errors.New("invalid_calendar_schema")
	}
	snap := Snapshot{Available: true, DirectGoogle: top["direct_google"] == true}
	var err error
	if snap.UpdatedAt, err = parseTimestamp(top["updated_at"], loc); err != nil {
		return Snapshot{}, err
	}
	if snap.RangeStart, err = parseDate(top["range_start"], loc); err != nil {
		return Snapshot{}, err
	}
	if snap.RangeEnd, err = parseDate(top["range_end"], loc); err != nil {
		return Snapshot{}, err
	}
	if !snap.RangeEnd.After(snap.RangeStart) {
		return Snapshot{}, errors.New("invalid_calendar_range")
	}
	var sources []any
	if v, present := top["sources"]; present {
		if sources, ok = v.([]any); !ok || len(sources) > maxSources {
			return Snapshot{}, errors.New("invalid_calendar_sources")
		}
	}
	if scope != nil {
		if sources, err = applyScope(sources, scope); err != nil {
			return Snapshot{}, err
		}
	}
	seen := map[[3]string]bool{}
	for _, item := range sources {
		src, ok := item.(object)
		if !ok {
			return Snapshot{}, errors.New("invalid_calendar_source")
		}
		parsed, err := parseSource(src, scope, seen, loc)
		if err != nil {
			return Snapshot{}, err
		}
		if parsed.RangeStart.IsZero() && parsed.Status == "ok" {
			parsed.RangeStart, parsed.RangeEnd = snap.RangeStart, snap.RangeEnd
		}
		snap.Sources = append(snap.Sources, parsed)
	}
	return snap, nil
}

// applyScope drops calendars outside the scope, fills in missing ones as
// unavailable, and blanks a scope-checked calendar that is not verified.
func applyScope(sources []any, scope []ScopeEntry) ([]any, error) {
	selected := map[[2]string]object{}
	for _, item := range sources {
		src, ok := item.(object)
		if !ok {
			return nil, errors.New("invalid_calendar_source")
		}
		account, _ := src["account"].(string)
		calendar, _ := src["calendar_id"].(string)
		key := [2]string{account, calendar}
		if scopeEntry(scope, account, calendar) == nil {
			continue
		}
		if _, dup := selected[key]; dup {
			return nil, errors.New("duplicate_calendar_source")
		}
		selected[key] = src
	}
	out := make([]any, 0, len(scope))
	for _, entry := range scope {
		src, ok := selected[[2]string{entry.Account, entry.CalendarID}]
		if !ok {
			src = object{"account": entry.Account, "calendar_id": entry.CalendarID,
				"label": entry.Label, "status": "unavailable", "complete": false,
				"updated_at": nil, "events": []any{}}
		}
		if entry.RequireVerified && src["scope_verified"] != true {
			blanked := object{}
			for k, v := range src {
				blanked[k] = v
			}
			blanked["status"], blanked["complete"] = "unavailable", false
			blanked["updated_at"], blanked["events"] = nil, []any{}
			blanked["scope_verified"] = false
			src = blanked
		}
		out = append(out, src)
	}
	return out, nil
}

func scopeEntry(scope []ScopeEntry, account, calendar string) *ScopeEntry {
	for i := range scope {
		if scope[i].Account == account && scope[i].CalendarID == calendar {
			return &scope[i]
		}
	}
	return nil
}

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func parseSource(src object, scope []ScopeEntry, seen map[[3]string]bool, zones ...*time.Location) (Source, error) {
	loc := calendarZone(zones)
	account, _ := src["account"].(string)
	calendar, _ := src["calendar_id"].(string)
	if account == "" || calendar == "" {
		return Source{}, errors.New("calendar_identity_required")
	}
	status := "unavailable"
	if v, present := src["status"]; present {
		s, ok := v.(string)
		if !ok {
			return Source{}, errors.New("invalid_calendar_status")
		}
		status = s
	}
	complete, ok := src["complete"].(bool)
	if !ok {
		return Source{}, errors.New("invalid_calendar_complete")
	}
	switch status {
	case "ok", "partial", "error", "pending", "unavailable":
	default:
		status = "unavailable"
	}
	var updated time.Time
	if v := src["updated_at"]; v != nil {
		var err error
		if updated, err = parseTimestamp(v, loc); err != nil {
			return Source{}, err
		}
	}
	if status == "ok" && !complete {
		status = "partial"
	}
	label := calendar
	if v, present := src["label"]; present {
		if label, ok = v.(string); !ok {
			return Source{}, errors.New("invalid_calendar_label")
		}
	}
	label = truncate(label, 100)
	color := defaultColor
	entry := scopeEntry(scope, account, calendar)
	if entry != nil {
		label = entry.Label
		if entry.Color != "" {
			color = entry.Color
		}
	}
	if c, ok := src["color"].(string); ok && colorPattern.MatchString(c) {
		color = c
	}
	out := Source{
		Account: truncate(account, 254), CalendarID: calendar, Label: label, Color: color,
		Status: status, ScopeRequired: entry != nil && entry.RequireVerified,
		ScopeVerified: src["scope_verified"] == true, UpdatedAt: updated,
	}
	var err error
	if v := src["attempted_at"]; v != nil {
		if out.AttemptedAt, err = parseTimestamp(v, loc); err != nil {
			return Source{}, err
		}
	}
	if src["range_start"] != nil || src["range_end"] != nil {
		if out.RangeStart, err = parseDate(src["range_start"], loc); err != nil {
			return Source{}, err
		}
		if out.RangeEnd, err = parseDate(src["range_end"], loc); err != nil || !out.RangeEnd.After(out.RangeStart) {
			return Source{}, errors.New("invalid_calendar_range")
		}
	}
	var events []any
	if v, present := src["events"]; present {
		if events, ok = v.([]any); !ok || len(events) > maxEvents {
			return Source{}, errors.New("invalid_calendar_events")
		}
	}
	for _, item := range events {
		raw, ok := item.(object)
		if !ok {
			return Source{}, errors.New("invalid_calendar_event")
		}
		event, keep, err := parseEvent(raw, account, calendar, seen, loc)
		if err != nil {
			return Source{}, err
		}
		if keep {
			out.Events = append(out.Events, event)
		}
	}
	return out, nil
}

var markerTime = regexp.MustCompile(`^(?:[01]\d|2[0-3]):[0-5]\d$`)

func parseEvent(raw object, account, calendar string, seen map[[3]string]bool, zones ...*time.Location) (SourceEvent, bool, error) {
	loc := calendarZone(zones)
	status := "confirmed"
	if v, present := raw["status"]; present {
		s, ok := v.(string)
		if !ok {
			return SourceEvent{}, false, errors.New("invalid_calendar_event_status")
		}
		status = s
	}
	if status == "cancelled" {
		return SourceEvent{}, false, nil
	}
	id, _ := raw["id"].(string)
	identity := [3]string{account, calendar, id}
	if id == "" || seen[identity] {
		return SourceEvent{}, false, nil
	}
	start, ok1 := raw["start"].(object)
	end, ok2 := raw["end"].(object)
	if !ok1 || !ok2 {
		return SourceEvent{}, false, errors.New("invalid_calendar_event_boundary")
	}
	_, startDate := start["date"]
	_, endDate := end["date"]
	allDay := startDate && endDate
	var begin, finish time.Time
	var err error
	if allDay {
		if begin, err = parseDate(start["date"], loc); err != nil {
			return SourceEvent{}, false, err
		}
		if finish, err = parseDate(end["date"], loc); err != nil {
			return SourceEvent{}, false, err
		}
	} else {
		if begin, err = parseTimestamp(start["dateTime"], loc); err != nil {
			return SourceEvent{}, false, err
		}
		if finish, err = parseTimestamp(end["dateTime"], loc); err != nil {
			return SourceEvent{}, false, err
		}
	}
	if !finish.After(begin) {
		return SourceEvent{}, false, errors.New("invalid_calendar_interval")
	}
	seen[identity] = true

	title := "予定"
	if v, present := raw["title"]; present {
		s, ok := v.(string)
		if !ok {
			return SourceEvent{}, false, errors.New("invalid_calendar_title")
		}
		title = s
	} else if v, present := raw["summary"]; present {
		s, ok := v.(string)
		if !ok {
			return SourceEvent{}, false, errors.New("invalid_calendar_title")
		}
		title = s
	}
	important, err := optionalBool(raw["important"], "invalid_calendar_importance")
	if err != nil {
		return SourceEvent{}, false, err
	}
	// Only a decision the user made hides or promotes an event in the week
	// view. A flag written by the collector without that basis has, in the
	// past, hidden every family event; treat it as not set.
	if basis, _ := raw["important_basis"].(string); basis != "user_override" {
		important = nil
	}
	recurring, err := optionalBool(raw["is_recurring"], "invalid_calendar_recurrence")
	if err != nil {
		return SourceEvent{}, false, err
	}
	seriesID, present := raw["recurring_event_id"]
	if !present {
		seriesID = raw["recurringEventId"]
	}
	rules, _ := raw["recurrence"].([]any)
	if s, ok := seriesID.(string); (ok && strings.TrimSpace(s) != "") || hasRecurrenceRule(rules) {
		yes := true
		recurring = &yes
	}
	if Hidden(title, false) {
		return SourceEvent{}, false, nil
	}
	basis := ""
	switch v := raw["display_basis"].(type) {
	case nil:
	case string:
		if v != "user_confirmed_day" || !allDay {
			return SourceEvent{}, false, errors.New("invalid_calendar_display_basis")
		}
		basis = v
	default:
		return SourceEvent{}, false, errors.New("invalid_calendar_display_basis")
	}
	response, _ := raw["self_response"].(string)
	switch response {
	case "accepted", "declined", "tentative", "needsAction":
	default:
		response = ""
	}
	visibility, _ := raw["visibility"].(string)
	event := SourceEvent{
		ID: id, Title: truncate(title, 300), Start: begin, End: finish, AllDay: allDay,
		Private: visibility == "private" || visibility == "confidential",
		Status:  status, SelfResponse: response, Important: important, Recurring: recurring,
		Frequent: frequent(rules, recurring), URL: EventLink(raw), DisplayBasis: basis,
	}
	var markers []any
	if v, present := raw["display_markers"]; present {
		var ok bool
		if markers, ok = v.([]any); !ok || len(markers) > maxMarkers {
			return SourceEvent{}, false, errors.New("invalid_calendar_markers")
		}
	}
	for _, item := range markers {
		marker, _ := item.(object)
		markerTitle, _ := marker["title"].(string)
		clock, _ := marker["time"].(string)
		if !allDay || basis != "user_confirmed_day" || marker["basis"] != "lacia_interpretation" ||
			strings.TrimSpace(markerTitle) == "" || !markerTime.MatchString(clock) {
			return SourceEvent{}, false, errors.New("invalid_calendar_marker")
		}
		hour, _ := strconv.Atoi(clock[:2])
		minute, _ := strconv.Atoi(clock[3:])
		event.Markers = append(event.Markers, Marker{Time: clock, Minute: hour*60 + minute, Title: truncate(markerTitle, 300)})
	}
	return event, true, nil
}

func optionalBool(v any, code string) (*bool, error) {
	switch b := v.(type) {
	case nil:
		return nil, nil
	case bool:
		return &b, nil
	}
	return nil, errors.New(code)
}

func hasRecurrenceRule(rules []any) bool {
	for _, rule := range rules {
		if s, ok := rule.(string); ok && (strings.HasPrefix(s, "RRULE:") || strings.HasPrefix(s, "RDATE:")) {
			return true
		}
	}
	return false
}

// frequent reports whether the series repeats at least once a week. Without a
// usable RRULE it falls back to the collector's is_recurring flag.
func frequent(rules []any, recurring *bool) bool {
	unitDays := map[string]float64{"SECONDLY": 1.0 / 86400, "MINUTELY": 1.0 / 1440, "HOURLY": 1.0 / 24, "DAILY": 1, "WEEKLY": 7}
	known, often := false, false
	for _, rule := range rules {
		s, ok := rule.(string)
		if !ok || !strings.HasPrefix(s, "RRULE:") {
			continue
		}
		fields := map[string]string{}
		for _, part := range strings.Split(strings.ToUpper(s[6:]), ";") {
			if k, v, ok := strings.Cut(part, "="); ok {
				fields[k] = v
			}
		}
		interval := 1
		if v, present := fields["INTERVAL"]; present {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				continue
			}
			interval = n
		}
		if interval < 1 {
			continue
		}
		freq := fields["FREQ"]
		if days, ok := unitDays[freq]; ok {
			known, often = true, often || float64(interval)*days <= 7
		} else if freq == "MONTHLY" || freq == "YEARLY" {
			known = true
		}
	}
	if known {
		return often
	}
	return recurring != nil && *recurring
}

var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04Z07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
}

var naiveLayouts = []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05.999999999", "2006-01-02"}

func parseTimestamp(v any, zones ...*time.Location) (time.Time, error) {
	loc := calendarZone(zones)
	s, ok := v.(string)
	if !ok {
		return time.Time{}, errors.New("invalid_calendar_timestamp")
	}
	for _, layout := range timestampLayouts {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		t = t.In(loc)
		if y := t.Year(); y < 1 || y > 9999 {
			return time.Time{}, errors.New("invalid_calendar_timestamp")
		}
		return t, nil
	}
	for _, layout := range naiveLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return time.Time{}, errors.New("calendar_timezone_required")
		}
	}
	return time.Time{}, errors.New("invalid_calendar_timestamp")
}

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func parseDate(v any, zones ...*time.Location) (time.Time, error) {
	loc := calendarZone(zones)
	s, ok := v.(string)
	if !ok || !datePattern.MatchString(s) {
		return time.Time{}, errors.New("invalid_calendar_date")
	}
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil || t.Year() < 1 {
		return time.Time{}, errors.New("invalid_calendar_date")
	}
	return t, nil
}

func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// EventLink reads the canonical snapshot field or its legacy alias.
func EventLink(event map[string]any) string {
	link, present := event["html_link"]
	if !present {
		link = event["htmlLink"]
	}
	value, _ := link.(string)
	return SafeLink(value)
}

// SafeLink returns the link reduced to an allow-listed Google Calendar event
// URL carrying only eid and ctz, or "" when it is anything else.
func SafeLink(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "calendar.google.com" && host != "www.google.com" {
		return ""
	}
	if port := u.Port(); port != "" && port != "443" {
		return ""
	}
	if u.EscapedPath() != "/calendar/event" {
		return ""
	}
	var query []string
	hasID := false
	for _, pair := range strings.Split(u.RawQuery, "&") {
		key, value, _ := strings.Cut(pair, "=")
		key, err1 := url.QueryUnescape(key)
		value, err2 := url.QueryUnescape(value)
		if err1 != nil || err2 != nil || value == "" || (key != "eid" && key != "ctz") {
			continue
		}
		hasID = hasID || key == "eid"
		query = append(query, key+"="+url.QueryEscape(value))
	}
	if !hasID {
		return ""
	}
	return "https://" + host + "/calendar/event?" + strings.Join(query, "&")
}

// titleKey normalises typography only: full-width ASCII, case and runs of
// white space. It does not guess that differently worded titles are the same.
func titleKey(title string) string {
	var b strings.Builder
	space := false
	for _, r := range title {
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFEE0
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// hidden holds the titles the owner chose not to see: always are never
// shown, week are only left out of the seven-day list.
var hidden struct{ always, week []*regexp.Regexp }

// SetHidden takes the patterns of the record repository's settings
// (calendar_hidden). Each is a regular expression matched against the title
// after titleKey: lower case, full-width ASCII narrowed, white space
// collapsed. The binary carries no patterns of its own.
func SetHidden(always, week []string) error {
	compile := func(patterns []string) ([]*regexp.Regexp, error) {
		var out []*regexp.Regexp
		for _, p := range patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("calendar_hidden の正規表現「%s」を読めませんでした。%w", p, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	a, err := compile(always)
	if err != nil {
		return err
	}
	w, err := compile(week)
	if err != nil {
		return err
	}
	hidden.always, hidden.week = a, w
	return nil
}

// Hidden reports whether a title is never shown, or with weekly set, whether
// it is also left out of the seven-day list.
func Hidden(title string, weekly bool) bool {
	key := titleKey(title)
	match := func(patterns []*regexp.Regexp) bool {
		for _, re := range patterns {
			if re.MatchString(key) {
				return true
			}
		}
		return false
	}
	return match(hidden.always) || (weekly && match(hidden.week))
}

func calendarZone(zones []*time.Location) *time.Location {
	if len(zones) > 0 && zones[0] != nil {
		return zones[0]
	}
	return JST
}

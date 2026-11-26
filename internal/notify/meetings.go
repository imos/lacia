package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imos/lacia/internal/calendar"
)

var (
	ErrCalendarUnavailable = errors.New("calendar_unavailable")
	ErrInvalidExclusion    = errors.New("invalid_exclusion")
)

// meetingSources are the calendars that produce meeting notices when the
// server does not say which it shows (Config.Sources).
// It is empty outside the tests: a server too old to say produces none.
var meetingSources [][2]string

const (
	leadSeconds      = 60    // fire this long before the start
	horizonSeconds   = 86400 // do not register further ahead
	freshnessSeconds = 2100  // a source older than this is not trusted
	maxClockSkew     = 5
)

// identity hashes the parts exactly like the original
// sha256(json.dumps(parts, ensure_ascii=False)), because delivered notices are
// deduplicated by ID across the migration.
func identity(parts ...string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, part := range parts {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('"')
		for _, r := range part {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				if r < 0x20 {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

var stampPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2})(?:[.,](\d+))?)?([+-])(\d{2}):?(\d{2})$`)

// parseStamp reads an ISO 8601 date-time that carries a UTC offset. It
// returns the instant and its Unix seconds as Python computes them.
func parseStamp(value string) (time.Time, float64, error) {
	m := stampPattern.FindStringSubmatch(strings.ReplaceAll(value, "Z", "+00:00"))
	if m == nil {
		return time.Time{}, 0, fmt.Errorf("%q は日時として読めません。日付、時刻、UTCとの時差を指定してください", value)
	}
	num := func(s string) int { n, _ := strconv.Atoi(s); return n }
	micro := 0
	if m[7] != "" {
		micro = num((m[7] + "000000")[:6])
	}
	offset := num(m[9])*3600 + num(m[10])*60
	if m[8] == "-" {
		offset = -offset
	}
	year, month, day, hour, minute, second := num(m[1]), num(m[2]), num(m[3]), num(m[4]), num(m[5]), num(m[6])
	t := time.Date(year, time.Month(month), day, hour, minute, second, micro*1000, time.FixedZone("", offset))
	if year < 1 || t.Year() != year || int(t.Month()) != month || t.Day() != day || hour > 23 || minute > 59 || second > 59 || num(m[9]) > 23 || num(m[10]) > 59 {
		return time.Time{}, 0, fmt.Errorf("日時 %q の日付、時刻、UTCとの時差のどれかが範囲外です", value)
	}
	return t, seconds(t), nil
}

// hashable mirrors Python raising on a list or object used as a set member.
func hashable(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return false
	}
	return true
}

// contains mirrors Python's `needle in v` for decoded JSON.
func contains(v any, needle string) (bool, error) {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, needle), nil
	case map[string]any:
		_, ok := x[needle]
		return ok, nil
	case []any:
		for _, item := range x {
			if item == any(needle) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, ErrCalendarUnavailable
}

// excluded applies the exclude_meetings rules: exact identity fields and a
// literal title substring. Rules are validated up to the first match.
func excluded(event map[string]any, account, calendar string, config Config) (bool, error) {
	if config.excludeInvalid {
		return false, ErrInvalidExclusion
	}
	for _, raw := range config.ExcludeMeetings {
		rule, ok := raw.(map[string]any)
		if !ok || len(rule) == 0 {
			return false, ErrInvalidExclusion
		}
		for key, value := range rule {
			switch key {
			case "account", "calendar_id", "event_id", "title_contains":
			default:
				return false, ErrInvalidExclusion
			}
			if s, ok := value.(string); !ok || s == "" {
				return false, ErrInvalidExclusion
			}
		}
		match := true
		for _, key := range []string{"account", "calendar_id", "event_id", "title_contains"} {
			value, ok := rule[key].(string)
			if !ok {
				continue
			}
			switch key {
			case "account":
				match = account == value
			case "calendar_id":
				match = calendar == value
			case "event_id":
				match = event["id"] == any(value)
			case "title_contains":
				title, present := event["title"]
				if !present {
					title = ""
				}
				var err error
				if match, err = contains(title, value); err != nil {
					return false, err
				}
			}
			if !match {
				break
			}
		}
		if match {
			return true, nil
		}
	}
	return false, nil
}

// stampField reads holder[field]["dateTime"].
func stampField(event map[string]any, field string) (time.Time, float64, error) {
	holder, ok := event[field].(map[string]any)
	if !ok {
		return time.Time{}, 0, ErrCalendarUnavailable
	}
	value, ok := holder["dateTime"].(string)
	if !ok {
		return time.Time{}, 0, ErrCalendarUnavailable
	}
	return parseStamp(value)
}

// PlanMeetings plans meeting notices from the raw calendar snapshot, using
// the source records rather than what the Web page chooses to display.
//
// Missing information never cancels a notice planned earlier: a stale or
// failed source, or an event whose meeting_candidate is unknown, leaves the
// previous notices in place and makes the result incomplete. On an error the
// state is left untouched.
func PlanMeetings(raw []byte, config Config, state *State, now time.Time, siteURL string) (notices []Notice, complete bool, err error) {
	return planMeetings(raw, config, state, now, siteURL, nil)
}

func planMeetings(raw []byte, config Config, state *State, now time.Time, siteURL string, confirmed map[string]bool) (notices []Notice, complete bool, err error) {
	if !utf8.Valid(raw) {
		return nil, false, ErrCalendarUnavailable
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrCalendarUnavailable, err)
	}
	sources, ok := snapshot["sources"].([]any)
	zone, _ := snapshot["timezone"].(string)
	loc, zoneErr := time.LoadLocation(zone)
	if snapshot["schema_version"] != any(float64(1)) || zoneErr != nil || zone == "" || zone == "Local" || !ok {
		return nil, false, ErrCalendarUnavailable
	}
	if config.Location != nil {
		loc = config.Location
	}
	at := seconds(now)
	home := strings.TrimRight(siteURL, "/") + "/"

	// retained keeps insertion order, which is the order of the manifest.
	var retained []Notice
	set := func(n Notice) {
		for i := range retained {
			if retained[i].ID == n.ID {
				retained[i] = n
				return
			}
		}
		retained = append(retained, n)
	}
	for _, n := range state.Meetings {
		if n.Expires > at {
			set(n)
		}
	}

	wanted := config.Sources
	if wanted == nil {
		wanted = meetingSources
	}
	isWanted := func(key [2]string) bool {
		for _, w := range wanted {
			if w == key {
				return true
			}
		}
		return false
	}
	if config.Sources != nil {
		retained = keep(retained, func(n Notice) bool {
			for _, key := range wanted {
				if strings.HasPrefix(n.ID, "meeting-"+identity(key[0], key[1])+"-") {
					return true
				}
			}
			return false
		})
	}
	known := map[[2]string]bool{}
	complete = true
	for _, rawSource := range sources {
		source, ok := rawSource.(map[string]any)
		if !ok || !hashable(source["account"]) || !hashable(source["calendar_id"]) {
			return nil, false, ErrCalendarUnavailable
		}
		account, _ := source["account"].(string)
		calendarID, _ := source["calendar_id"].(string)
		key := [2]string{account, calendarID}
		if !isWanted(key) || known[key] {
			continue
		}
		known[key] = true
		prefix := "meeting-" + identity(account, calendarID) + "-"
		if source["access_revoked"] == true {
			retained = keep(retained, func(n Notice) bool { return !strings.HasPrefix(n.ID, prefix) })
			complete = false
			continue
		}

		if !hashable(source["status"]) {
			return nil, false, ErrCalendarUnavailable
		}
		status, _ := source["status"].(string)
		fresh := (status == "ok" || status == "partial") && source["scope_verified"] == any(true) && truthy(source["updated_at"])
		if fresh {
			updated, ok := source["updated_at"].(string)
			if !ok {
				return nil, false, ErrCalendarUnavailable
			}
			_, updatedAt, err := parseStamp(updated)
			if err != nil {
				return nil, false, fmt.Errorf("%w: %v", ErrCalendarUnavailable, err)
			}
			fresh = -maxClockSkew <= at-updatedAt && at-updatedAt <= freshnessSeconds
		}
		if !fresh {
			complete = false
			continue
		}
		sourceComplete := source["complete"] == any(true) && status == "ok"
		complete = complete && sourceComplete
		// A partial source can contain events copied from the last fetch.
		canConfirm := sourceComplete && confirmed != nil

		var events []any
		if value, present := source["events"]; present {
			if events, ok = value.([]any); !ok {
				return nil, false, ErrCalendarUnavailable
			}
		}
		var observed []string
		for _, rawEvent := range events {
			event, ok := rawEvent.(map[string]any)
			if !ok {
				return nil, false, ErrCalendarUnavailable
			}
			id, _ := event["id"].(string)
			if id == "" {
				continue
			}
			eventPrefix := prefix + identity(id) + "-"
			observed = append(observed, eventPrefix)
			var previous []Notice
			for _, n := range retained {
				if strings.HasPrefix(n.ID, eventPrefix) {
					previous = append(previous, n)
					delete(confirmed, n.ID)
				}
			}

			candidate := event["meeting_candidate"]
			explicit := candidate == any(false) || event["status"] == any("cancelled") || event["self_response"] == any("declined")
			if !explicit {
				allDay := false
				if start, present := event["start"]; present {
					if allDay, err = contains(start, "date"); err != nil {
						return nil, false, err
					}
				}
				explicit = allDay || truthy(event["display_basis"])
			}
			if candidate != any(true) && !explicit {
				// An explicit blacklist rule wins even when meeting evidence is missing.
				if skip, ruleErr := excluded(event, account, calendarID, config); ruleErr != nil {
					return nil, false, ruleErr
				} else if skip {
					retained = keep(retained, func(n Notice) bool { return !strings.HasPrefix(n.ID, eventPrefix) })
					continue
				}
				// Unknown meeting metadata is missing information, even if
				// the source finished paging.
				complete = false
				continue
			}
			// An explicit observation replaces this event's occurrences.
			retained = keep(retained, func(n Notice) bool { return !strings.HasPrefix(n.ID, eventPrefix) })
			if explicit {
				continue
			}

			startTime, start, err := stampField(event, "start")
			if err != nil {
				return nil, false, fmt.Errorf("%w: %v", ErrCalendarUnavailable, err)
			}
			_, end, err := stampField(event, "end")
			if err != nil {
				return nil, false, fmt.Errorf("%w: %v", ErrCalendarUnavailable, err)
			}
			noticeID := eventPrefix + strconv.FormatInt(int64(start*1000), 10)
			if end <= start {
				continue
			}
			if skip, err := excluded(event, account, calendarID, config); err != nil {
				return nil, false, err
			} else if skip {
				continue
			}
			link := calendar.EventLink(event)
			if start-leadSeconds <= at {
				// An already planned event may have moved into the delivery
				// window. Replace its old occurrence even this late, but do
				// not announce an unrelated event discovered after fire time.
				if start <= at {
					continue
				}
				matched := false
				for _, n := range previous {
					if n.ID == noticeID {
						if link != "" {
							n.URL = link
						}
						set(n)
						matched = true
						if canConfirm {
							confirmed[noticeID] = true
						}
					}
				}
				if matched || len(previous) == 0 {
					continue
				}
			}
			if start-leadSeconds > at+horizonSeconds {
				continue
			}
			title := "非公開の予定"
			if event["visibility"] != any("private") {
				title = "会議"
				if value := event["title"]; truthy(value) {
					if title, ok = value.(string); !ok {
						return nil, false, ErrCalendarUnavailable
					}
				}
			}
			if link == "" {
				link = home
			}
			set(Notice{
				ID:      noticeID,
				Kind:    KindMeeting,
				Title:   "会議が1分後に始まります",
				Body:    startTime.In(loc).Format("15:04") + " " + title,
				URL:     link,
				Fire:    start - leadSeconds,
				Expires: start,
			})
			if canConfirm {
				confirmed[noticeID] = true
			}
		}
		if sourceComplete {
			// Absence is authoritative for the actual window read by this source.
			retained = keep(retained, func(n Notice) bool {
				if !strings.HasPrefix(n.ID, prefix) || !noticeInsideRange(n, source, snapshot, loc) {
					return true
				}
				for _, eventPrefix := range observed {
					if strings.HasPrefix(n.ID, eventPrefix) {
						return true
					}
				}
				return false
			})
		}
	}
	complete = complete && len(known) == len(wanted)
	state.Meetings = keep(retained, func(Notice) bool { return true })
	return state.Meetings, complete, nil
}

func noticeInsideRange(notice Notice, source, snapshot map[string]any, loc *time.Location) bool {
	const layout = "2006-01-02"
	for _, item := range []map[string]any{source, snapshot} {
		start, _ := item["range_start"].(string)
		end, _ := item["range_end"].(string)
		if start == "" && end == "" {
			continue
		}
		from, errFrom := time.ParseInLocation(layout, start, loc)
		until, errUntil := time.ParseInLocation(layout, end, loc)
		return errFrom == nil && errUntil == nil && notice.Expires >= seconds(from) && notice.Expires < seconds(until)
	}
	// Older snapshots did not always carry a range.
	return true
}

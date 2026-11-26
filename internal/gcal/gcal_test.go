package gcal

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/calendar"
)

func TestCalendarSourceNeedsAllPagesAndDistinguishesPermissions(t *testing.T) {
	mux := http.NewServeMux()
	mode := "success"
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"fixture"}`))
	})
	mux.HandleFunc("GET /api/calendars/primary", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"owner@example.invalid"}`))
	})
	mux.HandleFunc("GET /api/calendars/shared/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageToken") == "" {
			w.Write([]byte(`{"accessRole":"reader","items":[],"nextPageToken":"two"}`))
			return
		}
		switch mode {
		case "success":
			w.Write([]byte(`{"accessRole":"reader","items":[{"id":"fixture","status":"confirmed","start":{"date":"2026-11-13"},"end":{"date":"2026-11-14"}}]}`))
		case "empty":
			w.Write([]byte(`{"accessRole":"reader","items":[]}`))
		case "invalid":
			w.Write([]byte(`{"accessRole":"reader","items":[{"id":"fixture","start":{"date":"2026-11-14"},"end":{"date":"2026-11-13"}}]}`))
		case "revoked":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"errors":[{"reason":"forbidden"}]}}`))
		case "quota":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := &Client{Endpoints: Endpoints{Token: srv.URL + "/token", API: srv.URL + "/api"}}
	start := time.Date(2026, 11, 12, 0, 0, 0, 0, calendar.JST)
	for _, tc := range []struct {
		mode   string
		events int
		denied bool
	}{
		{"success", 1, false}, {"empty", 0, false}, {"invalid", -1, false},
		{"revoked", -1, true}, {"quota", -1, false}, {"failed", -1, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			mode = tc.mode
			src, err := client.Source(context.Background(), Credential{}, "owner@example.invalid",
				calendar.ScopeEntry{CalendarID: "shared", Account: "owner@example.invalid"}, "共有", start, start.AddDate(0, 0, 7), start, nil)
			if (err != nil) != (tc.events < 0) || errors.Is(err, errCalendarAccess) != tc.denied {
				t.Fatalf("result = %v, %v", src, err)
			}
			if tc.events >= 0 && len(src["events"].([]map[string]any)) != tc.events {
				t.Fatalf("events = %v", src["events"])
			}
		})
	}
}

// familyShared makes the fake share the family calendar with the work account.
var familyShared bool

// fakeGoogle answers the token and calendar endpoints for two accounts.
func fakeGoogle(t *testing.T, failFamily *bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch {
		case r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") == "good-code" && r.Form.Get("client_secret") == "secret":
			json.NewEncoder(w).Encode(map[string]string{"access_token": "access-" + r.Form.Get("client_id"), "refresh_token": "refresh-" + r.Form.Get("client_id")})
		case r.Form.Get("grant_type") == "refresh_token" && strings.HasPrefix(r.Form.Get("refresh_token"), "refresh-"):
			json.NewEncoder(w).Encode(map[string]string{"access_token": "access-" + r.Form.Get("client_id")})
		default:
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		}
	})
	account := func(r *http.Request) string {
		return map[string]string{"Bearer access-work": calendar.Scope[0].Account, "Bearer access-home": calendar.Scope[1].Account}[r.Header.Get("Authorization")]
	}
	mux.HandleFunc("GET /api/calendars/primary", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": account(r)})
	})
	mux.HandleFunc("GET /api/calendars/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if account(r) == "" {
			w.WriteHeader(401)
			return
		}
		if r.PathValue("id") == calendar.Scope[1].CalendarID {
			if account(r) != calendar.Scope[1].Account && !familyShared {
				w.WriteHeader(404)
				return
			}
			if *failFamily {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(`{"accessRole":"reader","items":[
			 {"id":"school","status":"confirmed","summary":"午前授業","start":{"date":"2026-11-13"},"end":{"date":"2026-11-14"},"htmlLink":"https://www.google.com/calendar/event?eid=x"}]}`))
			return
		}
		if r.URL.Query().Get("singleEvents") != "true" || r.URL.Query().Get("timeMin") == "" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("pageToken") == "" {
			w.Write([]byte(`{"accessRole":"owner","nextPageToken":"p2","items":[
			 {"id":"sync_1","status":"confirmed","summary":"定例","start":{"dateTime":"2026-11-12T01:00:00Z"},"end":{"dateTime":"2026-11-12T02:00:00Z"},"recurringEventId":"sync",
			  "attendees":[{"self":true,"responseStatus":"accepted"},{"responseStatus":"accepted"}],"hangoutLink":"https://meet.google.com/abc"},
			 {"id":"declined","status":"confirmed","summary":"欠席する会議","start":{"dateTime":"2026-11-12T13:00:00+09:00"},"end":{"dateTime":"2026-11-12T14:00:00+09:00"},
			  "attendees":[{"self":true,"responseStatus":"declined"},{"responseStatus":"accepted"}]},
			 {"id":"office","status":"confirmed","summary":"Office","eventType":"workingLocation","start":{"date":"2026-11-12"},"end":{"date":"2026-11-13"}}]}`))
			return
		}
		w.Write([]byte(`{"accessRole":"owner","items":[
         {"id":"all-hands","status":"confirmed","summary":"All hands fixture","description":"Join: <a href=\"https://company.zoom.us/j/fixture\">meeting</a>","start":{"dateTime":"2026-11-12T11:00:00+09:00"},"end":{"dateTime":"2026-11-12T12:00:00+09:00"},"attendees":[{"self":true,"responseStatus":"needsAction"}]},
		 {"id":"alone","status":"confirmed","summary":"作業","visibility":"private","start":{"dateTime":"2026-11-12T15:00:00+09:00"},"end":{"dateTime":"2026-11-12T16:00:00+09:00"}}]}`))
	})
	mux.HandleFunc("GET /api/calendars/{id}/events/{event}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"sync","recurrence":["RRULE:FREQ=WEEKLY;BYDAY=TH"]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchWritesAReadableSnapshot(t *testing.T) {
	failFamily := false
	srv := fakeGoogle(t, &failFamily)
	client := &Client{Endpoints: Endpoints{Auth: srv.URL + "/auth", Token: srv.URL + "/token", API: srv.URL + "/api"}}
	dir := t.TempDir()
	store := &Store{Path: filepath.Join(dir, "secrets", "gcal.json")}
	ctx := context.Background()

	if _, _, err := client.Exchange(ctx, "work", "wrong", "http://x/cb", "good-code", "verifier"); err == nil {
		t.Error("a wrong client secret was accepted")
	}
	f := &Fetcher{Store: store, Client: client, Path: filepath.Join(dir, "calendar", "snapshot.json"), Overrides: filepath.Join(dir, "overrides.json"),
		Now: func() time.Time { return time.Date(2026, 11, 12, 0, 30, 0, 0, time.UTC) }}
	for i, id := range []string{"work", "home"} {
		if f.Ready() {
			t.Error("ready before every calendar is connected")
		}
		cred, access, err := client.Exchange(ctx, id, "secret", "http://x/cb", "good-code", "verifier")
		if err != nil {
			t.Fatal(err)
		}
		if owner, _ := client.PrimaryID(ctx, access); owner != calendar.Scope[i].Account {
			t.Errorf("owner = %q", owner)
		}
		if err := store.Put(calendar.Scope[i].Account, cred); err != nil {
			t.Fatal(err)
		}
	}
	if info, _ := os.Stat(store.Path); info.Mode().Perm() != 0o600 {
		t.Errorf("credentials file mode = %v", info.Mode().Perm())
	}
	os.WriteFile(f.Overrides, []byte(`{"events":[{"id":"school","display_basis":"user_confirmed_day","display_markers":[{"time":"13:30","title":"下校","basis":"lacia_interpretation"}]}]}`), 0o644)

	if !f.Ready() {
		t.Fatal("not ready after connecting both")
	}
	if err := f.Once(ctx); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Path)
	if _, err := calendar.Parse(data); err != nil {
		t.Fatalf("the pages cannot read the snapshot: %v\n%s", err, data)
	}
	var snap struct {
		RangeStart string `json:"range_start"`
		RangeEnd   string `json:"range_end"`
		Sources    []struct {
			Status string
			Events []map[string]any
		}
	}
	json.Unmarshal(data, &snap)
	if snap.RangeStart != "2026-11-12" || snap.RangeEnd != "2026-11-19" {
		t.Errorf("range = %s..%s", snap.RangeStart, snap.RangeEnd)
	}
	work := map[string]map[string]any{}
	for _, e := range snap.Sources[0].Events {
		work[e["id"].(string)] = e
	}
	if len(work) != 4 || work["office"] != nil || work["broken"] != nil {
		t.Errorf("work events = %v", work)
	}
	sync := work["sync_1"]
	if sync["meeting_candidate"] != true || sync["self_response"] != "accepted" || sync["is_recurring"] != true ||
		sync["start"].(map[string]any)["dateTime"] != "2026-11-12T10:00:00+09:00" || len(sync["recurrence"].([]any)) != 1 {
		t.Errorf("recurring meeting = %v", sync)
	}
	if work["declined"]["meeting_candidate"] != false || work["alone"]["meeting_candidate"] != nil || work["alone"]["visibility"] != "private" {
		t.Errorf("non-meetings: %v / %v", work["declined"], work["alone"])
	}
	if work["all-hands"]["meeting_candidate"] != true || work["all-hands"]["self_response"] != "needsAction" {
		t.Fatal("unanswered description-only meeting lost")
	}
	for _, leaked := range []string{"zoom.us", "description", "meet.google.com", "attendees", "hangoutLink", "refresh-", "secret"} {
		if strings.Contains(string(data), leaked) {
			t.Errorf("snapshot contains %q", leaked)
		}
	}
	school := snap.Sources[1].Events[0]
	if school["display_basis"] != "user_confirmed_day" || school["start"].(map[string]any)["date"] != "2026-11-13" || school["display_markers"] == nil {
		t.Errorf("all-day event with override = %v", school)
	}

	// A failing calendar keeps what it had.
	failFamily = true
	if err := f.Once(ctx); err == nil {
		t.Error("the failure was not reported")
	}
	data, _ = os.ReadFile(f.Path)
	json.Unmarshal(data, &snap)
	if snap.Sources[1].Status != "partial" || len(snap.Sources[1].Events) != 1 || snap.Sources[0].Status != "ok" {
		t.Errorf("after a failure: %+v", snap.Sources)
	}
	if _, err := calendar.Parse(data); err != nil {
		t.Errorf("snapshot after a failure is unreadable: %v", err)
	}
}

func TestSharedCalendarIsReadThroughAnotherAccount(t *testing.T) {
	familyShared = true
	defer func() { familyShared = false }()
	failFamily := false
	srv := fakeGoogle(t, &failFamily)
	client := &Client{Endpoints: Endpoints{Auth: srv.URL + "/auth", Token: srv.URL + "/token", API: srv.URL + "/api"}}
	dir := t.TempDir()
	store := &Store{Path: filepath.Join(dir, "gcal.json")}
	f := &Fetcher{Store: store, Client: client, Path: filepath.Join(dir, "snapshot.json"),
		Now: func() time.Time { return time.Date(2026, 11, 12, 0, 30, 0, 0, time.UTC) }}
	if f.Ready() {
		t.Fatal("ready with nothing connected")
	}
	cred, _, err := client.Exchange(context.Background(), "work", "secret", "http://x/cb", "good-code", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	store.Put(calendar.Scope[0].Account, cred)
	readers := f.Readers(context.Background())
	if readers[[2]string{calendar.Scope[1].Account, calendar.Scope[1].CalendarID}] != calendar.Scope[0].Account || !f.Ready() {
		t.Fatalf("readers = %v", readers)
	}
	if err := f.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Path)
	var snap struct {
		Sources []struct {
			Account string
			Status  string
			Events  []map[string]any
		}
	}
	json.Unmarshal(data, &snap)
	// The source keeps the calendar's own account, whoever read it.
	if snap.Sources[1].Account != calendar.Scope[1].Account || snap.Sources[1].Status != "ok" || len(snap.Sources[1].Events) != 1 {
		t.Errorf("family source = %+v", snap.Sources[1])
	}
}

func TestMeetingEvidenceAndEmbeddedLinks(t *testing.T) {
	for _, response := range []string{"needsAction", "accepted", "tentative", ""} {
		e := apiEvent{Status: "confirmed", Description: `<p>Join: <a href="https://company.zoom.us/j/fixture?x=1&amp;y=2">meeting</a></p>`}
		if got := meetingEvidence(e, true, response, false); got != true {
			t.Errorf("response %q: %v", response, got)
		}
		if got := meetingEvidence(e, true, "declined", false); got != false {
			t.Fatal("declined restored")
		}
		if got := meetingEvidence(e, false, response, false); got != false {
			t.Fatal("all-day accepted")
		}
	}
	for _, link := range []string{"https://zoom.us.example.test/j/x", "https://zoom.us@evil.test/j/x", "https://evil.test@zoom.us/j/x", "ftp://zoom.us/j/x"} {
		if conference(apiEvent{Description: link}) {
			t.Errorf("unsafe link accepted: %s", link)
		}
	}
	if !conference(apiEvent{Location: "Join here: https://meet.google.com/fixture"}) {
		t.Fatal("embedded location ignored")
	}
	if got := meetingEvidence(apiEvent{}, true, "needsAction", false); got != nil {
		t.Errorf("missing evidence = %v", got)
	}
	if got := meetingEvidence(apiEvent{Status: "cancelled"}, true, "accepted", true); got != false {
		t.Fatal("cancelled accepted")
	}
}

func TestLoadClient(t *testing.T) {
	write := func(text string) string {
		path := filepath.Join(t.TempDir(), "client.json")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// The file as Google's console offers it, for either kind of client.
	for _, kind := range []string{"installed", "web"} {
		id, secret, err := LoadClient(write(`{"` + kind + `":{"client_id":"an-id","client_secret":"a-secret","redirect_uris":["http://localhost"]}}`))
		if err != nil || id != "an-id" || secret != "a-secret" {
			t.Errorf("%s: %q %v", kind, id, err)
		}
	}
	for _, bad := range []string{`{}`, `{"installed":{"client_id":"an-id"}}`, `not JSON`} {
		if _, _, err := LoadClient(write(bad)); err == nil || strings.Contains(err.Error(), "a-secret") {
			t.Errorf("%q was accepted: %v", bad, err)
		}
	}
	if _, _, err := LoadClient(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing file was accepted")
	}
	// A verifier and its challenge belong together, and differ every time.
	v1, c1 := Challenge()
	v2, _ := Challenge()
	sum := sha256.Sum256([]byte(v1))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != c1 || v1 == v2 || len(v1) < 43 {
		t.Errorf("challenge: %q %q", v1, c1)
	}
}

// Package gcal reads Google Calendar directly and writes the snapshot the
// pages and the local notification agent use. It replaces the scheduled Codex job that
// produced the same file through a connector: the API returns exact types
// and expanded recurrences, so nothing has to be inferred.
package gcal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/network"
	"github.com/imos/lacia/internal/task"
)

// Scope is the permission requested by default: read events, change nothing.
const Scope = "https://www.googleapis.com/auth/calendar.readonly"

// ScopeEvents also allows creating and changing events. It is requested only
// where the settings ask for it; nothing in the server writes to a calendar.
const ScopeEvents = "https://www.googleapis.com/auth/calendar.events"

// LoadClient reads the client ID and secret from the JSON file Google's
// console offers for download. A client of the "desktop app" kind is what a
// Lacia on one's own machine uses: Google does not treat its secret as one,
// the user is sent back to a loopback address, and the code is bound to this
// process with PKCE. A "web application" client works the same way here.
func LoadClient(path string) (id, secret string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	if id, secret, err = parseClient(data); err != nil {
		return "", "", fmt.Errorf("%s: %w", path, err)
	}
	return id, secret, nil
}

func parseClient(data []byte) (id, secret string, err error) {
	var file map[string]struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return "", "", fmt.Errorf("Google のクライアント設定を JSON として読めませんでした: %w", err)
	}
	for _, kind := range []string{"installed", "web"} {
		if c := file[kind]; c.ClientID != "" && c.ClientSecret != "" {
			return c.ClientID, c.ClientSecret, nil
		}
	}
	return "", "", errors.New("クライアントIDかシークレットがありません。Google Cloud の「認証情報」からダウンロードした JSON を指定してください")
}

// Challenge returns a fresh PKCE verifier and its S256 challenge. The
// verifier stays in this process; a code caught on its way back is useless
// without it.
func Challenge() (verifier, challenge string) {
	buf := make([]byte, 32)
	rand.Read(buf)
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// Endpoints of Google's services; tests point them at a fake.
type Endpoints struct {
	Auth  string
	Token string
	API   string
}

// Google is the real service.
var Google = Endpoints{
	Auth:  "https://accounts.google.com/o/oauth2/v2/auth",
	Token: "https://oauth2.googleapis.com/token",
	API:   "https://www.googleapis.com/calendar/v3",
}

// Credential is what one account needs to be read. It lives in a private
// file outside Git and is never shown on a page or written to a log.
type Credential struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	Connected    string `json:"connected"` // when the user granted access
}

// Store keeps the credentials file.
type Store struct {
	Path string
	mu   sync.Mutex
}

func (s *Store) load() (map[string]Credential, error) {
	out := map[string]Credential{}
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(data, &out)
}

// Get returns the credential of an account, if it is connected.
func (s *Store) Get(account string) (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	c, ok := all[account]
	return c, err == nil && ok && c.RefreshToken != ""
}

// Put saves the credential of an account.
func (s *Store) Put(account string, c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	all[account] = c
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return disk.WriteFileNoSync(s.Path, append(data, '\n'), 0o600)
}

// Remove disconnects an account.
func (s *Store) Remove(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	delete(all, account)
	data, _ := json.MarshalIndent(all, "", "  ")
	return disk.WriteFileNoSync(s.Path, append(data, '\n'), 0o600)
}

// Client talks to Google for one credential.
type Client struct {
	Endpoints Endpoints
	HTTP      *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return network.Internet(30 * time.Second)
}

// AuthURL is where the user grants access. loginHint preselects the
// account; state ties the answer to the request that started it; challenge
// is the PKCE challenge whose verifier Exchange sends; scope is Scope, or
// Scope and ScopeEvents separated by a space.
func (c *Client) AuthURL(clientID, redirect, state, loginHint, challenge, scope string) string {
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {scope},
		"access_type": {"offline"}, "prompt": {"consent"}, "state": {state}, "login_hint": {loginHint},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	return c.Endpoints.Auth + "?" + q.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (c *Client) token(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoints.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	var out tokenResponse
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("Googleの応答を読めませんでした（%d）", resp.StatusCode)
	}
	if out.Error != "" || out.AccessToken == "" {
		// The description never contains a secret; the request body is not echoed.
		return out, fmt.Errorf("Googleが認証を受け付けませんでした: %s %s", out.Error, out.Description)
	}
	return out, nil
}

// Exchange turns the code from the consent screen into a credential.
func (c *Client) Exchange(ctx context.Context, clientID, clientSecret, redirect, code, verifier string) (Credential, string, error) {
	t, err := c.token(ctx, url.Values{"client_id": {clientID}, "client_secret": {clientSecret}, "redirect_uri": {redirect},
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}})
	if err != nil {
		return Credential{}, "", err
	}
	if t.RefreshToken == "" {
		return Credential{}, "", errors.New("次回の接続に必要な更新トークンを Google から受け取れませんでした。Google アカウントの設定でこのアプリとの接続を外してから、もう1度試してください")
	}
	return Credential{ClientID: clientID, ClientSecret: clientSecret, RefreshToken: t.RefreshToken}, t.AccessToken, nil
}

// Access gets a short-lived access token.
func (c *Client) Access(ctx context.Context, cred Credential) (string, error) {
	t, err := c.token(ctx, url.Values{"client_id": {cred.ClientID}, "client_secret": {cred.ClientSecret},
		"grant_type": {"refresh_token"}, "refresh_token": {cred.RefreshToken}})
	return t.AccessToken, err
}

var errCalendarAccess = errors.New("予定の詳細を読み取れる権限がありません")

type googleAPIError struct {
	status  int
	message string
	reasons []string
}

func (e *googleAPIError) Error() string {
	return fmt.Sprintf("Google Calendarが %d を返しました: %s", e.status, e.message)
}

func calendarAccessError(err error) error {
	var apiErr *googleAPIError
	if errors.As(err, &apiErr) {
		if apiErr.status == http.StatusNotFound {
			return errCalendarAccess
		}
		if apiErr.status == http.StatusForbidden {
			for _, reason := range apiErr.reasons {
				switch reason {
				case "forbidden", "insufficientPermissions":
					return errCalendarAccess
				}
			}
		}
	}
	return err
}

func (c *Client) get(ctx context.Context, access, path string, query url.Values, out any) error {
	u := c.Endpoints.API + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Errors  []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
			} `json:"error"`
		}
		json.Unmarshal(data, &e)
		apiErr := &googleAPIError{status: resp.StatusCode, message: e.Error.Message}
		for _, detail := range e.Error.Errors {
			apiErr.reasons = append(apiErr.reasons, detail.Reason)
		}
		return apiErr
	}
	return json.Unmarshal(data, out)
}

// PrimaryID returns the address of the account the token belongs to. It is
// how a grant made with the wrong account is caught.
func (c *Client) PrimaryID(ctx context.Context, access string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.get(ctx, access, "/calendars/primary", nil, &out)
	return out.ID, err
}

type apiTime struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
}

type apiEvent struct {
	ID               string   `json:"id"`
	Status           string   `json:"status"`
	Summary          string   `json:"summary"`
	HTMLLink         string   `json:"htmlLink"`
	Start            apiTime  `json:"start"`
	End              apiTime  `json:"end"`
	RecurringEventID string   `json:"recurringEventId"`
	Recurrence       []string `json:"recurrence"`
	Visibility       string   `json:"visibility"`
	EventType        string   `json:"eventType"`
	HangoutLink      string   `json:"hangoutLink"`
	Location         string   `json:"location"`
	Description      string   `json:"description"`
	Attendees        []struct {
		Self           bool   `json:"self"`
		Resource       bool   `json:"resource"`
		ResponseStatus string `json:"responseStatus"`
	} `json:"attendees"`
	ConferenceData struct {
		EntryPoints []struct {
			URI string `json:"uri"`
		} `json:"entryPoints"`
	} `json:"conferenceData"`
}

// Override is a display decision the user made for one event: the day it
// should appear on, and marks such as a pick-up time.
type Override struct {
	ID             string `json:"id"`
	DisplayBasis   string `json:"display_basis"`
	DisplayMarkers []any  `json:"display_markers"`
}

// Source reads one calendar and returns it in the snapshot's shape. The
// description, the attendees and the meeting links are used only here, to
// decide whether an event is a meeting, and are not stored.
//
// reader is the account whose grant is used. It is usually the calendar's
// own account, but a calendar shared with another connected account can be
// read through that one.
func (c *Client) Source(ctx context.Context, cred Credential, reader string, entry calendar.ScopeEntry, label string, from, to, now time.Time, overrides map[string]Override) (map[string]any, error) {
	access, err := c.Access(ctx, cred)
	if err != nil {
		return nil, err
	}
	owner, err := c.PrimaryID(ctx, access)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(owner, reader) {
		return nil, fmt.Errorf("Google との連携に使ったアカウントは %s です。予定を読む設定のアカウント %s と一致しません", owner, reader)
	}
	path := "/calendars/" + url.PathEscape(entry.CalendarID) + "/events"
	query := url.Values{"singleEvents": {"true"}, "orderBy": {"startTime"}, "showDeleted": {"false"}, "maxResults": {"250"},
		"timeMin": {from.Format(time.RFC3339)}, "timeMax": {to.Format(time.RFC3339)}, "timeZone": {from.Location().String()}}
	var all []apiEvent
	seenTokens := map[string]bool{}
	var role string
	for {
		var page struct {
			Items         []apiEvent `json:"items"`
			NextPageToken string     `json:"nextPageToken"`
			AccessRole    string     `json:"accessRole"`
		}
		if err := c.get(ctx, access, path, query, &page); err != nil {
			return nil, calendarAccessError(err)
		}
		if err := checkCalendarRole(page.AccessRole); err != nil {
			return nil, err
		}
		if role != "" && page.AccessRole != role {
			return nil, errCalendarAccess
		}
		role = page.AccessRole
		all = append(all, page.Items...)
		if page.NextPageToken == "" {
			break
		}
		if seenTokens[page.NextPageToken] {
			return nil, errors.New("カレンダーの次のページを確かめられませんでした")
		}
		seenTokens[page.NextPageToken] = true
		query.Set("pageToken", page.NextPageToken)
	}
	// Instances do not carry the rule of their series; the week view needs
	// it to tell a weekly routine from a rarer event.
	rules := map[string][]string{}
	events := []map[string]any{}
	observed := now.UTC().Format(time.RFC3339)
	for _, e := range all {
		if e.Status == "cancelled" || e.EventType == "workingLocation" {
			continue
		}
		start, end, timed, ok := times(e, from.Location())
		if !ok || e.ID == "" {
			return nil, errors.New("予定のIDか日時を確かめられませんでした")
		}
		out := map[string]any{"id": e.ID, "title": e.Summary, "status": e.Status, "start": start, "end": end}
		if e.HTMLLink != "" {
			out["html_link"] = e.HTMLLink
		}
		if e.Visibility == "private" || e.Visibility == "confidential" {
			out["visibility"] = "private"
		}
		if e.RecurringEventID != "" {
			out["is_recurring"] = true
			rule, seen := rules[e.RecurringEventID]
			if !seen {
				var master apiEvent
				if err := c.get(ctx, access, path+"/"+url.PathEscape(e.RecurringEventID), nil, &master); err == nil {
					rule = master.Recurrence
				}
				rules[e.RecurringEventID] = rule
			}
			if len(rule) > 0 {
				out["recurrence"] = rule
			}
		}
		response, others := "", false
		for _, a := range e.Attendees {
			if a.Self {
				response = a.ResponseStatus
			} else if !a.Resource {
				others = true
			}
		}
		if response != "" {
			out["self_response"] = response
		}
		out["meeting_candidate"] = meetingEvidence(e, timed, response, others)
		out["meeting_candidate_basis"] = "source_observation"
		out["meeting_candidate_observed_at"] = observed
		if o, ok := overrides[e.ID]; ok && !timed {
			if o.DisplayBasis != "" {
				out["display_basis"] = o.DisplayBasis
			}
			if len(o.DisplayMarkers) > 0 {
				out["display_markers"] = o.DisplayMarkers
			}
		}
		events = append(events, out)
	}
	return map[string]any{
		"account": entry.Account, "calendar_id": entry.CalendarID, "label": label, "status": "ok", "complete": true,
		"updated_at": observed, "error": nil, "scope_verified": true,
		"scope_basis": reader + " のアカウントから許可を受け、Google Calendar API にカレンダーのIDを指定して予定を読み取りました。",
		"events":      events,
	}, nil
}

// times returns start and end in the snapshot's form: {"date": ...} for an
// all-day event, {"dateTime": ...} with an offset otherwise.
func times(e apiEvent, zones ...*time.Location) (start, end map[string]any, timed, ok bool) {
	loc := task.JST
	if len(zones) > 0 && zones[0] != nil {
		loc = zones[0]
	}
	switch {
	case e.Start.Date != "" && e.End.Date != "":
		s, errStart := time.Parse(time.DateOnly, e.Start.Date)
		f, errEnd := time.Parse(time.DateOnly, e.End.Date)
		if errStart != nil || errEnd != nil || !f.After(s) {
			return nil, nil, false, false
		}
		return map[string]any{"date": e.Start.Date}, map[string]any{"date": e.End.Date}, false, true
	case e.Start.DateTime != "" && e.End.DateTime != "":
		s, err1 := time.Parse(time.RFC3339, e.Start.DateTime)
		f, err2 := time.Parse(time.RFC3339, e.End.DateTime)
		if err1 != nil || err2 != nil || !f.After(s) {
			return nil, nil, false, false
		}
		return map[string]any{"dateTime": s.In(loc).Format(time.RFC3339)}, map[string]any{"dateTime": f.In(loc).Format(time.RFC3339)}, true, true
	}
	return nil, nil, false, false
}

// meetingEvidence never treats an unanswered invitation as a rejection.
// Missing meeting metadata is unknown, not evidence that a meeting ended.
func meetingEvidence(e apiEvent, timed bool, response string, others bool) any {
	if !timed || response == "declined" || e.Status == "cancelled" || (e.EventType != "" && e.EventType != "default") {
		return false
	}
	if others || conference(e) {
		return true
	}
	return nil
}

var meetingURL = regexp.MustCompile(`https?://[^\s<>"']+`)

func conference(e apiEvent) bool {
	links := []string{e.HangoutLink}
	links = append(links, meetingURL.FindAllString(html.UnescapeString(e.Location+"\n"+e.Description), -1)...)
	for _, p := range e.ConferenceData.EntryPoints {
		links = append(links, p.URI)
	}
	for _, link := range links {
		u, err := url.Parse(strings.TrimSpace(link))
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			continue
		}
		host := strings.ToLower(u.Hostname())
		for _, known := range []string{"meet.google.com", "zoom.us", "teams.microsoft.com"} {
			if host == known || strings.HasSuffix(host, "."+known) {
				return true
			}
		}
	}
	return false
}

func checkCalendarRole(role string) error {
	switch role {
	case "reader", "writerWithoutPrivateAccess", "writer", "owner":
		return nil
	case "none", "freeBusyReader":
		return errCalendarAccess
	default:
		return errors.New("カレンダーの閲覧権限を確かめられませんでした")
	}
}

// readAccess checks one calendar rather than relying on a grant for its account.
func (c *Client) readAccess(ctx context.Context, cred Credential, calendarID string) error {
	access, err := c.Access(ctx, cred)
	if err != nil {
		return err
	}
	var out struct {
		AccessRole string `json:"accessRole"`
	}
	if err := c.get(ctx, access, "/calendars/"+url.PathEscape(calendarID)+"/events", url.Values{"maxResults": {"1"}}, &out); err != nil {
		return calendarAccessError(err)
	}
	return checkCalendarRole(out.AccessRole)
}

// Accounts lists the connected accounts.
func (s *Store) Accounts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, _ := s.load()
	var out []string
	for account, c := range all {
		if c.RefreshToken != "" {
			out = append(out, account)
		}
	}
	sort.Strings(out)
	return out
}

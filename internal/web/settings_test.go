package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/gcal"
)

func TestCalendarConnection(t *testing.T) {
	owner := calendar.Scope[0].Account
	google := http.NewServeMux()
	google.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_secret") != "s3cret" || r.Form.Get("code") != "the-code" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "at", "refresh_token": "rt"})
	})
	google.HandleFunc("GET /api/calendars/primary", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": owner})
	})
	fake := httptest.NewServer(google)
	defer fake.Close()

	store, _ := setup(t)
	creds := credentials(t)
	connected := 0
	h, err := (&Server{Store: store, Calendars: &Calendars{Store: creds, Site: "https://lacia.example",
		Client:    &gcal.Client{Endpoints: gcal.Endpoints{Auth: fake.URL + "/auth", Token: fake.URL + "/token", API: fake.URL + "/api"}},
		Connected: func() { connected++ }}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	if code, body, _ := get(t, h, "/settings/calendar"); code != 200 || !strings.Contains(body, "https://lacia.example/oauth/google/callback") || !strings.Contains(body, "未連携") {
		t.Fatalf("settings page: %d", code)
	}

	for _, check := range []struct{ account, message string }{
		{"unknown@example.test", "このアカウントは連携の対象にありません"},
		{owner, "クライアントIDとクライアントシークレットを入力してください"},
	} {
		rec := post(h, "/settings/calendar/connect", "", url.Values{"account": {check.account}})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), check.message) {
			t.Errorf("invalid calendar connection for %s: %d %s", check.account, rec.Code, rec.Body)
		}
	}

	start := func(account string) url.Values {
		rec := post(h, "/settings/calendar/connect", "", url.Values{"account": {account}, "client_id": {"cid"}, "client_secret": {"s3cret"}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("connect: %d %s", rec.Code, rec.Body.String())
		}
		to, _ := url.Parse(rec.Header().Get("Location"))
		q := to.Query()
		if !strings.HasPrefix(to.String(), fake.URL+"/auth?") || q.Get("redirect_uri") != "https://lacia.example/oauth/google/callback" ||
			q.Get("scope") != gcal.Scope || q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" || q.Get("login_hint") != account || strings.Contains(to.String(), "s3cret") {
			t.Errorf("consent URL = %s", to)
		}
		return q
	}
	callback := func(query string) string {
		code, _, header := get(t, h, "/oauth/google/callback?"+query)
		if code != http.StatusSeeOther {
			t.Errorf("callback: %d", code)
		}
		to, _ := url.Parse(header.Get("Location"))
		return to.Query().Get("flash")
	}

	// Granted with the other account: nothing is stored.
	q := start(calendar.Scope[1].Account)
	if msg := callback("code=the-code&state=" + q.Get("state")); !strings.Contains(msg, "Googleで許可したアカウントは "+owner) {
		t.Errorf("wrong account: %q", msg)
	}
	if _, ok := creds.Get(calendar.Scope[1].Account); ok || connected != 0 {
		t.Error("a grant from the wrong account was stored")
	}
	// A state that was never issued, and a state used twice.
	if msg := callback("code=the-code&state=forged"); !strings.Contains(msg, "有効期限") {
		t.Errorf("forged state: %q", msg)
	}
	q = start(owner)
	if msg := callback("code=the-code&state=" + q.Get("state")); !strings.Contains(msg, "読めるようになりました") {
		t.Errorf("grant: %q", msg)
	}
	if msg := callback("code=the-code&state=" + q.Get("state")); !strings.Contains(msg, "有効期限") {
		t.Errorf("replayed state: %q", msg)
	}
	cred, ok := creds.Get(owner)
	if !ok || cred.RefreshToken != "rt" || cred.ClientID != "cid" || connected != 1 {
		t.Errorf("stored credential ok=%v connected=%d", ok, connected)
	}
	_, body, _ := get(t, h, "/settings/calendar")
	if !strings.Contains(body, "許可した日時 "+cred.Connected) {
		t.Error("settings page did not show the time permission was granted")
	}
	if !strings.Contains(body, "連携済み") || strings.Contains(body, "s3cret") || strings.Contains(body, `"rt"`) || strings.Contains(body, "cid") {
		t.Error("settings page after connecting is wrong, or shows a secret")
	}
}

// A Lacia on one's own machine: the client comes from the settings, Google
// sends the user back to the loopback address, and the code is bound to this
// server with PKCE.
func TestCalendarConnectionWithADesktopClient(t *testing.T) {
	owner := calendar.Scope[0].Account
	challenge := ""
	google := http.NewServeMux()
	google.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("client_id") != "desktop-id" || r.Form.Get("client_secret") != "desktop-secret" ||
			r.Form.Get("redirect_uri") != "http://127.0.0.1:18300/oauth/google/callback" ||
			challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "at", "refresh_token": "rt"})
	})
	google.HandleFunc("GET /api/calendars/primary", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": owner})
	})
	fake := httptest.NewServer(google)
	defer fake.Close()

	store, _ := setup(t)
	creds := credentials(t)
	h, err := (&Server{Store: store, Calendars: &Calendars{Store: creds, Local: "http://127.0.0.1:18300",
		ClientID: "desktop-id", ClientSecret: "desktop-secret", Write: true,
		Client: &gcal.Client{Endpoints: gcal.Endpoints{Auth: fake.URL + "/auth", Token: fake.URL + "/token", API: fake.URL + "/api"}}}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	// The page asks for no client, and never shows the secret.
	code, body, _ := get(t, h, "/settings/calendar")
	if code != 200 || strings.Contains(body, `name="client_secret"`) || strings.Contains(body, "desktop-secret") ||
		!strings.Contains(body, "http://127.0.0.1:18300/oauth/google/callback") || !strings.Contains(body, "予定を作成したり変更したりする許可") {
		t.Fatalf("settings page: %d", code)
	}
	rec := post(h, "/settings/calendar/connect", "", url.Values{"account": {owner}})
	to, _ := url.Parse(rec.Header().Get("Location"))
	q := to.Query()
	challenge = q.Get("code_challenge")
	if rec.Code != http.StatusSeeOther || q.Get("client_id") != "desktop-id" || q.Get("code_challenge_method") != "S256" || len(challenge) < 40 ||
		q.Get("redirect_uri") != "http://127.0.0.1:18300/oauth/google/callback" || q.Get("scope") != gcal.Scope+" "+gcal.ScopeEvents ||
		strings.Contains(to.String(), "desktop-secret") {
		t.Fatalf("consent URL = %s", to)
	}
	_, _, header := get(t, h, "/oauth/google/callback?code=the-code&state="+q.Get("state"))
	back, _ := url.Parse(header.Get("Location"))
	if msg := back.Query().Get("flash"); !strings.Contains(msg, "読めるようになりました") {
		t.Fatalf("callback: %q", msg)
	}
	if cred, ok := creds.Get(owner); !ok || cred.RefreshToken != "rt" || cred.ClientID != "desktop-id" {
		t.Fatalf("stored credential: %+v %v", cred.ClientID, ok)
	}
}

// credentials keeps the Google credentials in a new state directory.
func credentials(t *testing.T) *gcal.Store {
	t.Helper()
	state, err := disk.OpenTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	return &gcal.Store{Dir: state, Name: "gcal.json"}
}

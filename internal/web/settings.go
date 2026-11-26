package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/gcal"
)

// Calendars is the direct connection to Google Calendar. With it set, the
// settings page lets the user grant read access; the secrets go from the
// user's browser straight into the credentials file and never onto a page.
type Calendars struct {
	Refresh func(context.Context) error
	Store   *gcal.Store
	Client  *gcal.Client
	Site    string // public base URL, for the address Google sends the user back to
	// Local is the server's own loopback address, e.g. http://127.0.0.1:10148.
	// Without a public address the user is sent back there, as a client of
	// the "desktop app" kind requires.
	Local string
	// ClientID and ClientSecret are the client from the settings. With them,
	// the page asks for nothing but the grant.
	ClientID, ClientSecret string
	// Write also asks for the permission to create and change events.
	Write bool
	// Connected is called after an account was connected or disconnected.
	Connected func()
	// Readers says which connected account reads each configured calendar.
	Readers func() map[[2]string]string

	mu      sync.Mutex
	pending map[string]pendingGrant
}

type pendingGrant struct {
	account, clientID, clientSecret string
	verifier                        string // PKCE; never leaves the server
	expires                         time.Time
}

func (c *Calendars) redirect() string {
	base := c.Site
	if base == "" {
		base = c.Local
	}
	return strings.TrimRight(base, "/") + "/oauth/google/callback"
}

func (c *Calendars) scope() string {
	if c.Write {
		return gcal.Scope + " " + gcal.ScopeEvents
	}
	return gcal.Scope
}

type calendarRow struct {
	Account   string
	Label     string
	Connected string // when access was granted; empty if not connected
	HasClient bool   // a client ID is already saved, so the form may be left empty
	Via       string // another connected account that reads this calendar
}

type settingsData struct {
	Rows     []calendarRow
	Redirect string
	Flash    string
	Ready    bool
	Default  bool // the settings carry a client; the form asks for none
	Write    bool
}

func (s *Server) calendarSettings(w http.ResponseWriter, r *http.Request) {
	c := s.Calendars
	data := settingsData{Redirect: c.redirect(), Flash: r.URL.Query().Get("flash"), Ready: true, Default: c.ClientID != "", Write: c.Write}
	readers := map[[2]string]string{}
	if c.Readers != nil {
		readers = c.Readers()
	}
	for _, entry := range calendar.Scope {
		row := calendarRow{Account: entry.Account, Label: entry.Label}
		via := readers[[2]string{entry.Account, entry.CalendarID}]
		if cred, ok := c.Store.Get(entry.Account); ok && (via == entry.Account || c.Readers == nil) {
			row.Connected, row.HasClient = cred.Connected, true
		} else if via != "" {
			row.Via = via
		} else {
			data.Ready = false
		}
		data.Rows = append(data.Rows, row)
	}
	data.Ready = data.Ready && len(data.Rows) > 0
	s.render(w, http.StatusOK, "settings", "予定の連携", data)
}

func (s *Server) calendarConnect(w http.ResponseWriter, r *http.Request) {
	c := s.Calendars
	account := r.FormValue("account")
	known := false
	for _, entry := range calendar.Scope {
		known = known || entry.Account == account
	}
	clientID, clientSecret := strings.TrimSpace(r.FormValue("client_id")), strings.TrimSpace(r.FormValue("client_secret"))
	if clientID == "" && clientSecret == "" {
		// The client from the settings comes first, so that a grant can be
		// renewed with a new client by changing the settings alone.
		if c.ClientID != "" {
			clientID, clientSecret = c.ClientID, c.ClientSecret
		} else if cred, ok := c.Store.Get(account); ok {
			clientID, clientSecret = cred.ClientID, cred.ClientSecret
		}
	}
	if !known || clientID == "" || clientSecret == "" {
		s.fail(w, http.StatusBadRequest, "クライアントIDとクライアントシークレットを入力してください。")
		return
	}
	buf := make([]byte, 24)
	rand.Read(buf)
	state := hex.EncodeToString(buf)
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]pendingGrant{}
	}
	for k, p := range c.pending {
		if time.Now().After(p.expires) {
			delete(c.pending, k)
		}
	}
	verifier, challenge := gcal.Challenge()
	c.pending[state] = pendingGrant{account, clientID, clientSecret, verifier, time.Now().Add(10 * time.Minute)}
	c.mu.Unlock()
	http.Redirect(w, r, c.Client.AuthURL(clientID, c.redirect(), state, account, challenge, c.scope()), http.StatusSeeOther)
}

func (s *Server) calendarCallback(w http.ResponseWriter, r *http.Request) {
	c := s.Calendars
	q := r.URL.Query()
	c.mu.Lock()
	grant, ok := c.pending[q.Get("state")]
	delete(c.pending, q.Get("state"))
	c.mu.Unlock()
	back := func(message string) {
		http.Redirect(w, r, "/settings/calendar?flash="+url.QueryEscape(message), http.StatusSeeOther)
	}
	switch {
	case !ok || time.Now().After(grant.expires):
		back("許可の手続きが見つからないか、有効期限が切れています。「予定の連携」からもう1度やり直してください。")
		return
	case q.Get("error") != "":
		back("Googleでの許可が完了しませんでした（" + q.Get("error") + "）。")
		return
	}
	cred, access, err := c.Client.Exchange(r.Context(), grant.clientID, grant.clientSecret, c.redirect(), q.Get("code"), grant.verifier)
	if err != nil {
		back(err.Error())
		return
	}
	// A grant made while signed in to another account must not be stored
	// under this one.
	owner, err := c.Client.PrimaryID(r.Context(), access)
	if err != nil {
		back("許可のあと、アカウントを確かめられませんでした。" + err.Error())
		return
	}
	if !strings.EqualFold(owner, grant.account) {
		back("Googleで許可したアカウントは " + owner + " です。" + grant.account + " でログインして、もう1度許可してください。")
		return
	}
	cred.Connected = time.Now().In(s.Store.Location()).Format("2006-01-02 15:04")
	if err := c.Store.Put(grant.account, cred); err != nil {
		back("許可の情報を保存できませんでした。" + err.Error())
		return
	}
	if c.Connected != nil {
		c.Connected()
	}
	back(grant.account + " の予定を読めるようになりました。")
}

func (s *Server) calendarDisconnect(w http.ResponseWriter, r *http.Request) {
	c := s.Calendars
	if err := c.Store.Remove(r.FormValue("account")); err != nil {
		s.fail(w, http.StatusInternalServerError, "連携を外せませんでした。")
		return
	}
	if c.Connected != nil {
		c.Connected()
	}
	http.Redirect(w, r, "/settings/calendar?flash="+url.QueryEscape(r.FormValue("account")+" の連携を外しました。"), http.StatusSeeOther)
}

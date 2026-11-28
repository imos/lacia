// Package web serves the Lacia pages. Every page is rendered on the server
// from the files on disk; there is no client-side data loading.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

type Server struct {
	Store   *task.Store
	Version string // commit the binary was built from
	// RestartWhenIdle keeps a request to stop once the work is done. Nil
	// when nothing would watch for it.
	RestartWhenIdle func() error
	// PublishedRevision is the last revision available from the task Git remote.
	PublishedRevision func(context.Context) string
	// Label is the display name of this profile, used where a text says
	// which Lacia wrote it. PeerLabel names the
	// profile it exchanges messages with. Both come from the settings.
	Label, PeerLabel string
	// People is what the texts call those the peer profile talks with
	// besides the owner, such as "ご家族".
	People string
	// PeerURL is where the peer profile's server is reached. The handoff
	// command asks for it.
	PeerURL string
	// Site is the configured browser-facing address. If set, loopback
	// callers and requests for that host can reach the pages and APIs.
	Site string
	// DoorOnly limits non-loopback callers to the peer endpoints even
	// without Site. Profiles with pages disabled use this setting.
	DoorOnly bool
	// Peer is the link to the chat side. Nil on the chat side's server itself and
	// wherever the chat side cannot be reached.
	Peer *Peer
	// Members is set where Discord is listened to.
	Members     MemberControl
	Attachments AttachmentSender
	// Inbox makes this server accept messages from the task side.
	Inbox *Inbox
	// Self is Lacia's own view of things, rewritten every night. Nil hides
	// the page and the commands for it.
	Self *self.Keeper
	Now  func() time.Time // replaceable in tests
	// Requests is the queue of messages for Codex. Nil hides the request
	// forms: the pages then only show and change tasks.
	Requests *request.Queue
	acceptMu sync.Mutex
	// Calendars enables the settings page for the direct calendar connection.
	Calendars *Calendars
	// Schedules returns the scheduled jobs and when each last ran.
	Schedules func() ([]schedule.Job, map[string]string)
	// Routines preserves the acceptance and review history.
	Routines *routine.Store
	// RoutineActions displays owner actions from routine tasks. While disabled,
	// the legacy receipt review interface remains available.
	RoutineActions bool
	// RoutineActionsEnabled optionally reads the rollout state at request time.
	RoutineActionsEnabled func() bool
	routineKey            []byte
	// Runs knows the scheduled runs in progress.
	Runs             *schedule.RunLog
	ScheduleDispatch *schedule.Dispatch
	// CalendarPath is the calendar snapshot file. Empty hides the calendar.
	CalendarPath         string
	HideCalendarHolidays bool
	// Sessions remembers which conversations ran the lacia command. Handler
	// keeps them in memory when it is nil.
	Sessions *caller.Registry
	// Changed is called after the server wrote to the repository.
	Changed func()
	// flood stops a source that keeps sending the same write to one task.
	flood floodGuard
	// Waits holds what Lacia waits for outside a conversation, such as
	// cluster jobs. Nil turns the commands for it off.
	Waits              *wait.Store
	Handovers          *handover.Store
	Place              string
	ResolveDestination func(string) (caller.Destination, error)
	AuthorizeHandover  func(place, token string) bool
	handoverMu         sync.Mutex
	waitMu             sync.Mutex
	// Notice returns a problem to show on every page, such as a failed push.
	Notice func() string
	// Machine returns what to fix on the machine the server runs on, for
	// the home page. Nil shows nothing.
	Machine func() []MachineIssue

	pages    map[string]*template.Template
	calendar *calendar.Reader
	calTmpl  *template.Template
}

// label and peerLabel are the names of this profile and of the one it talks
// to, as the texts name them.
func (s *Server) label() string {
	if s.Label != "" {
		return s.Label
	}
	return self.DefaultLabel
}

func (s *Server) peerLabel() string {
	if s.PeerLabel != "" {
		return s.PeerLabel
	}
	return self.DefaultPeerLabel
}

func (s *Server) people() string {
	if s.People != "" {
		return s.People
	}
	return self.DefaultPeople
}

type page struct {
	Title    string
	Version  string
	Notice   string
	HasPeer  bool // the link to the chat side is set up
	HasSelf  bool // the page of her own view is set up
	Tasks    bool // the repository keeps tasks; without them, their links go
	Problems []task.Problem
	Data     any
}

func (s *Server) routineActions() bool {
	if s.RoutineActionsEnabled != nil {
		return s.RoutineActionsEnabled()
	}
	return s.RoutineActions
}

func (s *Server) render(w http.ResponseWriter, status int, name, title string, data any) {
	p := page{Title: title, Version: s.Version, HasPeer: s.Peer != nil, HasSelf: s.Self != nil, Tasks: s.Store.KeepsTasks(), Problems: s.Store.Problems(), Data: data}
	if s.Notice != nil {
		p.Notice = s.Notice()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages[name].Execute(w, p); err != nil {
		slog.Error("template", "page", name, "error", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, status int, message string) {
	s.render(w, status, "error", "エラー", message)
}

func (s *Server) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

const invalidJSONMessage = "JSONを読み取れませんでした。形式とデータの大きさを確かめてください"

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

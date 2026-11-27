// Package web serves the Lacia pages. Every page is rendered on the server
// from the files on disk; there is no client-side data loading.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/project"
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

// Server holds what the handlers need.
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

// Handler builds the routes.
func (s *Server) Handler() (http.Handler, error) {
	if s.Now == nil {
		s.Now = time.Now
	}
	s.Store.SetLiveCondition(s.liveCondition)
	css, err := staticFiles.ReadFile("static/app.css")
	if err != nil {
		return nil, err
	}
	cssHash := sha256.Sum256(css)
	cssURL := "/static/app.css?v=" + hex.EncodeToString(cssHash[:8])
	funcs := template.FuncMap{
		"appCSS":        func() string { return cssURL },
		"areaLabel":     s.Store.AreaLabel,
		"rechecks":      recheckChoices,
		"label":         s.label,
		"peerLabel":     s.peerLabel,
		"people":        s.people,
		"markdown":      func(text string) template.HTML { return renderMarkdown(text, "") },
		"selfMarkdown":  selfMarkdown,
		"brief":         func(text string) string { return summary(text, 90) },
		"clock":         func(t time.Time) string { return t.In(s.Store.Location()).Format("1月2日 15:04") },
		"dateTime":      func(t time.Time) string { return task.FormatWhenIn(t, s.Store.Location()) },
		"taskURL":       func(id string) string { return "/tasks/" + id },
		"routineURL":    schedule.RoutineURL,
		"receiptURL":    routine.ReceiptURL,
		"receiptBrief":  receiptBrief,
		"taskListLabel": taskListLabel,
		"projectURL":    func(p project.Project) string { return "/projects/" + p.Area + "/" + p.Slug },
		"projectStatus": project.StatusLabel,
		"add":           func(a, b int) int { return a + b },
		"size":          task.HumanSize,
		"owner":         func() string { return task.OwnerName },
	}
	if s.Sessions == nil {
		s.Sessions, _ = caller.OpenRegistry("")
		s.Sessions.Now = func() time.Time { return s.Now() }
	}
	if s.Routines == nil {
		s.Routines = routine.NewStore(s.Store.Root())
	}
	if err := s.initRoutineKey(); err != nil {
		return nil, err
	}
	s.pages = map[string]*template.Template{}
	for _, name := range []string{"home", "tasks", "task", "file", "error", "requests", "schedule", "routines", "routine", "routine-receipt", "settings", "projects", "project", "peer", "self", "who"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFiles, "templates/layout.html", "templates/task-conditions.html", "templates/task-parts.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		s.pages[name] = t
	}
	static, _ := fs.Sub(staticFiles, "static")
	mux := http.NewServeMux()
	if s.CalendarPath != "" {
		t, err := calendar.AddTemplate(template.New("calendar"))
		if err != nil {
			return nil, err
		}
		s.calendar, s.calTmpl = calendar.NewReader(s.CalendarPath), t
		s.calendar.Location = s.Store.Location()
		s.calendar.HideHolidays = s.HideCalendarHolidays
		asset := func(contentType, body string) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				fmt.Fprint(w, body)
			}
		}
		mux.HandleFunc("GET /calendar.css", asset("text/css; charset=utf-8", calendar.CSS))
		mux.HandleFunc("GET /calendar.js", asset("text/javascript; charset=utf-8", calendar.JS))
	}
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /tasks", s.tasks)
	mux.HandleFunc("POST /tasks", s.create)
	mux.HandleFunc("GET /tasks/{id}", s.task)
	mux.HandleFunc("POST /tasks/{id}/status", s.setStatus)
	mux.HandleFunc("POST /tasks/{id}/schedule", s.setSchedule)
	mux.HandleFunc("POST /tasks/{id}/after", s.setAfter)
	mux.HandleFunc("POST /tasks/{id}/after-run", s.setAfterRun)
	mux.HandleFunc("POST /tasks/{id}/on-ready", s.setOnReady)
	mux.HandleFunc("POST /tasks/{id}/recheck", s.setRecheck)
	mux.HandleFunc("POST /tasks/{id}/upload", s.upload)
	mux.HandleFunc("POST /tasks/{id}/stored", s.changeStored)
	mux.HandleFunc("PUT /api/tasks/{id}/files", s.apiPutFile)
	mux.HandleFunc("PUT /api/tasks/{id}/doc", s.apiWriteDoc)
	mux.HandleFunc("POST /tasks/{id}/start", s.start)
	mux.HandleFunc("GET /projects", s.projects)
	mux.HandleFunc("GET /projects/{area}/{slug}", s.project)
	mux.HandleFunc("POST /projects/{area}/{slug}/move", s.moveInProject)
	mux.HandleFunc("GET /tasks/{id}/files/{path...}", s.taskFile)
	mux.HandleFunc("GET /tasks/{id}/log", s.taskLog)
	mux.HandleFunc("GET /projects/{area}/{slug}/log", s.projectLog)
	mux.HandleFunc("GET /files/{path...}", s.repoFile)
	mux.HandleFunc("GET /design/{$}", s.designDocument)
	mux.HandleFunc("GET /design/{name}", s.designDocument)
	mux.HandleFunc("GET /requests", s.requests)
	mux.HandleFunc("GET /requests/{id}", s.requestDetail)
	mux.HandleFunc("POST /requests/{id}/resume", s.resumeRequest)
	mux.HandleFunc("POST /requests", s.ask)
	mux.HandleFunc("GET /api/schedule-runs", s.scheduleExecutions)
	mux.HandleFunc("GET /api/schedule-troubles", s.apiScheduleTroubles)
	mux.HandleFunc("POST /api/routines/{id}/runs/{run}", s.reportRoutineRun)
	mux.HandleFunc("GET /api/routine-notices", s.routineNotices)
	mux.HandleFunc("POST /api/routine-notices/{id}/{sequence}", s.routineNoticeAcknowledged)
	mux.HandleFunc("POST /api/schedule-worker/{device}/{action}", s.scheduleWorker)
	mux.HandleFunc("POST /api/schedule-worker/{device}/{run}/{action}", s.scheduleWorker)
	mux.HandleFunc("GET /routines", s.routines)
	mux.HandleFunc("GET /routines/{id}", s.routine)
	mux.HandleFunc("GET /routines/{id}/receipts/{sequence}", s.routineReceipt)
	mux.HandleFunc("POST /routines/{id}/status", s.setRoutineStatus)
	mux.HandleFunc("GET /routines/{id}/files/{path...}", s.routineFile)
	mux.HandleFunc("POST /routines/{id}/receipts/{sequence}/complete", s.completeRoutine)
	mux.HandleFunc("POST /routines/{id}/complete", s.completeRoutine)
	mux.HandleFunc("GET /schedules", redirectLegacySchedule)
	mux.HandleFunc("GET /schedules/{name}", s.redirectScheduleDetail)
	mux.HandleFunc("GET /who", s.who)
	mux.HandleFunc("GET /api/sessions", s.apiSessions)
	mux.HandleFunc("GET /api/busy", s.apiBusy)
	mux.HandleFunc("POST /api/restart", s.apiRestart)
	// Calendars may be set after the routes are built, so decide per request.
	withCalendars := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.Calendars == nil {
				s.fail(w, http.StatusNotFound, "予定の連携は、このサーバーでは有効になっていません。")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /settings/calendar", withCalendars(s.calendarSettings))
	mux.HandleFunc("POST /settings/calendar/connect", withCalendars(s.calendarConnect))
	mux.HandleFunc("POST /settings/calendar/disconnect", withCalendars(s.calendarDisconnect))
	mux.HandleFunc("GET /oauth/google/callback", withCalendars(s.calendarCallback))
	mux.HandleFunc("POST /api/requests", s.apiAsk)
	mux.HandleFunc("GET /api/calendar/snapshot", s.apiSnapshot)
	mux.HandleFunc("GET /api/calendar/list", s.apiCalendarList)
	mux.HandleFunc("POST /api/calendar/refresh", func(w http.ResponseWriter, r *http.Request) {
		if s.Calendars == nil || s.Calendars.Refresh == nil {
			http.Error(w, "予定の直接取得を利用できません", http.StatusServiceUnavailable)
			return
		}
		if err := s.Calendars.Refresh(r.Context()); err != nil {
			http.Error(w, "一部の予定を確認できませんでした", http.StatusServiceUnavailable)
			return
		}
		s.apiSnapshot(w, r)
	})
	mux.HandleFunc("GET /api/calendar/scope", s.apiCalendarScope)
	mux.HandleFunc("GET /api/tasks", s.apiTasks)
	mux.HandleFunc("GET /api/tasks/waiting-warnings", s.apiWaitingWarnings)
	mux.HandleFunc("POST /api/tasks", s.apiCreate)
	mux.HandleFunc("GET /api/tasks/{id}", s.apiTask)
	mux.HandleFunc("POST /api/tasks/{id}", s.apiUpdate)
	mux.HandleFunc("POST /api/projects/notes", s.apiProjectNote)
	mux.HandleFunc("GET /api/requests/{id}", s.apiRequest)
	if s.Waits != nil {
		mux.HandleFunc("GET /api/waits", s.apiWaits)
		mux.HandleFunc("GET /api/wait-destination", s.apiWaitDestination)
		mux.HandleFunc("POST /api/waits", s.apiAddWait)
		mux.HandleFunc("GET /api/waits/{id}", s.apiWait)
		mux.HandleFunc("POST /api/waits/{id}", s.apiUpdateWait)
		mux.HandleFunc("PATCH /api/waits/{id}", s.apiUpdateWait)
	}
	if s.Handovers != nil {
		mux.HandleFunc("GET /api/handovers", s.apiHandovers)
		mux.HandleFunc("POST /api/handovers", s.apiAddHandover)
		mux.HandleFunc("GET /api/handovers/{id}", s.apiHandover)
		mux.HandleFunc("POST /api/handovers/{id}", s.apiUpdateHandover)
		mux.HandleFunc("PATCH /api/handovers/{id}", s.apiUpdateHandover)
	}
	mux.HandleFunc("POST /api/handoffs", s.apiHandoff)
	mux.HandleFunc("GET /api/handoffs/{id}", s.apiHandoffStatus)
	mux.HandleFunc("POST /api/inbox", s.apiInbox)
	mux.HandleFunc("GET /api/inbox/{id}", s.apiInboxStatus)
	mux.HandleFunc("POST /api/replies", s.apiReply)
	mux.HandleFunc("GET /api/peer", s.apiPeer)
	mux.HandleFunc("GET /peer", s.peerPage)
	mux.HandleFunc("POST /peer/messages", s.tellPeer)
	mux.HandleFunc("POST /api/peer/messages", s.apiTellPeer)
	mux.HandleFunc("GET /api/peer/messages/{id}", s.apiToldPeer)
	mux.HandleFunc("GET /self", s.selfPage)
	mux.HandleFunc("GET /self/rejected/{day}", s.selfDraft)
	mux.HandleFunc("POST /self/comments", s.selfComment)
	mux.HandleFunc("GET /api/self", s.apiSelf)
	mux.HandleFunc("GET /api/self/tidy/{day}", s.apiSelfTidy)
	mux.HandleFunc("POST /api/self/notes", s.apiSelfNote)
	mux.HandleFunc("POST /api/self/sleep", s.apiSelfSleep)
	mux.HandleFunc("POST /api/self/dates", s.apiSelfDates)
	mux.HandleFunc("GET /api/self/improvements", s.apiSelfImprovements)
	mux.HandleFunc("POST /api/self/improvements/{id}", s.apiDecideSelfImprovement)
	mux.HandleFunc("GET /api/self/core", s.apiSelfCore)
	mux.HandleFunc("PUT /api/self/core", s.apiSetSelfCore)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /api/discord/members", s.apiMembers)
	mux.HandleFunc("POST /api/discord/attachments", s.apiDiscordAttachment)
	mux.HandleFunc("POST /api/discord/{action}", s.apiMemberPause)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	return s.guard(mux), nil
}

// guard logs every request, turns panics into logged 500s, and refuses
// state-changing requests that did not come from this site's own pages.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "path", r.URL.Path, "error", v)
				http.Error(rec, "内部でエラーが起きました。", http.StatusInternalServerError)
			}
			if r.URL.Path != "/healthz" {
				slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
			}
		}()
		if !openToAll(r) && !s.trusted(r) {
			http.Error(rec, "この入口からは、もう一方のレイシアとのやり取りだけを受け付けます。", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					http.Error(rec, "別のサイトからの操作は受け付けません。", http.StatusForbidden)
					return
				}
			} else if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(rec, "別のサイトからの操作は受け付けません。", http.StatusForbidden)
				return
			}
		}
		rec.Header().Set("X-Content-Type-Options", "nosniff")
		rec.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
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

// Row is one task in a list.
type Row struct {
	RoutineReceiptURL string
	RoutineIssues     []string
	RoutineTaskNext   string // the first paragraph of a routine's task in review
	task.Task
	Next               string
	Routine            bool
	URL                string
	Project            *project.Project // the project the task belongs to, if any
	ParentTask         *task.Task
	HandoverAction     string
	Waits              wait.TaskWaits
	HandoverConditions []taskHandoverCondition
	RequestFinished    bool
	RequestRunning     bool
	Queued             bool
	ListStatus         task.Status
	StatusSince        time.Time
	StatusAge          string
}

func taskListLabel(status task.Status) string {
	if status == task.Waiting {
		return "待機"
	}
	return status.Label()
}

type taskHandoverCondition struct {
	Next string
	Text string
}

func (r Row) MissingCondition() bool {
	return r.Task.MissingWaitingCondition(r.Queued || r.RequestRunning || len(r.Waits) > 0 || len(r.HandoverConditions) > 0)
}

func (r Row) WaitingWarning() string {
	if r.MissingCondition() {
		return task.WaitingConditionWarning
	}
	return ""
}

// liveCondition reports what the server itself watches for a task: a job
// or a file it waits for, or a handover another conversation has yet to
// take. With one, a task may wait without a date or a dependency. A request
// in the queue is not one: it is often the very conversation that is about
// to leave the task waiting.
func (s *Server) liveCondition(id string) bool {
	return len(s.Waits.OpenByTask()[id]) > 0 || len(s.Handovers.ConditionsByTask()[id]) > 0
}

type taskListContext struct {
	now       time.Time
	waits     map[string]wait.TaskWaits
	activity  map[string]string
	handovers map[string][]handover.Handover
}

func (s *Server) taskListContext() taskListContext {
	return taskListContext{
		now: s.Now().In(s.Store.Location()), waits: s.Waits.OpenByTask(),
		activity: s.Requests.TaskActivity(), handovers: s.Handovers.ConditionsByTask(),
	}
}

func (c taskListContext) status(t task.Task) task.Status {
	return t.ListStatus(c.now, c.waits[t.ID].Running(), c.activity[t.ID] == request.Queued)
}

func (c taskListContext) row(t task.Task) Row {
	status := c.status(t)
	r := Row{
		Task: t, Next: summary(t.Next(), 110), ListStatus: status,
		Waits:          c.waits[t.ID],
		Queued:         status == task.Waiting && c.activity[t.ID] == request.Queued,
		RequestRunning: status == task.Waiting && c.activity[t.ID] == request.Running,
	}
	for _, h := range c.handovers[t.ID] {
		r.HandoverConditions = append(r.HandoverConditions, taskHandoverCondition{Next: h.Next, Text: h.ConditionText(c.now)})
	}
	r.RequestFinished = t.Status == task.Doing && t.Owner == "" && t.OwnerSession == "" &&
		c.activity[t.ID] == request.Done && !t.HasReadyConditions() && len(r.Waits) == 0 && len(r.HandoverConditions) == 0
	return r
}

// Block is one area's tasks inside a section.
type Block struct {
	Area string
	Rows []Row
}

// Section is one of the lists on the home page.
type Section struct {
	Key    string
	Title  string
	Hint   string
	Count  int
	Blocks []Block
}

func (s *Server) section(key, title, hint string, tasks []task.Task, list taskListContext) Section {
	sec := Section{Key: key, Title: title, Hint: hint, Count: len(tasks)}
	var handoverActions map[string]string
	if key == "review" && s.Handovers != nil && len(tasks) > 0 {
		handoverActions = make(map[string]string)
		for _, h := range s.Handovers.List(func(h handover.Handover) bool { return h.RequestedAction() != "" }) {
			handoverActions[h.TaskID] = summary(h.RequestedAction(), 110)
		}
	}
	for _, area := range s.Store.Areas() {
		block := Block{Area: area}
		for _, t := range tasks {
			if t.Area == area {
				row := s.homeRow(t, list)
				row.HandoverAction = handoverActions[t.ID]
				block.Rows = append(block.Rows, row)
			}
		}
		if len(block.Rows) == 0 {
			continue
		}
		sortByStatusSince(block.Rows)
		sec.Blocks = append(sec.Blocks, block)
	}
	return sec
}

// homeRow adds the parent as context without changing the task's position.
func (s *Server) homeRow(t task.Task, list taskListContext) Row {
	row := list.row(t)
	s.setStatusAge(&row, list)
	row.Project = s.projectOf(t)
	if t.Parent != "" {
		if parent, err := s.Store.Get(t.Parent); err == nil {
			row.ParentTask = &parent
		}
	}
	return row
}

// projectBlock is a project on the home page: its next task as a row like
// any other, and how many more wait behind it.
type projectBlock struct {
	project.Plan
	Rows []Row
}

type homeData struct {
	Projects       []projectBlock // active projects, each with its next task
	CanAsk         bool
	Latest         []request.Request
	Calendar       template.HTML
	Sections       []Section
	RecentRoutines []routineRecent
	Recent         []task.Task
	Areas          []string
	Flash          string
	Undo           string // a task just closed from its row, which can be taken back
	// Tasks is false in a repository that keeps no tasks: the page then
	// shows recurring work that needs attention.
	Tasks bool
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	list := s.taskListContext()
	now := list.now
	// A task of an active project that has not been started is shown as
	// part of the project, which names the one to start next.
	var active []project.Plan
	inProject := map[string]bool{}
	for _, plan := range s.plans() {
		if plan.Status == project.Active {
			active = append(active, plan)
			inProject[plan.Area+"/"+plan.Slug] = true
		}
	}
	var review, closing, ongoing, ready, later, closed []task.Task
	for _, t := range s.Store.List() {
		switch list.status(t) {
		case task.Done, task.Cancelled:
			closed = append(closed, t)
		case task.Waiting:
			later = append(later, t)
		case task.Review:
			review = append(review, t)
		case task.Closing:
			closing = append(closing, t)
		case task.Todo:
			if !inProject[t.Area+"/"+t.Project] {
				ready = append(ready, t)
			}
		case task.Doing:
			ongoing = append(ongoing, t)
		}
	}
	sort.SliceStable(closed, func(i, j int) bool { return closed[i].Done > closed[j].Done })
	if len(closed) > 8 {
		closed = closed[:8]
	}
	var blocks []projectBlock
	for _, plan := range active {
		block := projectBlock{Plan: plan}
		if plan.Next != nil {
			row := s.homeRow(plan.Next.Task, list)
			row.Project = nil
			block.Rows = []Row{row}
		}
		blocks = append(blocks, block)
	}
	data := homeData{
		Sections: []Section{
			s.section("review", "要対応", task.OwnerName+"の判断、回答、操作を待っています。", review, list),
			s.section("closing", "完了確認待ち", "作業は終わっています。成果を見て、よければ完了にしてください。", closing, list),
			s.section("ongoing", "進行中", "わたしが作業しているタスクと、処理が実行中のタスクです。", ongoing, list),
			s.section("ready", "着手待ち", "まだ始めていないタスクです。進行中のプロジェクトで次に着手するタスクは「プロジェクト」の欄に表示します。", ready, list),
			s.section("later", "待機", "日時やほかのタスクなど、次に進むための条件を待っています。", later, list),
		},
		Projects: blocks,
		Recent:   closed,
		Areas:    s.Store.Areas(),
		Flash:    r.URL.Query().Get("flash"),
		Tasks:    true,
	}
	// Closing a finished task from its row takes one click, so the notice
	// that follows offers to take it back.
	if t, err := s.Store.Get(r.URL.Query().Get("undo")); err == nil && t.Status == task.Done {
		data.Undo = t.ID
	}
	routineActions := s.routineActions()
	if !s.Store.KeepsTasks() {
		// Tasks closed before the repository stopped keeping them stay on
		// the list of all tasks, not here.
		data.Tasks, data.Projects, data.Recent = false, nil, nil
		data.Sections = nil
		if !routineActions {
			data.Sections = []Section{s.section("review", "要対応", "定期タスクの未確認の結果や、実行を妨げている問題です。", nil, list)}
		}
	}
	if routineActions {
		if data.Tasks {
			s.addRoutineTaskReviews(&data.Sections[0])
		}
	} else {
		if err := s.addRoutineReview(&data.Sections[0], s.troubles()); err != nil {
			s.fail(w, http.StatusInternalServerError, "定期タスクの結果を読めませんでした。")
			return
		}
		data.RecentRoutines = s.recentRoutineReviews()
	}
	if s.Requests != nil {
		data.CanAsk = true
		// Show what the user asked and anything Lacia has to tell; notices
		// that ended with nothing to report stay on the full list only.
		for _, req := range s.Requests.Recent(30, "", true) {
			if len(data.Latest) < 4 && (req.Kind == request.User || (req.Reply != "" && !strings.HasPrefix(req.Reply, "報告はありません")) || req.Error != "") {
				data.Latest = append(data.Latest, req)
			}
		}
	}
	if s.calendar != nil {
		// A failed read still yields the last good view; the error is for the log.
		view, err := s.calendar.View(now)
		if err != nil {
			slog.Warn("calendar", "error", err)
		}
		var b strings.Builder
		if err := s.calTmpl.ExecuteTemplate(&b, "calendar", view); err != nil {
			slog.Error("calendar template", "error", err)
			b.Reset()
			b.WriteString(`<p class="notice">予定を表示できませんでした。</p>`)
		}
		data.Calendar = template.HTML(b.String())
	}
	s.render(w, http.StatusOK, "home", "レイシア", data)
}

type tasksData struct {
	Rows     []Row
	Status   string
	Area     string
	Query    string
	Statuses []task.Status
	Areas    []string
	Total    int
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	data := tasksData{Status: q.Get("status"), Area: q.Get("area"), Query: strings.TrimSpace(q.Get("q")), Statuses: task.Statuses, Areas: s.Store.Areas()}
	needle := strings.ToLower(data.Query)
	list := s.taskListContext()
	all := s.Store.List()
	data.Total = len(all)
	for _, t := range all {
		switch {
		case data.Status != "" && string(list.status(t)) != data.Status:
		case data.Area != "" && t.Area != data.Area:
		case needle != "" && !strings.Contains(strings.ToLower(t.ID+" "+t.Title+" "+t.Body), needle):
		default:
			row := list.row(t)
			s.setStatusAge(&row, list)
			data.Rows = append(data.Rows, row)
		}
	}
	sortByStatusSince(data.Rows)
	s.render(w, http.StatusOK, "tasks", "すべてのタスク", data)
}

// Action is a button on the task page.
type Action struct {
	Status  task.Status
	Label   string
	Primary bool
}

func actions(st task.Status) []Action {
	switch st {
	case task.Review, task.Closing:
		return []Action{{task.Done, "完了にする", true}, {task.Doing, "進行中へ戻す", false}, {task.Cancelled, "取りやめる", false}}
	case task.Doing:
		return []Action{{task.Waiting, "待機にする", false}, {task.Review, "確認待ちにする", false}, {task.Done, "完了にする", true}, {task.Cancelled, "取りやめる", false}}
	case task.Waiting:
		return []Action{{task.Doing, "進行中にする", false}, {task.Review, "確認待ちにする", false}, {task.Done, "完了にする", true}, {task.Cancelled, "取りやめる", false}}
	case task.Todo:
		return []Action{{task.Doing, "進行中にする", true}, {task.Done, "完了にする", false}, {task.Cancelled, "取りやめる", false}}
	}
	return []Action{{task.Todo, "やり直す", false}}
}

type taskData struct {
	Plan        *project.Plan  // the project the task belongs to
	Entry       *project.Entry // the task's place in it
	Position    int
	Task        task.Task
	ListStatus  task.Status
	Conditions  Row
	RoutineJobs []schedule.Job
	Owner       *ownerInfo // the conversation working on the task, if known
	Body        template.HTML
	ReviewSince string
	// CanTellPeer shows the form that passes a message on to the chat side.
	CanTellPeer   bool
	Told          []request.Request // what was passed on about this task
	Parent        *task.Task
	Children      []task.Task
	Artifacts     []string
	PastMaterials []pastMaterial
	ArtifactInfo  []artifactInfo
	StoredInfo    []artifactInfo
	History       []historyGroup
	Notes         template.HTML
	Recent        []string // latest entries of the history
	HasLog        bool
	LogName       string // log.md or log.jsonl
	Actions       []Action
	Stored        []task.StoredFile // files/ of the task, which Git does not hold
	StoredSum     string
	Pinned        bool
	ExpiresOn     string
	Flash         string
	CanAsk        bool
	Requests      []request.Request
	Warnings      []task.Warning // shape and wording problems, for whoever edits the task next
	Handovers     []handover.Handover
	Waits         []wait.Wait // what the task waits for outside a conversation
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")
	// Codex edits task files directly, so read this one fresh.
	if err := s.Store.Refresh(ref); err != nil && !errors.Is(err, task.ErrNotFound) {
		slog.Warn("refresh", "task", ref, "error", err)
	}
	t, err := s.Store.Get(ref)
	if err != nil {
		if job, ok := s.oldRoutineID(ref); ok {
			http.Redirect(w, r, schedule.RoutineURL(job.Name)+routineQuery(r)+"#task", http.StatusFound)
			return
		}
		s.fail(w, http.StatusNotFound, ref+" というタスクは見つかりませんでした。")
		return
	}
	// A routine's task is shown on the routine's page, above its receipts.
	if t.Routine {
		http.Redirect(w, r, schedule.RoutineURL(t.ID)+routineQuery(r)+"#task", http.StatusFound)
		return
	}
	if ref != t.ID {
		http.Redirect(w, r, "/tasks/"+t.ID, http.StatusFound)
		return
	}
	dir := filepath.Join(s.Store.Root(), t.Dir)
	watched := s.waitsOf(t.ID)
	row := s.taskListContext().row(t)
	data := taskData{Task: t, ListStatus: row.ListStatus, Conditions: row, Owner: s.ownerOf(t), Waits: watched,
		Body: renderMarkdown(t.Body, t.Dir), Children: s.Store.Children(t.ID), Actions: actions(t.Status), Flash: r.URL.Query().Get("flash")}
	if !t.Status.Closed() {
		data.RoutineJobs = s.routineJobs()
	}
	if parent, err := s.Store.Get(t.Parent); t.Parent != "" && err == nil {
		data.Parent = &parent
	}
	filepath.WalkDir(filepath.Join(dir, "artifacts"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			rel, _ := filepath.Rel(filepath.Join(dir, "artifacts"), p)
			data.Artifacts = append(data.Artifacts, filepath.ToSlash(rel))
		}
		return nil
	})
	if notes, err := os.ReadFile(filepath.Join(dir, "notes.md")); err == nil {
		data.Notes = renderMarkdown(string(notes), t.Dir)
	}
	data.LogName = s.Store.LogName()
	if lines, err := s.Store.HistoryLines(t, 6); err == nil {
		data.HasLog = true
		for _, line := range lines {
			data.Recent = append(data.Recent, strings.TrimPrefix(line, "- "))
		}
	}

	for _, plan := range s.plans() {
		if t.Project != plan.Slug || t.Area != plan.Area {
			continue
		}
		plan := plan
		data.Plan = &plan
		for i := range plan.Entries {
			if plan.Entries[i].ID == t.ID {
				data.Entry, data.Position = &plan.Entries[i], i+1
			}
		}
	}
	if s.Requests != nil {
		data.CanAsk, data.Requests = true, s.Requests.Recent(5, t.ID, false)
	}
	if s.Peer != nil && t.Area == s.Store.HandoffArea() {
		data.CanTellPeer, data.Told = true, s.told(5, t.ID)
	}
	data.Handovers = s.handoversOf(t.ID)
	data.Stored, data.Pinned, data.ExpiresOn = s.Store.StoredFiles(t), s.Store.Pinned(t), s.Store.ExpiresOn(t)
	var sum int64
	for _, f := range data.Stored {
		sum += f.Size
	}
	data.StoredSum = task.HumanSize(sum)
	lines := taskEvents(s.Store.Root(), t)
	data.ReviewSince = s.taskReviewSince(t, lines)
	if s.Store.Format() >= 1 {
		data.PastMaterials = pastMaterials(t, lines, s.Store.Location())
		data.History = groupedHistory(lines, t.Dir, s.Store.Location())
	}
	var artifactPaths, storedPaths []string
	for _, name := range data.Artifacts {
		artifactPaths = append(artifactPaths, "artifacts/"+name)
	}
	for _, f := range data.Stored {
		storedPaths = append(storedPaths, "files/"+f.Path)
	}
	data.ArtifactInfo = artifactInfos(s.Store.Root(), t, artifactPaths, lines, s.Store.Location())
	data.StoredInfo = artifactInfos(s.Store.Root(), t, storedPaths, lines, s.Store.Location())
	setMaterialURLs(data.ArtifactInfo, "/tasks/"+t.ID)
	setMaterialURLs(data.StoredInfo, "/tasks/"+t.ID)
	if !t.Status.Closed() {
		data.Warnings = append(task.CheckFormat(t, s.Store.Files(t), s.Store.Format()), task.LintFormat(t.Body, s.Store.Format())...)
		data.Warnings = append(data.Warnings, s.Store.FileWarnings(t)...)
	}
	s.render(w, http.StatusOK, "task", t.ID+" "+t.Title, data)
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request) {
	before, _ := s.Store.Get(r.PathValue("id"))
	t, changed, err := s.Store.SetStatus(r.PathValue("id"), task.Status(r.FormValue("status")), "Web", r.FormValue("note"))
	switch {
	case errors.Is(err, task.ErrNotFound):
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	case err != nil:
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if changed {
		s.statusChanged(t)
		if s.Changed != nil {
			s.Changed()
		}
	}
	back, undo := "/tasks/"+t.ID, ""
	if r.FormValue("back") == "home" {
		back = "/"
		if changed && before.Status == task.Closing && t.Status == task.Done {
			undo = "&undo=" + url.QueryEscape(t.ID)
		}
	}
	http.Redirect(w, r, back+"?flash="+url.QueryEscape(t.ID+" を「"+taskListLabel(t.Status)+"」にしました。")+undo, http.StatusSeeOther)
}

// upload saves files chosen in the browser into the task's files/ or
// artifacts/ directory.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, task.MaxFileSize*4)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.fail(w, http.StatusBadRequest, "ファイルを受け取れませんでした。1ファイルの上限は50 MiBです。")
		return
	}
	kind := r.FormValue("to")
	if kind == "" {
		kind = task.FilesDir
	}
	var names []string
	for _, header := range r.MultipartForm.File["file"] {
		f, err := header.Open()
		if err == nil {
			_, err = s.Store.PutFile(id, kind, filepath.Base(header.Filename), f, "Web")
			f.Close()
		}
		if err != nil {
			s.fail(w, http.StatusBadRequest, err.Error())
			return
		}
		names = append(names, filepath.Base(header.Filename))
	}
	if len(names) == 0 {
		s.fail(w, http.StatusBadRequest, "ファイルを選んでください。")
		return
	}
	if kind == task.ArtifactsDir && s.Changed != nil {
		s.Changed()
	}
	http.Redirect(w, r, "/tasks/"+strings.ToUpper(id)+"?flash="+url.QueryEscape(strings.Join(names, "、")+" を "+kind+"/ に置きました。"), http.StatusSeeOther)
}

// changeStored removes one stored file, or changes whether the task's
// files/ are kept after it is closed.
func (s *Server) changeStored(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var err error
	message := ""
	switch {
	case r.FormValue("remove") != "":
		err, message = s.Store.RemoveFile(id, r.FormValue("remove"), "Web"), "files/"+r.FormValue("remove")+" を消しました。"
	case r.FormValue("keep") == "1":
		err, message = s.Store.SetPinned(id, true), "タスクを閉じたあとも files/ のファイルを残します。"
	default:
		err, message = s.Store.SetPinned(id, false), "files/ のファイルを残す指定を外しました。"
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	http.Redirect(w, r, "/tasks/"+strings.ToUpper(id)+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}

// apiPutFile saves the request body as a file of the task. A command using
// the server API can attach a file even from a read-only records copy.
func (s *Server) apiPutFile(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("to")
	if kind == "" {
		kind = task.FilesDir
	}
	sess, known := s.session(r)
	if s.floodStopped(w, r.PathValue("id"), sess, known) {
		return
	}
	hash := sha256.New()
	saved, err := s.Store.PutFile(r.PathValue("id"), kind, r.URL.Query().Get("name"), io.TeeReader(r.Body, hash), actorFor(r.URL.Query().Get("actor"), sess, known))
	switch {
	case errors.Is(err, task.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if kind == task.ArtifactsDir && s.Changed != nil {
		s.Changed()
	}
	s.floodWrote(r.PathValue("id"), sess, known, "ファイル "+kind+"/"+saved.Path, "file\x00"+kind+"/"+saved.Path+"\x00"+hex.EncodeToString(hash.Sum(nil)))
	writeJSON(w, http.StatusOK, map[string]any{"path": kind + "/" + saved.Path, "size": saved.Size})
}

// apiWriteDoc replaces the body of task.md, or notes.md, with the request
// body. A conversation on a read-only copy saves its text this way, without
// a request to the main conversation. The reply carries what the checks
// still find in task.md, as advice.
func (s *Server) apiWriteDoc(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.URL.Query().Get("name")
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, task.MaxDocSize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "文書を読み取れませんでした。保存できる文書のサイズは " + task.HumanSize(task.MaxDocSize) + " までです"})
		return
	}
	s.Store.Refresh(id)
	sess, known := s.session(r)
	if s.floodStopped(w, id, sess, known) {
		return
	}
	before, _ := s.Store.Get(id)
	t, err := s.Store.WriteDoc(id, name, string(data), actorFor(r.URL.Query().Get("actor"), sess, known))
	switch {
	case errors.Is(err, task.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	sum := sha256.Sum256(data)
	s.floodWrote(id, sess, known, name, "doc\x00"+name+"\x00"+hex.EncodeToString(sum[:]))
	advice := []string{}
	switch name {
	case task.TaskFile:
		for _, warn := range append(task.CheckFormat(t, s.Store.Files(t), s.Store.Format()), task.LintFormat(t.Body, s.Store.Format())...) {
			advice = append(advice, warn.String())
		}
	case task.NotesFile:
		if s.Store.Format() >= 1 {
			for _, warn := range task.LintDocument("notes", string(data)) {
				advice = append(advice, warn.String())
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t, "advice": advice, "warning": ownedByOther(before, sess, known)})
}

// setAfter sets or clears the tasks a task waits for.
func (s *Server) setAfter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, _, err := s.Store.SetField(id, "after", r.FormValue("after"), "Web")
	switch {
	case errors.Is(err, task.ErrNotFound):
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	case err != nil:
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	message := "待つタスクに " + strings.Join(t.After, "、") + " を指定しました。"
	if len(t.After) == 0 {
		message = "待つタスクの指定を外しました。"
	}
	http.Redirect(w, r, "/tasks/"+t.ID+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}

func (s *Server) setAfterRun(w http.ResponseWriter, r *http.Request) {
	name, err := schedule.ResolveWait(s.Store.Root(), r.FormValue("after_run"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	t, changed, err := s.Store.SetField(r.PathValue("id"), "after_run", name, "Web")
	switch {
	case errors.Is(err, task.ErrNotFound):
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	case err != nil:
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if changed && s.Changed != nil {
		s.Changed()
	}
	message := "定期タスクの指定を外しました。"
	if name != "" {
		message = "定期タスク " + name + " の次の実行を待ちます。"
	}
	http.Redirect(w, r, "/tasks/"+t.ID+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}

// start asks Lacia to begin a task that became ready, without the user
// having to write a request.
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	saved, _, err := s.accept(request.Request{Source: "Web", TaskID: r.PathValue("id"), Text: "Webの「着手を頼む」からの依頼です。条件がそろったので、このタスクに着手してください。"}, "")
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	back := "/tasks/" + saved.TaskID
	if r.FormValue("back") == "home" {
		back = "/"
	}
	http.Redirect(w, r, back+"?flash="+url.QueryEscape("着手の依頼を受け付けました（"+saved.TaskID+"）。"), http.StatusSeeOther)
}

// StartReady asks Lacia to begin a task whose conditions were met and that
// was marked to start by itself (on_ready), as the button on its page does
// for the user. If the request cannot be queued, the task asks for review
// instead, so that it is not forgotten.
func (s *Server) StartReady(t task.Task) {
	req := request.Request{Source: "あらかじめ決めた条件", TaskID: t.ID, Text: "指定した着手の条件がそろったので、サーバーがこの依頼を出しました。待っていた条件は履歴で確かめられます。指定の日時を大きく過ぎているときは、いま始めても依頼の目的を果たせるか、先に確かめてください。タスクの「次にすること」と補足を読んで進めてください。"}
	to, err := s.targetReady(t)
	if err == nil {
		if to.Place != "" && to.Place != s.Place {
			_, _, err = s.addHandover(handover.Handover{TaskID: t.ID, Next: to.String(), From: req.Source, Then: req.Text})
		} else {
			if to.Kind != "" {
				req.Agent = to.Kind
				req.Kind = request.System
			}
			_, _, err = s.accept(req, "")
		}
	}
	if err != nil {
		s.Store.Ready(t.ID, "条件がそろいましたが、着手の依頼を出せませんでした（"+err.Error()+"）。", false)
	}
}

// setOnReady sets or clears the mark that makes Lacia begin the task when
// its conditions are met.
func (s *Server) setOnReady(w http.ResponseWriter, r *http.Request) {
	value, ok := s.setReadyValue(w, r)
	if !ok {
		return
	}
	message := "条件がそろったら、「要対応」へ出します。"
	if value != "" {
		message = "条件がそろったら、わたしが着手します。"
		if to, _ := task.ReadyDestination(value); to != "" {
			message = "条件がそろったら、" + to + " の会話へ着手を頼みます。"
		}
	}
	t, _, err := s.Store.SetField(r.PathValue("id"), "on_ready", value, "Web")
	switch {
	case errors.Is(err, task.ErrNotFound):
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	case err != nil:
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	http.Redirect(w, r, "/tasks/"+t.ID+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}

// setSchedule sets or clears the time at which a task asks for review. With
// "hide" the task also stays out of the lists until then.
func (s *Server) setSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	when := strings.TrimSpace(r.FormValue("date") + " " + r.FormValue("time"))
	set, clear := "review_at", "resume_on"
	if r.FormValue("hide") != "" {
		set, clear = clear, set
	}
	if r.FormValue("clear") != "" {
		when = ""
	}
	_, _, err := s.Store.SetField(id, set, when, "Web")
	if err == nil {
		_, _, err = s.Store.SetField(id, clear, "", "Web")
	}
	switch {
	case errors.Is(err, task.ErrNotFound):
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	case err != nil:
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	message := "着手の条件に " + when + " を指定しました。"
	if when == "" {
		message = "日時の指定を外しました。"
	}
	http.Redirect(w, r, "/tasks/"+strings.ToUpper(id)+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Create(task.NewTask{Area: r.FormValue("area"), Title: r.FormValue("title"), Parent: strings.ToUpper(strings.TrimSpace(r.FormValue("parent"))), Actor: "Web"})
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	http.Redirect(w, r, "/tasks/"+t.ID+"?flash="+url.QueryEscape(t.ID+" を作りました。"), http.StatusSeeOther)
}

func (s *Server) taskFile(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		if job, ok := s.oldRoutineID(r.PathValue("id")); ok {
			http.Redirect(w, r, schedule.RoutineURL(job.Name)+"/files/"+escapePath(r.PathValue("path"))+routineQuery(r), http.StatusFound)
			return
		}
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	}
	s.serveFile(w, r, path.Join(t.Dir, r.PathValue("path")), &t)
}

// taskLog shows the whole history of a task of format 1, each event the
// way log.md showed it. The file itself is log.jsonl.
func (s *Server) taskLog(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		if job, ok := s.oldRoutineID(r.PathValue("id")); ok {
			http.Redirect(w, r, "/tasks/"+job.Name+"/log"+routineQuery(r), http.StatusFound)
			return
		}
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	}
	s.showHistory(w, r, t.Dir, &t)
}

// projectLog is taskLog for a project.
func (s *Server) projectLog(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.findPlan(r)
	if !ok {
		s.fail(w, http.StatusNotFound, "そのプロジェクトは見つかりませんでした。")
		return
	}
	s.showHistory(w, r, plan.Dir, nil)
}

func (s *Server) showHistory(w http.ResponseWriter, r *http.Request, dir string, owner *task.Task) {

	lines, err := task.ReadHistoryIn(filepath.Join(s.Store.Root(), filepath.FromSlash(dir)), s.Store.Format(), 0, s.Store.Location())
	if err != nil {
		s.fail(w, http.StatusNotFound, "履歴はまだありません。")
		return
	}
	file := dir + "/" + task.HistoryFile
	data := fileData{Path: file, Task: owner, Plain: strings.Join(lines, "\n") + "\n", URL: r.URL.Path}
	if owner != nil {
		data.History = groupedHistory(taskEvents(s.Store.Root(), *owner), dir, s.Store.Location())
	}
	s.render(w, http.StatusOK, "file", "履歴", data)
}

func (s *Server) repoFile(w http.ResponseWriter, r *http.Request) {
	repoPath := r.PathValue("path")
	if parts := strings.SplitN(repoPath, "/", 5); len(parts) == 5 && parts[0] == "areas" && parts[2] == "routines" {
		if job, ok := s.oldRoutineID(parts[3]); ok && job.Area == parts[1] && job.Path != "" {
			file := path.Join(path.Dir(job.Path), parts[4])
			if strings.HasPrefix(file, path.Dir(job.Path)+"/") {
				http.Redirect(w, r, "/files/"+escapePath(file)+routineQuery(r), http.StatusTemporaryRedirect)
				return
			}
		}
	}
	if s.Store.Format() >= task.AreaRoutineFormat {
		if legacy, ok := strings.CutPrefix(repoPath, "routines/"); ok {
			id, file, _ := strings.Cut(legacy, "/")
			if file == "routine.md" && task.ValidRoutineID(id) {
				if job, ok := s.oldRoutineID(id); ok {
					http.Redirect(w, r, "/files/"+escapePath(job.Path)+routineQuery(r), http.StatusTemporaryRedirect)
					return
				}
				root := s.Store.Root()
				if dir, err := task.RoutineDir(root, id); err == nil {
					if rel, err := filepath.Rel(root, filepath.Join(dir, file)); err == nil {
						http.Redirect(w, r, "/files/"+escapePath(filepath.ToSlash(rel))+routineQuery(r), http.StatusTemporaryRedirect)
						return
					}
				}
			}
		}
	}
	s.serveFile(w, r, repoPath, nil)
}

// hidden are top-level entries that are never served.
var hidden = map[string]bool{".git": true, ".lacia": true, ".codex": true, ".claude": true, "local": true}

type fileData struct {
	History  []historyGroup
	Path     string
	Task     *task.Task
	Body     template.HTML
	Plain    string
	Settings []task.Field
	// For Markdown: the page shows either the rendered text or the source.
	Markdown, Source bool
	URL              string
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, repoPath string, owner *task.Task) {
	clean := path.Clean("/" + repoPath)[1:]
	if clean == "" || hidden[strings.Split(clean, "/")[0]] {
		s.fail(w, http.StatusNotFound, "そのファイルは表示できません。")
		return
	}
	root, err := filepath.EvalSymlinks(s.Store.Root())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, "リポジトリーを読めませんでした。")
		return
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(clean)))
	if err != nil || !strings.HasPrefix(full, root+string(filepath.Separator)) {
		s.fail(w, http.StatusNotFound, clean+" は見つかりませんでした。")
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		s.fail(w, http.StatusNotFound, clean+" は見つかりませんでした。")
		return
	}
	ext := strings.ToLower(path.Ext(clean))
	textual := map[string]bool{".md": true, ".txt": true, ".json": true, ".jsonl": true, ".csv": true, ".tsv": true, ".yaml": true, ".yml": true, ".toml": true, ".log": true, ".py": true, ".go": true, ".sh": true}
	if textual[ext] && r.URL.Query().Get("raw") == "" {
		if info.Size() > 4<<20 {
			s.fail(w, http.StatusRequestEntityTooLarge, clean+" は大きすぎるので、ここでは表示しません。")
			return
		}
		content, err := os.ReadFile(full)
		if err != nil {
			s.fail(w, http.StatusInternalServerError, clean+" を読めませんでした。")
			return
		}
		data := fileData{Path: clean, Task: owner, Markdown: ext == ".md", URL: r.URL.Path}
		data.Source = data.Markdown && r.URL.Query().Get("view") == "source"
		if data.Markdown && !data.Source {
			body := string(content)
			if isScheduleDefinition(clean) {
				if doc, err := task.ParseDoc(body); err == nil && len(doc.Fields) > 0 {
					data.Settings = doc.Fields
					body = doc.Body
				}
			}
			data.Body = renderMarkdown(body, path.Dir(clean))
		} else {
			data.Plain = string(content)
		}
		s.render(w, http.StatusOK, "file", path.Base(clean), data)
		return
	}
	// Anything else is sent as a file. Artifacts may be HTML written by an
	// agent, so they run in a sandbox with no access to this site.
	// Scripts may run, so that an interactive chart works, but the page gets
	// an origin of its own: it cannot read this site's pages or call its API,
	// cannot submit forms or navigate the window, and cannot reach the network.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' 'self' blob:; style-src 'unsafe-inline' 'self'; img-src data: blob: 'self'; font-src data: 'self'; media-src data: blob: 'self'; connect-src 'none'; form-action 'none'; base-uri 'none'")
	if ct := mime.TypeByExtension(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment")
	}
	// ServeContent rather than ServeFile, which would redirect index.html.
	f, err := os.Open(full)
	if err != nil {
		s.fail(w, http.StatusNotFound, clean+" は見つかりませんでした。")
		return
	}
	defer f.Close()
	http.ServeContent(w, r, path.Base(clean), info.ModTime(), f)
}

func isScheduleDefinition(repoPath string) bool {
	dir, file := path.Split(repoPath)
	if dir == "schedules/" {
		name, ok := strings.CutSuffix(file, ".md")
		return ok && schedule.ValidName(name)
	}
	root, name := path.Split(strings.TrimSuffix(dir, "/"))
	if root == "routines/" {
		return file == "routine.md" && schedule.ValidName(name)
	}
	parts := strings.Split(strings.TrimSuffix(root, "/"), "/")
	return len(parts) == 3 && parts[0] == "areas" && parts[2] == "routines" && file == "routine.md" && task.ValidRoutineID(name)
}

// plans reads the projects and arranges their tasks. Projects are a
// handful of small files, so they are read on each request, which keeps
// edits by Codex visible at once.
func (s *Server) plans() []project.Plan {
	projects, problems := project.Load(s.Store.Root(), s.Store.Format(), s.Store.Areas())
	for _, err := range problems {
		slog.Warn("project", "error", err)
	}
	tasks := s.Store.List()
	plans := make([]project.Plan, 0, len(projects))
	for _, p := range projects {
		plans = append(plans, project.Arrange(p, tasks))
	}
	return plans
}

// projectOf finds the project a task names, if it exists.
func (s *Server) projectOf(t task.Task) *project.Project {
	if t.Project == "" {
		return nil
	}
	dir := s.Store.AreaDir(t.Area) + "/projects/" + t.Project
	data, err := os.ReadFile(filepath.Join(s.Store.Root(), filepath.FromSlash(dir), project.File))
	if err != nil {
		return nil
	}
	p, err := project.ParseFormat(string(data), s.Store.Format())
	if err != nil {
		return nil
	}
	p.Slug, p.Area, p.Dir = t.Project, t.Area, dir
	return &p
}

func (s *Server) projects(w http.ResponseWriter, _ *http.Request) {
	s.render(w, http.StatusOK, "projects", "プロジェクト", s.plans())
}

type projectData struct {
	Rows  map[string]Row
	Plan  project.Plan
	Body  template.HTML // project.md without the order section, which the page shows as a list
	Files []string
	Log   []string
	Flash string
	// LogName is the file of the history, log.md or log.jsonl.
	LogName string
}

func (s *Server) findPlan(r *http.Request) (project.Plan, bool) {
	for _, plan := range s.plans() {
		if plan.Area == r.PathValue("area") && plan.Slug == r.PathValue("slug") {
			return plan, true
		}
	}
	return project.Plan{}, false
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.findPlan(r)
	if !ok {
		s.fail(w, http.StatusNotFound, "そのプロジェクトは見つかりませんでした。")
		return
	}
	data := projectData{Plan: plan, Rows: make(map[string]Row, len(plan.Entries)), Flash: r.URL.Query().Get("flash"), LogName: s.Store.LogName()}
	list := s.taskListContext()
	for _, e := range plan.Entries {
		data.Rows[e.ID] = list.row(e.Task)
	}
	body := plan.Body
	heading := project.OrderHeadingFor(s.Store.Format())
	if order := task.Section(body, heading); order != "" {
		body = strings.Replace(body, "## "+heading+"\n\n"+order, "", 1)
	}
	data.Body = renderMarkdown(body, plan.Dir)
	dir := filepath.Join(s.Store.Root(), plan.Dir)
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") && d.Name() != project.File && d.Name() != data.LogName {
			rel, _ := filepath.Rel(dir, p)
			data.Files = append(data.Files, filepath.ToSlash(rel))
		}
		return nil
	})
	lines, _ := task.ReadHistoryIn(dir, task.LatestFormat, 8, s.Store.Location())
	for _, line := range lines {
		data.Log = append(data.Log, strings.TrimPrefix(line, "- "))
	}

	s.render(w, http.StatusOK, "project", plan.Title, data)
}

// moveInProject shifts a task one place in the project's order and notes
// the change in the project's history.
func (s *Server) moveInProject(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.findPlan(r)
	if !ok {
		s.fail(w, http.StatusNotFound, "そのプロジェクトは見つかりませんでした。")
		return
	}
	id, delta, word := r.FormValue("id"), 1, "後ろ"
	if r.FormValue("dir") == "up" {
		delta, word = -1, "前"
	}
	var open []string
	for _, e := range plan.Entries {
		if !e.Status.Closed() {
			open = append(open, e.ID)
		}
	}
	file := filepath.Join(s.Store.Root(), plan.Dir, project.File)
	src, err := os.ReadFile(file)
	if err == nil {
		var out string
		if out, err = project.MoveFormat(string(src), open, id, delta, s.Store.Format()); err == nil {
			err = os.WriteFile(file, []byte(out), 0o644)
		}
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	direction := "down"
	if delta < 0 {
		direction = "up"
	}
	message := id + " を1つ" + word + "に移しました。"
	project.AppendLog(filepath.Join(s.Store.Root(), plan.Dir), s.Now().In(s.Store.Location()),
		task.Event{Type: task.EventOrder, By: "Web", Task: id, Direction: direction, Text: message})
	if s.Changed != nil {
		s.Changed()
	}
	http.Redirect(w, r, "/projects/"+plan.Area+"/"+plan.Slug+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}

// accept records a request for Codex. The task is marked as in progress at
// once, before Codex has seen the message: that is what makes the page
// reflect a request immediately. A request about no particular task gets a
// provisional task, which Codex renames or merges later.
func (s *Server) accept(req request.Request, area string) (request.Request, bool, error) {
	s.acceptMu.Lock()
	defer s.acceptMu.Unlock()
	if s.Requests == nil {
		return request.Request{}, false, errors.New("いまは依頼を受け付けていません")
	}
	if req.ID != "" {
		if old, ok := s.Requests.Get(req.ID); ok {
			return old, false, nil
		}
	}
	if req.StatusPolicy != "" && req.StatusPolicy != request.KeepStatus {
		return request.Request{}, false, errors.New("status_policy は空欄にするか、keep を指定してください")
	}
	if req.StatusPolicy == request.KeepStatus && req.TaskID == "" {
		return request.Request{}, false, errors.New("タスクの状態を変えずに依頼するときは、既存のタスクを指定してください")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return request.Request{}, false, errors.New("依頼の文章を入力してください")
	}
	if !s.Store.KeepsTasks() {
		// The request is answered in the conversation and ends there; no
		// task is made for it, and none is started.
		if req.TaskID != "" {
			return request.Request{}, false, task.ErrNoTasks
		}
	} else if req.TaskID != "" {
		t, err := s.Store.Get(req.TaskID)
		if err != nil {
			return request.Request{}, false, err
		}
		req.TaskID = t.ID
		if req.StatusPolicy != request.KeepStatus && t.Status != task.Doing {
			req.Was = string(t.Status)
			if _, _, err := s.Store.SetStatus(t.ID, task.Doing, "依頼の受付", "依頼を受け付けました。"); err != nil {
				return request.Request{}, false, err
			}
		}
	} else {
		if area == "" {
			area = s.Store.Areas()[0]
		}
		t, err := s.Store.Create(task.NewTask{Area: area, Title: "受け付けた依頼を整理します", Status: task.Doing, Actor: "依頼の受付",
			Body: task.Body(s.Store.Format(), "依頼を受け付けたところです。内容はこれから整理します。", "わたしが依頼を読んで、進め方を決めます。")})
		if err != nil {
			return request.Request{}, false, err
		}
		req.TaskID = t.ID
	}
	saved, created, err := s.Requests.Add(req)
	if err == nil && created {
		if saved.TaskID != "" {
			s.Store.Log(saved.TaskID, task.Event{Type: task.EventRequest, Request: saved.ID, Via: "依頼の受付", Text: "依頼を受け付けました（" + saved.ID + "）。"})
		}
		if s.Changed != nil {
			s.Changed()
		}
	}
	return saved, created, err
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	saved, _, err := s.accept(request.Request{Source: "Web", TaskID: strings.TrimSpace(r.FormValue("task")), Text: r.FormValue("text")}, r.FormValue("area"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, "/tasks/"+saved.TaskID+"?flash="+url.QueryEscape("依頼を受け付けました。"+saved.TaskID+" は進行中です。"), http.StatusSeeOther)
}

func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	if s.Requests == nil {
		s.fail(w, http.StatusNotFound, "いまは依頼を受け付けていません。")
		return
	}
	s.render(w, http.StatusOK, "requests", "やり取り", s.Requests.Recent(50, "", r.URL.Query().Get("all") != ""))
}

// resume never accepts replacement text or routing from the caller.
func (s *Server) resume(original, id, source string) (request.Request, bool, error) {
	s.acceptMu.Lock()
	defer s.acceptMu.Unlock()
	if s.Requests == nil {
		return request.Request{}, false, errors.New("いまは依頼を受け付けていません")
	}
	old, ok := s.Requests.Get(original)
	if !ok || !old.CanResume() {
		return request.Request{}, false, errors.New("この依頼は再開できません")
	}
	t, err := s.Store.Get(old.TaskID)
	if err != nil {
		return request.Request{}, false, err
	}
	if t.Status == task.Done || t.Status == task.Cancelled {
		return request.Request{}, false, errors.New("閉じたタスクの依頼は再開できません")
	}
	was := ""
	if t.Status != task.Doing {
		was = string(t.Status)
	}
	saved, created, err := s.Requests.Resume(original, id, source, was, func(next request.Request) error {
		if t.Status != task.Doing {
			if _, _, err := s.Store.SetStatus(t.ID, task.Doing, "依頼の再開", "依頼の続きを受け付けました。"); err != nil {
				return err
			}
		}
		return s.Store.Log(t.ID, task.Event{Type: task.EventRequest, Request: next.ID, Via: "再開", Text: "依頼の続きを受け付けました（元の依頼: " + original + "、新しい依頼: " + next.ID + "）。"})
	})
	if err != nil {
		return request.Request{}, false, err
	}
	if created && s.Changed != nil {
		s.Changed()
	}
	return saved, created, nil
}

func (s *Server) resumeRequest(w http.ResponseWriter, r *http.Request) {
	sess, known := s.session(r)
	if s.Requests != nil {
		if original, ok := s.Requests.Get(r.PathValue("id")); ok && s.floodStopped(w, original.TaskID, sess, known) {
			return
		}
	}
	saved, _, err := s.resume(r.PathValue("id"), "", "Web")
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, "/requests/"+saved.ID, http.StatusSeeOther)
}

func (s *Server) requestDetail(w http.ResponseWriter, r *http.Request) {
	if s.Requests != nil {
		if saved, ok := s.Requests.Get(r.PathValue("id")); ok {
			related := []request.Request{saved}
			for _, next := range s.Requests.Recent(0, saved.TaskID, false) {
				if next.ResumeOf == saved.ID {
					related = append(related, next)
				}
			}
			s.render(w, http.StatusOK, "requests", "やり取り", related)
			return
		}
	}
	s.fail(w, http.StatusNotFound, "その依頼は見つかりません")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// apiAsk is the request form's API for commands and other clients.
// Sending the same id twice returns the first request unchanged.
func (s *Server) apiAsk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID, Text, Task, Source, Area string
		StatusPolicy                 string `json:"status_policy"`
		Resume                       string `json:"resume"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSONを読めませんでした"})
		return
	}
	if in.Source == "" {
		in.Source = "コマンド"
	}
	sess, known := s.session(r)
	if in.Task != "" && s.floodStopped(w, in.Task, sess, known) {
		return
	}
	var saved request.Request
	var created bool
	var err error
	if in.Resume != "" {
		if s.Requests != nil {
			if original, ok := s.Requests.Get(in.Resume); ok && s.floodStopped(w, original.TaskID, sess, known) {
				return
			}
		}
		if in.Text != "" || in.Task != "" || in.Area != "" || in.StatusPolicy != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "依頼を再開するときは、文章、タスク、領域、状態を指定しないでください"})
			return
		}
		saved, created, err = s.resume(in.Resume, in.ID, in.Source)
	} else {
		req := request.Request{ID: in.ID, Source: in.Source, TaskID: in.Task, Text: in.Text, StatusPolicy: in.StatusPolicy}
		if known {
			// A conversation sent it, not the user at a keyboard.
			req.Sender = sess.Label()
		}
		saved, created, err = s.accept(req, in.Area)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		if saved.TaskID != "" {
			s.floodWrote(saved.TaskID, sess, known, "依頼の受付", "ask\x00"+strings.TrimSpace(in.Text))
		}
	}
	writeJSON(w, status, saved)
}

func (s *Server) apiRequest(w http.ResponseWriter, r *http.Request) {
	if s.Requests != nil {
		if saved, ok := s.Requests.Get(r.PathValue("id")); ok {
			writeJSON(w, http.StatusOK, saved)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "その依頼は見つかりません"})
}

func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request) {
	if s.PublishedRevision != nil {
		if revision := s.PublishedRevision(r.Context()); revision != "" {
			w.Header().Set(task.RevisionHeader, revision)
		}
	}
	writeJSON(w, http.StatusOK, s.Store.List())
}

func (s *Server) apiTask(w http.ResponseWriter, r *http.Request) {
	s.Store.Refresh(r.PathValue("id"))
	t, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if r.URL.Query().Get("detail") == "1" {
		detail, err := s.Store.Detail(t)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "タスクの詳細を読めませんでした。"})
			return
		}
		writeJSON(w, http.StatusOK, detail)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// apiCreate allocates the ID here, in the one process that owns the series,
// so commands run by several agents at once cannot pick the same number.
func (s *Server) apiCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Area, Title, Status, Parent, Slug, Actor string
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSONを読めませんでした"})
		return
	}
	sess, known := s.session(r)
	actor := actorFor(in.Actor, sess, known)
	t, err := s.Store.Create(task.NewTask{Area: in.Area, Title: in.Title, Status: task.Status(in.Status), Parent: in.Parent, Slug: in.Slug, Actor: actor})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// A task created as "doing" belongs to the conversation that created it.
	if known && t.Status == task.Doing {
		if claimed, _, err := s.Store.Claim(t.ID, task.Owner{Label: sess.Label(), Session: sess.Key}, actor, ""); err == nil {
			t = claimed
		}
	}
	if s.Changed != nil {
		s.Changed()
	}
	writeJSON(w, http.StatusCreated, t)
}

// apiUpdate changes the status, one field, or appends to the log. Moving a
// task to "doing" makes the calling conversation its owner, unless another
// conversation already is; "claim" takes the task over from that one.
func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status, Note, Field, Value, Log, Actor string
		Claim                                  bool
		// Recheck registers how often and until when a waiting task is
		// looked at; Rechecked and Text record what one look found.
		Recheck         *struct{ Every, Until, Next string }
		Rechecked, Text string
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSONを読めませんでした"})
		return
	}
	id := r.PathValue("id")
	s.Store.Refresh(id)
	sess, known := s.session(r)
	if s.floodStopped(w, id, sess, known) {
		return
	}
	actor := actorFor(in.Actor, sess, known)
	before, _ := s.Store.Get(id)
	warning := ownedByOther(before, sess, known)
	var err error
	changed := false
	switch {
	case in.Claim && !known:
		err = errors.New("担当する会話を特定できませんでした。Codex か Claude の会話から実行してください")
	case in.Claim || (known && in.Status == string(task.Doing) && warning == ""):
		var set task.Task
		if set, changed, err = s.Store.Claim(id, task.Owner{Label: sess.Label(), Session: sess.Key}, actor, in.Note); err == nil && changed && before.Status != task.Doing {
			s.statusChanged(set)
		}
		warning = ""
	case in.Status == string(task.Doing) && warning != "":
		// Another conversation is working on it: leave the task as it is.
	case in.Status != "":
		var set task.Task
		if set, changed, err = s.Store.SetStatus(id, task.Status(in.Status), actor, in.Note); err == nil && changed {
			s.statusChanged(set)
		}
	case in.Recheck != nil:
		_, changed, err = s.Store.SetRecheck(id, in.Recheck.Every, in.Recheck.Until, in.Recheck.Next, actor)
	case in.Rechecked != "":
		_, changed, err = s.Store.Rechecked(id, in.Rechecked, in.Text, actor)
	case in.Field != "":
		if in.Field == "after_run" {
			in.Value, err = schedule.ResolveWait(s.Store.Root(), in.Value)
		}
		if in.Field == "on_ready" && s.ResolveDestination != nil {
			if to, parseErr := task.ReadyDestination(in.Value); parseErr != nil {
				err = parseErr
			} else if to != "" {
				_, err = s.ResolveDestination(to)
			}
		}
		if err == nil {
			_, changed, err = s.Store.SetField(id, in.Field, in.Value, actor)
		}
	case in.Log != "":
		defer s.floodWrote(id, sess, known, "記録「"+summary(in.Log, 40)+"」", "log\x00"+strings.TrimSpace(in.Log))
		// A note says who wrote it when a conversation or a name is known.
		note := task.Event{Type: task.EventNote, Text: strings.TrimSpace(in.Log)}
		if in.Actor != "" || known {
			note.By = actor
		}
		err, changed = s.Store.Log(id, note), true
	default:
		err = errors.New("status、field、log、recheck、rechecked のどれかを指定してください")
	}
	if errors.Is(err, task.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if changed && s.Changed != nil {
		s.Changed()
	}
	t, _ := s.Store.Get(id)
	out := map[string]any{"task": t, "changed": changed, "warning": warning}
	if in.Status == string(task.Waiting) && t.Status == task.Waiting {
		if advice := s.taskListContext().row(t).WaitingWarning(); advice != "" {
			out["waiting_warning"] = advice
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiWaitingWarnings uses live server conditions even for lint on a read-only copy.
func (s *Server) apiWaitingWarnings(w http.ResponseWriter, r *http.Request) {
	list := s.taskListContext()
	out := make(map[string]string)
	for _, t := range s.Store.List() {
		if t.Status == task.Waiting {
			if advice := list.row(t).WaitingWarning(); advice != "" {
				out[t.ID] = advice
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiProjectNote adds an entry to a project's history, for `lacia project
// note`. Like a note on a task, it says who wrote it when that is known.
func (s *Server) apiProjectNote(w http.ResponseWriter, r *http.Request) {
	var in struct{ Project, Log, Actor string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSONを読めませんでした"})
		return
	}
	text := strings.TrimSpace(in.Log)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "履歴に書く文章を指定してください"})
		return
	}
	p, err := project.Find(s.Store.Root(), s.Store.Format(), s.Store.Areas(), in.Project)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	sess, known := s.session(r)
	e := task.Event{Type: task.EventNote, Text: text}
	if in.Actor != "" || known {
		e.By = actorFor(in.Actor, sess, known)
	}
	if err := project.AppendLog(filepath.Join(s.Store.Root(), filepath.FromSlash(p.Dir)), s.Now().In(s.Store.Location()), e); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	writeJSON(w, http.StatusOK, map[string]string{"project": p.Area + "/" + p.Slug})
}

// redirectLegacySchedule preserves saved links and history month filters.
func redirectLegacySchedule(w http.ResponseWriter, r *http.Request) {
	target := "/routines" + strings.TrimPrefix(r.URL.EscapedPath(), "/schedules")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

func (s *Server) redirectScheduleDetail(w http.ResponseWriter, r *http.Request) {
	if job, ok := s.findRoutine(r.PathValue("name")); ok {
		http.Redirect(w, r, "/routines/"+url.PathEscape(job.Name)+routineQuery(r), http.StatusPermanentRedirect)
		return
	}
	redirectLegacySchedule(w, r)
}

type scheduleRow struct {
	schedule.Job
	Last string
}

// schedulePage is one scheduled job with the record of its runs.
type schedulePage struct {
	scheduleRow
	Executions []schedule.Execution
	Health     schedule.Health
	Pending    []schedule.Run
	Months     []string
	Month      string
	Runs       []schedule.Run
}

func (s *Server) schedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var page *schedulePage
	var jobs []schedule.Job
	var last map[string]string
	if s.Schedules != nil {
		jobs, last = s.Schedules()
	} else {
		jobs = s.routineJobs()
	}
	for _, j := range jobs {
		if j.Name == name {
			page = &schedulePage{scheduleRow: scheduleRow{j, strings.Replace(last[j.Name], "T", " ", 1)}}
			break
		}
	}
	if page == nil {
		s.fail(w, http.StatusNotFound, "その名前の定期タスクは見つかりませんでした。")
		return
	}
	root := s.Store.Root()
	if s.ScheduleDispatch != nil {
		page.Executions = s.ScheduleDispatch.List(name)
	}
	page.Health = schedule.HealthOf(root, name)
	page.Months = schedule.Months(root, name)
	if s.Runs != nil {
		page.Pending = s.Runs.Pending(name)
	}
	page.Month = r.URL.Query().Get("month")
	if page.Month == "" && len(page.Months) > 0 {
		page.Month = page.Months[0]
	}
	page.Runs = schedule.ReadRuns(root, name, page.Month)
	title := page.Title
	if title == "" {
		title = page.Name
	}
	s.render(w, http.StatusOK, "schedule", title, page)
}

// troubles lists the scheduled jobs and devices that need a look.
func (s *Server) troubles() []schedule.Trouble {
	if s.Schedules == nil {
		return nil
	}
	jobs, _ := s.Schedules()
	var devices map[string]schedule.DeviceStatus
	if s.ScheduleDispatch != nil {
		devices = s.ScheduleDispatch.Devices()
	}
	return schedule.Troubles(s.Store.Root(), jobs, devices)
}

// apiScheduleTroubles reports operational alerts to older notification clients.
func (s *Server) apiScheduleTroubles(w http.ResponseWriter, _ *http.Request) {
	if s.routineActions() {
		writeJSON(w, http.StatusOK, []schedule.Trouble{})
		return
	}
	troubles := s.troubles()
	if troubles == nil {
		troubles = []schedule.Trouble{}
	}
	for i := range troubles {
		troubles[i].Path = "/routines"
		if name := troubles[i].Name; name != "" {
			troubles[i].Path += "/" + url.PathEscape(name)
		}
	}
	writeJSON(w, http.StatusOK, troubles)
}

// apiCalendarScope lists the calendars this server shows. Notification
// clients use the same list for meeting notices.
func (s *Server) apiCalendarScope(w http.ResponseWriter, _ *http.Request) {
	scope := calendar.Scope
	if scope == nil {
		scope = []calendar.ScopeEntry{}
	}
	writeJSON(w, http.StatusOK, scope)
}

func (s *Server) apiCalendarList(w http.ResponseWriter, r *http.Request) {
	if s.calendar == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "このレイシアには予定のデータがありません"})
		return
	}
	q := r.URL.Query()
	result, err := s.calendar.List(time.Now(), q.Get("period"), q.Get("from"), q.Get("until"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

// apiSnapshot returns the stored calendar snapshot without display filters.
func (s *Server) apiSnapshot(w http.ResponseWriter, _ *http.Request) {
	if s.CalendarPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "予定のデータがありません"})
		return
	}
	data, err := os.ReadFile(s.CalendarPath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "予定のデータを読めませんでした"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(data)
}

package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func (s *Server) Handler() (http.Handler, error) {
	if s.Now == nil {
		s.Now = time.Now
	}
	s.Store.SetLiveCondition(s.liveCondition)
	funcs, err := s.templateFuncs()
	if err != nil {
		return nil, err
	}
	if s.Sessions == nil {
		s.Sessions = caller.NewRegistry()
		s.Sessions.Now = func() time.Time { return s.Now() }
	}
	if s.Routines == nil {
		s.Routines = routine.NewStore(s.Store.Root())
	}
	if err := s.initRoutineKey(); err != nil {
		return nil, err
	}
	if err := s.parsePages(funcs); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	if s.CalendarPath != "" {
		if err := s.openCalendar(mux); err != nil {
			return nil, err
		}
	}
	s.routeTaskPages(mux)
	s.routeTaskAPI(mux)
	s.routeRoutines(mux)
	s.routeCalendar(mux)
	s.routeConversations(mux)
	s.routeSelf(mux)
	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	return s.guard(mux), nil
}

func (s *Server) templateFuncs() (template.FuncMap, error) {
	css, err := staticFiles.ReadFile("static/app.css")
	if err != nil {
		return nil, err
	}
	cssHash := sha256.Sum256(css)
	cssURL := "/static/app.css?v=" + hex.EncodeToString(cssHash[:8])
	return template.FuncMap{
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
	}, nil
}

func (s *Server) parsePages(funcs template.FuncMap) error {
	s.pages = map[string]*template.Template{}
	for _, name := range []string{"home", "tasks", "task", "file", "error", "requests", "schedule", "routines", "routine", "routine-receipt", "settings", "projects", "project", "peer", "self", "who"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFiles, "templates/layout.html", "templates/task-conditions.html", "templates/task-parts.html", "templates/"+name+".html")
		if err != nil {
			return err
		}
		s.pages[name] = t
	}
	return nil
}

// openCalendar reads the calendar snapshot for the home page and serves the
// files its view uses.
func (s *Server) openCalendar(mux *http.ServeMux) error {
	t, err := calendar.AddTemplate(template.New("calendar"))
	if err != nil {
		return err
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
	return nil
}

func (s *Server) routeTaskPages(mux *http.ServeMux) {
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
}

// routeTaskAPI serves the commands. The waits and the handovers are routed
// only where they are kept.
func (s *Server) routeTaskAPI(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/tasks/{id}/files", s.apiPutFile)
	mux.HandleFunc("PUT /api/tasks/{id}/doc", s.apiWriteDoc)
	mux.HandleFunc("POST /api/requests", s.apiAsk)
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
}

func (s *Server) routeRoutines(mux *http.ServeMux) {
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
}

func (s *Server) routeCalendar(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/calendar", s.withCalendars(s.calendarSettings))
	mux.HandleFunc("POST /settings/calendar/connect", s.withCalendars(s.calendarConnect))
	mux.HandleFunc("POST /settings/calendar/disconnect", s.withCalendars(s.calendarDisconnect))
	mux.HandleFunc("GET /oauth/google/callback", s.withCalendars(s.calendarCallback))
	mux.HandleFunc("GET /api/calendar/snapshot", s.apiSnapshot)
	mux.HandleFunc("GET /api/calendar/list", s.apiCalendarList)
	mux.HandleFunc("POST /api/calendar/refresh", s.apiCalendarRefresh)
	mux.HandleFunc("GET /api/calendar/scope", s.apiCalendarScope)
}

// withCalendars decides per request, because Calendars may be set after the
// routes are built.
func (s *Server) withCalendars(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Calendars == nil {
			s.fail(w, http.StatusNotFound, "予定の連携は、このサーバーでは有効になっていません。")
			return
		}
		next(w, r)
	}
}

func (s *Server) apiCalendarRefresh(w http.ResponseWriter, r *http.Request) {
	if s.Calendars == nil || s.Calendars.Refresh == nil {
		http.Error(w, "このサーバーでは、Googleから予定を直接取得できません", http.StatusServiceUnavailable)
		return
	}
	if err := s.Calendars.Refresh(r.Context()); err != nil {
		http.Error(w, "読み取りが済んでいない予定があります。「予定の連携」で各カレンダーの状態を確かめてください", http.StatusServiceUnavailable)
		return
	}
	s.apiSnapshot(w, r)
}

// routeConversations serves the conversations, the peer profile, and the
// Discord side.
func (s *Server) routeConversations(mux *http.ServeMux) {
	mux.HandleFunc("GET /who", s.who)
	mux.HandleFunc("GET /api/sessions", s.apiSessions)
	mux.HandleFunc("GET /api/busy", s.apiBusy)
	mux.HandleFunc("POST /api/restart", s.apiRestart)
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
	mux.HandleFunc("GET /api/discord/members", s.apiMembers)
	mux.HandleFunc("POST /api/discord/attachments", s.apiDiscordAttachment)
	mux.HandleFunc("POST /api/discord/{action}", s.apiMemberPause)
}

func (s *Server) routeSelf(mux *http.ServeMux) {
	mux.HandleFunc("GET /self", s.withSelf(s.selfPage))
	mux.HandleFunc("GET /self/rejected/{day}", s.selfDraft)
	mux.HandleFunc("POST /self/comments", s.withSelf(s.selfComment))
	mux.HandleFunc("GET /api/self", s.withSelfAPI(s.apiSelf))
	mux.HandleFunc("GET /api/self/tidy/{day}", s.apiSelfTidy)
	mux.HandleFunc("POST /api/self/notes", s.withSelfAPI(s.apiSelfNote))
	mux.HandleFunc("POST /api/self/sleep", s.apiSelfSleep)
	mux.HandleFunc("POST /api/self/dates", s.withSelfAPI(s.apiSelfDates))
	mux.HandleFunc("GET /api/self/improvements", s.withSelfAPI(s.apiSelfImprovements))
	mux.HandleFunc("POST /api/self/improvements/{id}", s.withSelfAPI(s.apiDecideSelfImprovement))
	mux.HandleFunc("GET /api/self/core", s.apiSelfCore)
	mux.HandleFunc("PUT /api/self/core", s.apiSetSelfCore)
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
				http.Error(rec, "サーバーでエラーが起き、処理を続けられませんでした。", http.StatusInternalServerError)
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

package web

import (
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

// projectBlock is a project on the home page: its next task as a row like
// any other, and how many more wait behind it.
type projectBlock struct {
	project.Plan
	Rows []Row
}

// MachineIssue is one thing to fix on the machine, the commands that fix it
// (lacia doctor), and the part of the install guide about it.
type MachineIssue struct{ Problem, Fix, Guide string }

type homeData struct {
	Machine        []MachineIssue
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
	active, inProject := s.activePlans()
	lists := s.homeLists(list, inProject)
	data := homeData{
		Projects: s.projectBlocks(active, list),
		Recent:   lists.closed,
		Areas:    s.Store.Areas(),
		Flash:    r.URL.Query().Get("flash"),
		Tasks:    true,
	}
	data.Sections = []Section{
		s.section("review", "要対応", task.OwnerName+"の判断、回答、操作を待っています。", lists.review, list),
		s.section("closing", "完了確認待ち", "作業は終わっています。成果を見て、よければ完了にしてください。", lists.closing, list),
		s.section("ongoing", "進行中", "わたしが作業を進めているタスクと、処理が動いているタスクです。", lists.ongoing, list),
		s.section("ready", "着手待ち", "まだ始めていないタスクです。進行中のプロジェクトで次に着手するタスクは「プロジェクト」の欄に表示します。", lists.ready, list),
		s.section("later", "待機", "指定した日時が来るのを待っているタスクや、ほかのタスクや処理が終わるのを待っているタスクです。", lists.later, list),
	}
	if s.Machine != nil {
		data.Machine = s.Machine()
	}
	// Closing a finished task from its row takes one click, so the notice
	// that follows offers to take it back.
	if t, err := s.Store.Get(r.URL.Query().Get("undo")); err == nil && t.Status == task.Done {
		data.Undo = t.ID
	}
	if err := s.addHomeRoutines(&data, list); err != nil {
		s.fail(w, http.StatusInternalServerError, "定期タスクの結果を読めませんでした。")
		return
	}
	if s.Requests != nil {
		data.CanAsk = true
		data.Latest = s.latestRequests()
	}
	if s.calendar != nil {
		data.Calendar = s.calendarView(list.now)
	}
	s.render(w, http.StatusOK, "home", "レイシア", data)
}

// activePlans lists the active projects. A task of one that has not been
// started is shown as part of the project, which names the one to start
// next.
func (s *Server) activePlans() ([]project.Plan, map[string]bool) {
	var active []project.Plan
	inProject := map[string]bool{}
	for _, plan := range s.plans() {
		if plan.Status == project.Active {
			active = append(active, plan)
			inProject[plan.Area+"/"+plan.Slug] = true
		}
	}
	return active, inProject
}

// closed holds only the eight closed most recently.
type homeTaskLists struct {
	review, closing, ongoing, ready, later, closed []task.Task
}

func (s *Server) homeLists(list taskListContext, inProject map[string]bool) homeTaskLists {
	var l homeTaskLists
	for _, t := range s.Store.List() {
		switch list.status(t) {
		case task.Done, task.Cancelled:
			l.closed = append(l.closed, t)
		case task.Waiting:
			l.later = append(l.later, t)
		case task.Review:
			l.review = append(l.review, t)
		case task.Closing:
			l.closing = append(l.closing, t)
		case task.Todo:
			if !inProject[t.Area+"/"+t.Project] {
				l.ready = append(l.ready, t)
			}
		case task.Doing:
			l.ongoing = append(l.ongoing, t)
		}
	}
	sort.SliceStable(l.closed, func(i, j int) bool { return l.closed[i].Done > l.closed[j].Done })
	if len(l.closed) > 8 {
		l.closed = l.closed[:8]
	}
	return l
}

func (s *Server) projectBlocks(active []project.Plan, list taskListContext) []projectBlock {
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
	return blocks
}

// addHomeRoutines puts the recurring work that needs attention on the home
// page. In a repository that keeps no tasks, it is all the page shows.
func (s *Server) addHomeRoutines(data *homeData, list taskListContext) error {
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
		return nil
	}
	if err := s.addRoutineReview(&data.Sections[0], s.troubles()); err != nil {
		return err
	}
	data.RecentRoutines = s.recentRoutineReviews()
	return nil
}

// latestRequests shows what the user asked and anything Lacia has to tell;
// notices that ended with nothing to report stay on the full list only.
func (s *Server) latestRequests() []request.Request {
	var latest []request.Request
	for _, req := range s.Requests.Recent(30, "", true) {
		if len(latest) < 4 && (req.Kind == request.User || (req.Reply != "" && !strings.HasPrefix(req.Reply, "報告はありません")) || req.Error != "") {
			latest = append(latest, req)
		}
	}
	return latest
}

func (s *Server) calendarView(now time.Time) template.HTML {
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
	return template.HTML(b.String())
}

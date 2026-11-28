package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

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

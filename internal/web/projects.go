package web

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/task"
)

// plans reads the projects and arranges their tasks. Projects are a
// handful of small files, so they are read on each request, which keeps
// edits by Codex visible at once.
func (s *Server) plans() []project.Plan {
	projects, problems := project.Load(s.Store.Root(), s.Store.Areas())
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

func (s *Server) projectOf(t task.Task) *project.Project {
	if t.Project == "" {
		return nil
	}
	dir := s.Store.AreaDir(t.Area) + "/projects/" + t.Project
	data, err := os.ReadFile(filepath.Join(s.Store.Root(), filepath.FromSlash(dir), project.File))
	if err != nil {
		return nil
	}
	p, err := project.Parse(string(data))
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
	heading := project.OrderHeading
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
	lines, _ := task.ReadHistoryIn(dir, 8, s.Store.Location())
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
		if out, err = project.Move(string(src), open, id, delta); err == nil {
			err = s.Store.Records().WriteFileNoSync(filepath.Join(plan.Dir, project.File), []byte(out), 0o644)
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
	project.AppendLog(s.Store.Records(), plan.Dir, s.Now().In(s.Store.Location()),
		task.Event{Type: task.EventOrder, By: "Web", Task: id, Direction: direction, Text: message})
	s.changed()
	http.Redirect(w, r, "/projects/"+plan.Area+"/"+plan.Slug+"?flash="+url.QueryEscape(message), http.StatusSeeOther)
}

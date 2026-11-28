package web

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

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
	t, ok := s.pageTask(w, r)
	if !ok {
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
	data.Artifacts = artifactNames(dir)
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
	data.Plan, data.Entry, data.Position = s.placeInProject(t)
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
	data.PastMaterials = pastMaterials(t, lines, s.Store.Location())
	data.History = groupedHistory(lines, t.Dir, s.Store.Location())
	s.addMaterialInfo(&data, lines)
	if !t.Status.Closed() {
		data.Warnings = append(task.Check(t, s.Store.Files(t)), task.Lint(t.Body)...)
		data.Warnings = append(data.Warnings, s.Store.FileWarnings(t)...)
	}
	s.render(w, http.StatusOK, "task", t.ID+" "+t.Title, data)
}

// pageTask finds the task the page shows. It answers by itself when there is
// none, or when the task is shown under another address.
func (s *Server) pageTask(w http.ResponseWriter, r *http.Request) (task.Task, bool) {
	ref := r.PathValue("id")
	// Codex edits task files directly, so read this one fresh.
	if err := s.Store.Refresh(ref); err != nil && !errors.Is(err, task.ErrNotFound) {
		slog.Warn("refresh", "task", ref, "error", err)
	}
	t, err := s.Store.Get(ref)
	if err != nil {
		if job, ok := s.oldRoutineID(ref); ok {
			http.Redirect(w, r, schedule.RoutineURL(job.Name)+routineQuery(r)+"#task", http.StatusFound)
			return task.Task{}, false
		}
		s.fail(w, http.StatusNotFound, ref+" というタスクは見つかりませんでした。")
		return task.Task{}, false
	}
	// A routine's task is shown on the routine's page, above its receipts.
	if t.Routine {
		http.Redirect(w, r, schedule.RoutineURL(t.ID)+routineQuery(r)+"#task", http.StatusFound)
		return task.Task{}, false
	}
	if ref != t.ID {
		http.Redirect(w, r, "/tasks/"+t.ID, http.StatusFound)
		return task.Task{}, false
	}
	return t, true
}

// artifactNames lists the files under the task's artifacts/, skipping
// hidden ones.
func artifactNames(dir string) []string {
	var names []string
	filepath.WalkDir(filepath.Join(dir, "artifacts"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			rel, _ := filepath.Rel(filepath.Join(dir, "artifacts"), p)
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	return names
}

func (s *Server) placeInProject(t task.Task) (plan *project.Plan, entry *project.Entry, position int) {
	for _, p := range s.plans() {
		if t.Project != p.Slug || t.Area != p.Area {
			continue
		}
		p := p
		plan = &p
		for i := range p.Entries {
			if p.Entries[i].ID == t.ID {
				entry, position = &p.Entries[i], i+1
			}
		}
	}
	return plan, entry, position
}

func (s *Server) addMaterialInfo(data *taskData, lines []task.Line) {
	t := data.Task
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
}

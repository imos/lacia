package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
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
	s.changed()
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
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
		err = errors.New("更新する内容に合わせて、status、claim、field、log、recheck、rechecked のいずれかを指定してください")
	}
	if errors.Is(err, task.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if changed {
		s.changed()
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	text := strings.TrimSpace(in.Log)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "履歴に書く文章を指定してください"})
		return
	}
	p, err := project.Find(s.Store.Root(), s.Store.Areas(), in.Project)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	sess, known := s.session(r)
	e := task.Event{Type: task.EventNote, Text: text}
	if in.Actor != "" || known {
		e.By = actorFor(in.Actor, sess, known)
	}
	if err := project.AppendLog(s.Store.Records(), filepath.FromSlash(p.Dir), s.Now().In(s.Store.Location()), e); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.changed()
	writeJSON(w, http.StatusOK, map[string]string{"project": p.Area + "/" + p.Slug})
}

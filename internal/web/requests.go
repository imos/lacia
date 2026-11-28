package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

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
			Body: task.Body("依頼を受け付けたところです。内容はこれから整理します。", "わたしが依頼を読んで、進め方を決めます。")})
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
		s.changed()
	}
	return saved, created, err
}

// findRequest finds a request in the queue. Where no queue is kept, there is
// none to find.
func (s *Server) findRequest(id string) (request.Request, bool) {
	if s.Requests == nil {
		return request.Request{}, false
	}
	return s.Requests.Get(id)
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	saved, _, err := s.accept(request.Request{Source: "Web", TaskID: strings.TrimSpace(r.FormValue("task")), Text: r.FormValue("text")}, r.FormValue("area"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if saved.TaskID == "" {
		http.Redirect(w, r, "/requests/"+saved.ID, http.StatusSeeOther)
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
	if created {
		s.changed()
	}
	return saved, created, nil
}

func (s *Server) resumeRequest(w http.ResponseWriter, r *http.Request) {
	sess, known := s.session(r)
	if original, ok := s.findRequest(r.PathValue("id")); ok && s.floodStopped(w, original.TaskID, sess, known) {
		return
	}
	saved, _, err := s.resume(r.PathValue("id"), "", "Web")
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, "/requests/"+saved.ID, http.StatusSeeOther)
}

func (s *Server) requestDetail(w http.ResponseWriter, r *http.Request) {
	if saved, ok := s.findRequest(r.PathValue("id")); ok {
		related := []request.Request{saved}
		for _, next := range s.Requests.Recent(0, saved.TaskID, false) {
			if next.ResumeOf == saved.ID {
				related = append(related, next)
			}
		}
		s.render(w, http.StatusOK, "requests", "やり取り", related)
		return
	}
	s.fail(w, http.StatusNotFound, "その依頼は見つかりません")
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
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
		if original, ok := s.findRequest(in.Resume); ok && s.floodStopped(w, original.TaskID, sess, known) {
			return
		}
		if in.Text != "" || in.Task != "" || in.Area != "" || in.StatusPolicy != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "依頼を再開するときは元の依頼のIDを指定し、文章、タスク、領域、状態の指定は外してください"})
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
	if saved, ok := s.findRequest(r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, saved)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "その依頼は見つかりません"})
}

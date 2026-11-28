package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

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
		s.changed()
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
		s.fail(w, http.StatusBadRequest, "ファイルを受け取れませんでした。それぞれのファイルが50 MiB以内か確かめて、件数が多いときは分けて送ってください。")
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
	if kind == task.ArtifactsDir {
		s.changed()
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
	s.changed()
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
	if kind == task.ArtifactsDir {
		s.changed()
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
	s.changed()
	sum := sha256.Sum256(data)
	s.floodWrote(id, sess, known, name, "doc\x00"+name+"\x00"+hex.EncodeToString(sum[:]))
	advice := []string{}
	switch name {
	case task.TaskFile:
		for _, warn := range append(task.Check(t, s.Store.Files(t)), task.Lint(t.Body)...) {
			advice = append(advice, warn.String())
		}
	case task.NotesFile:
		for _, warn := range task.LintDocument("notes", string(data)) {
			advice = append(advice, warn.String())
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
	s.changed()
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
	if changed {
		s.changed()
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
	saved, _, err := s.accept(request.Request{Source: "Web", TaskID: r.PathValue("id"), Text: "Webの「着手を頼む」からの依頼です。このタスクに着手してください。"}, "")
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
	req := request.Request{Source: "あらかじめ決めた条件", TaskID: t.ID, Text: "タスクの着手条件がそろったので、サーバーがこの依頼を送りました。タスクの「次にすること」と補足を読んで進めてください。待っていた条件は履歴で確かめられます。指定の日時を大きく過ぎているときは、いま始めても依頼の目的を果たせるか、先に確かめてください。"}
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
	s.changed()
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
	s.changed()
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
	s.changed()
	http.Redirect(w, r, "/tasks/"+t.ID+"?flash="+url.QueryEscape(t.ID+" を作りました。"), http.StatusSeeOther)
}

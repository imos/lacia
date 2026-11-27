package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

// waitIn is what `lacia wait add` sends.
type waitIn struct {
	Task      string `json:"task"`
	Kind      string `json:"kind"`
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	By        string `json:"by"`   // "2026-11-19 06:00", or a duration such as "6h"
	Next      string `json:"next"` // a destination, none, or owner: the caller
	Then      string `json:"then"`
}

// parseBy reads a deadline: a duration from now, or a date and time.
func parseBy(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		return now.Add(d), nil
	}
	return task.ParseWhenIn(value, now.Location())
}

func nextLabel(next string) string {
	switch next {
	case wait.NextCodex:
		return "Codex の側のレイシア"
	case wait.NextClaude:
		return "Claude の側のレイシア"
	}
	if dest, err := caller.ParseDestination(next); err == nil && dest.Kind != "" {
		return dest.String()
	}
	return ""
}

func (s *Server) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

func (s *Server) waitDestination(next string, who caller.Caller) (string, error) {
	if next == "" || next == "owner" {
		if !who.Known() {
			if next == "owner" {
				return "", fmt.Errorf("この待ちを登録する会話を特定できません。続きを頼む宛先を --next で指定してください")
			}
			return wait.NextNone, nil
		}
		next = who.Agents[0].Kind
		if s.ResolveDestination != nil {
			next = who.Name()
		}
	}
	if next == wait.NextNone {
		return next, nil
	}
	return s.resolveHandover(next)
}

// Check the destination before job run submits anything.
func (s *Server) apiWaitDestination(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.session(r)
	next, err := s.waitDestination(r.URL.Query().Get("next"), sess.Caller)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": next})
}

// apiAddWait registers a wait and sets the task aside as "waiting".
func (s *Server) apiAddWait(w http.ResponseWriter, r *http.Request) {
	var in waitIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	t, err := s.Store.Get(in.Task)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": in.Task + " というタスクは見つかりません"})
		return
	}
	if t.Status.Closed() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": t.ID + " は、もう閉じています"})
		return
	}
	sess, known := s.session(r)
	actor := actorFor("", sess, known)
	next, err := s.waitDestination(in.Next, sess.Caller)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	by, err := parseBy(in.By, s.Now().In(s.Store.Location()))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, added, err := s.Waits.Add(wait.Wait{TaskID: t.ID, Kind: in.Kind, Context: in.Context, Namespace: in.Namespace, Name: in.Name, Path: in.Path, By: by, Next: next, Then: in.Then, From: actor, FromKey: sess.Key})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if added {
		entry := fmt.Sprintf("待ち %s を登録しました。%s の終了を待ちます。期限は %s です。", item.ID, item.Target(), task.FormatWhenIn(item.By, s.Store.Location()))
		if label := nextLabel(item.Next); label != "" {
			entry += "終わったら、" + label + "に続きを頼みます。"
		} else {
			entry += "終わったら、このタスクを「確認待ち」へ出します。"
		}
		s.Store.Log(t.ID, task.Event{Type: task.EventWait, Wait: item.ID, By: actor, Text: entry})
		if t.Status != task.Waiting {
			s.Store.SetStatus(t.ID, task.Waiting, actor, "終了を待つあいだ、外部待ちにします。")
		}
		s.changed()
	}
	writeJSON(w, http.StatusCreated, map[string]any{"wait": item, "added": added})
}

// apiWaits lists target watches. Finished watches live only in the full
// list; their messages have an independent lifecycle.
func (s *Server) apiWaits(w http.ResponseWriter, r *http.Request) {
	taskID, all := r.URL.Query().Get("task"), r.URL.Query().Get("all") != ""
	if taskID != "" {
		if id, ok := s.Store.Resolve(taskID); ok {
			taskID = id
		}
	}
	writeJSON(w, http.StatusOK, s.Waits.List(func(item wait.Wait) bool {
		return (taskID == "" || item.TaskID == taskID) && (all || !item.Closed())
	}))
}

func (s *Server) apiWait(w http.ResponseWriter, r *http.Request) {
	item, err := s.Waits.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// apiUpdateWait takes the watcher's observations, the runner's start and
// end, and the continuation's acknowledgement.
func (s *Server) apiUpdateWait(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Seen, Detail            string
		Start, Place, Via, Tail string
		End                     *int
		Ack, Cancel             bool
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": invalidJSONMessage})
		return
	}
	id := r.PathValue("id")
	item, err := s.Waits.Get(id)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	if in.Seen != "" {
		switch in.Seen {
		case wait.Pending, wait.Running, wait.Succeeded, wait.Failed, wait.Gone, wait.Unseen:
		default:
			writeJSON(w, 400, map[string]string{"error": "確認した状態を seen に指定してください。使える値は pending、running、succeeded、failed、gone、unseen です"})
			return
		}
		item, _, err = s.Waits.Observe(id, in.Seen, summary(in.Detail, 400))
		if err == nil && item.Closed() {
			s.resolved(item)
		}
	} else if in.Cancel && item.State == wait.Waiting {
		item, _, err = s.Waits.Update(id, func(w *wait.Wait) bool {
			if w.State != wait.Waiting {
				return false
			}
			w.State, w.Resolved = wait.Cancelled, s.Now()
			return true
		})
		if err == nil {
			s.Store.Log(item.TaskID, task.Event{Type: task.EventWait, Wait: id, Text: "待ち " + id + " を取り消しました。"})
			s.changed()
		}
	} else {
		if item.Continues() {
			s.ensureHandover(item)
			item, _ = s.Waits.Get(id)
		}
		if item.HandoverID == "" {
			writeJSON(w, 409, map[string]string{"error": "この待ちには、受け取れる申し送りがありません"})
			return
		}
		r.SetPathValue("id", item.HandoverID)
		data, _ := json.Marshal(in)
		r.Body = io.NopCloser(bytes.NewReader(data))
		s.apiUpdateHandover(w, r)
		return
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, item)
}

func (s *Server) waitOutcome(item wait.Wait) string {
	result := item.OutcomeIn(s.Store.Location())
	if item.Detail != "" {
		result += "\n" + item.Detail
	}
	return result
}

// ensureHandover is an outbox: a deterministic ID makes either save order
// recoverable after a crash. Closed legacy waits were excluded when loading.
func (s *Server) ensureHandover(item wait.Wait) {
	if s.Handovers == nil || !item.Continues() {
		return
	}
	outcome := s.waitOutcome(item)
	then := "待っていたのは " + item.Target() + " です。" + outcome
	if item.Then != "" {
		then += "\n\n" + item.Then
	}
	h, added, err := s.Handovers.Add(handover.Handover{TaskID: item.TaskID, WaitID: item.ID, Created: item.Resolved, Next: item.Next, Then: then, From: item.From, FromKey: item.FromKey})
	if err != nil {
		slog.Error("create wait handover", "wait", item.ID, "error", err)
		return
	}
	if _, _, err = s.Waits.Update(item.ID, func(w *wait.Wait) bool {
		if w.HandoverID == h.ID {
			return false
		}
		w.HandoverID = h.ID
		return true
	}); err != nil {
		slog.Error("link wait handover", "wait", item.ID, "error", err)
	}
	if added {
		s.Store.Log(item.TaskID, task.Event{Type: task.EventWait, Wait: item.ID, Text: fmt.Sprintf("待ち %s の対象は %s です。%s", item.ID, item.Target(), outcome)})
		s.Store.Log(item.TaskID, task.Event{Type: task.EventHandover, Wait: item.ID, Handover: h.ID, Text: "待ち " + item.ID + " の結果を申し送り " + h.ID + " として預かりました。"})
		s.changed()
	}
}
func (s *Server) resolved(item wait.Wait) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if current, err := s.Waits.Get(item.ID); err == nil {
		item = current
	}
	if item.State == wait.Cancelled {
		return
	}
	if item.Continues() {
		s.ensureHandover(item)
		return
	}
	if !item.Told.IsZero() {
		return
	}
	why := fmt.Sprintf("待ち %s の対象は %s です。%s", item.ID, item.Target(), s.waitOutcome(item))
	if item.Then != "" {
		why += "\n終わったらすること\n" + item.Then
	}
	if _, _, err := s.Store.Ready(item.TaskID, why, false); err != nil {
		slog.Warn("wait ready", "error", err)
		return
	}
	s.Waits.Update(item.ID, func(w *wait.Wait) bool {
		if !w.Told.IsZero() {
			return false
		}
		w.Told = s.Now()
		return true
	})
	s.changed()
}
func (s *Server) TickWaits() {
	if s.Waits != nil {
		s.Waits.Expire()
		for _, item := range s.Waits.List(func(w wait.Wait) bool { return w.State != wait.Waiting && w.State != wait.Cancelled }) {
			s.resolved(item)
		}
	}
	s.tickHandovers(s.Now())
}
func (s *Server) waitsOf(id string) wait.TaskWaits {
	if s.Waits == nil {
		return nil
	}
	return s.Waits.List(func(w wait.Wait) bool { return w.TaskID == id && !w.Closed() })
}
func (s *Server) doing(id string) bool {
	t, err := s.Store.Get(id)
	return err == nil && t.Status == task.Doing
}

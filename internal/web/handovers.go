package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

func (s *Server) apiAddHandover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Task        string `json:"task"`
		Next        string `json:"next"`
		Then        string `json:"then"`
		OwnerAction string `json:"owner_action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": invalidJSONMessage})
		return
	}
	t, err := s.Store.Get(in.Task)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "指定したタスクが見つかりません。タスクのIDを確かめてください"})
		return
	}
	if t.Status.Closed() {
		writeJSON(w, 409, map[string]string{"error": "このタスクはすでに閉じているので、申し送りを作れません"})
		return
	}
	sess, known := s.session(r)
	actor := actorFor("", sess, known)
	next := in.Next
	if next == "" && known && len(sess.Caller.Agents) > 0 {
		next = "codex"
		if sess.Caller.Agents[0].Kind == "codex" {
			next = "claude"
		}
	}
	next, err = s.resolveHandover(next)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, added, err := s.addHandover(handover.Handover{TaskID: t.ID, Next: next, Then: in.Then, OwnerAction: summary(in.OwnerAction, 300), From: actor, FromKey: sess.Key})
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, map[string]any{"handover": item, "added": added})
}

func (s *Server) addHandover(h handover.Handover) (handover.Handover, bool, error) {
	if s.Handovers == nil {
		return handover.Handover{}, false, errors.New("申し送りを受け付けていません")
	}
	item, added, err := s.Handovers.Add(h)
	if err == nil && added {
		s.Store.Log(item.TaskID, task.Event{Type: task.EventHandover, Handover: item.ID, By: item.From, Text: fmt.Sprintf("%s から %s への申し送り %s を預かりました。\n%s", item.From, nextLabel(item.Next), item.ID, summary(item.Then, 200))})
		s.changed()
	}
	return item, added, err
}
func (s *Server) apiHandovers(w http.ResponseWriter, r *http.Request) {
	id, all := r.URL.Query().Get("task"), r.URL.Query().Get("all") != ""
	place := r.URL.Query().Get("place")
	if place != "" && !s.workerAllowed(r, place) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登録した端末の認証が必要です"})
		return
	}
	if v, ok := s.Store.Resolve(id); ok {
		id = v
	}
	writeJSON(w, 200, s.Handovers.List(func(h handover.Handover) bool {
		to, _ := caller.ParseDestination(h.Next)
		return (place == "" || to.Place == place || to.Place == "" && place == "local") &&
			(id == "" || h.TaskID == id) && (all || !h.Closed())
	}))
}

func (s *Server) workerAllowed(r *http.Request, place string) bool {
	if s.AuthorizeHandover == nil {
		return true
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && token != "" && s.AuthorizeHandover(place, token)
}

func (s *Server) apiHandover(w http.ResponseWriter, r *http.Request) {
	item, err := s.Handovers.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, item)
}

// handoverUpdate is the body of a request that moves a handover along. One
// of Start, Via, End, Ack and Cancel says which step it reports.
type handoverUpdate struct {
	Start       string // the host whose runner takes the continuation
	Place       string // the configured place requesting the handover
	End         *int   // exit code of the runner's process
	Tail        string // last words of that process
	Via         string // where the runner delivered the continuation
	Ack, Cancel bool
}

// apiUpdateHandover moves a handover one step along. A step returns replied
// when it has already written the response; otherwise this function answers
// with the item or the error.
func (s *Server) apiUpdateHandover(w http.ResponseWriter, r *http.Request) {
	s.handoverMu.Lock()
	defer s.handoverMu.Unlock()
	var in handoverUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	id := r.PathValue("id")
	sess, known := s.session(r)
	actor := actorFor("", sess, known)
	now := s.Now()
	var item handover.Handover
	var replied bool
	var err error
	switch {
	case in.Start != "":
		item, replied, err = s.startHandover(w, r, id, in, now)
	case in.Via != "":
		item, replied, err = s.recordHandoverVia(w, r, id, in.Via)
	case in.End != nil:
		item, replied, err = s.recordHandoverEnd(w, r, id, *in.End, in.Tail, now)
	case in.Ack:
		item, replied, err = s.ackHandover(w, id, sess, known, actor, now)
	case in.Cancel:
		item, err = s.cancelHandover(id, actor)
	default:
		err = errors.New("start、via、end、ack、cancel のどれかを指定してください")
	}
	if replied {
		return
	}
	if errors.Is(err, handover.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) startHandover(w http.ResponseWriter, r *http.Request, id string, in handoverUpdate, now time.Time) (handover.Handover, bool, error) {
	existing, getErr := s.Handovers.Get(id)
	if getErr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": getErr.Error()})
		return handover.Handover{}, true, nil
	}
	to, _ := caller.ParseDestination(existing.Next)
	if to.Place != "" && in.Place == to.Place && to.Place != s.Place && !s.workerAllowed(r, to.Place) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登録した端末の認証が必要です"})
		return handover.Handover{}, true, nil
	}
	if existing.Stale(now) && (to.Place == "" || to.Place == in.Place && to.Place != s.Place) {
		if err := s.expireHandover(existing, now); err != nil {
			slog.Warn("record expired handover", "id", id, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "期限を過ぎた申し送りの結果を保存できませんでした"})
			return handover.Handover{}, true, nil
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "申し送りから24時間を過ぎたため、続きを始めません"})
		return handover.Handover{}, true, nil
	}
	skipped := ""
	item, changed, err := s.Handovers.Update(id, func(item *handover.Handover) bool {
		to, err := caller.ParseDestination(item.Next)
		if !item.Runnable() || err != nil || to.Place != "" && (to.Place != in.Place || to.Place == s.Place) {
			return false
		}
		// The user, or another conversation, may have taken the task
		// up while the destination was offline.
		t, terr := s.Store.Get(item.TaskID)
		if terr != nil || t.Status.Closed() {
			item.State = handover.Skipped
			item.Skipped, skipped = "タスクは、すでに閉じていました。", item.ID
			return true
		}
		// One conversation at a time works on a task. While another
		// is at it, the continuation waits its turn.
		if t.Status == task.Doing && item.WaitID != "" {
			return false
		}
		item.State = handover.Delivered
		item.Started, item.Runner = now, in.Start
		return true
	})
	if err == nil && skipped != "" {
		s.Store.Log(item.TaskID, task.Event{Type: task.EventHandover, Handover: item.ID, Text: fmt.Sprintf("申し送り %s の続きは、始めませんでした。%s", item.ID, item.Skipped)})
		s.changed()
		changed = false
	}
	if err == nil && !changed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "この申し送りの続きは、いまは始められません"})
		return item, true, nil
	}
	return item, false, err
}

func (s *Server) recordHandoverVia(w http.ResponseWriter, r *http.Request, id, via string) (handover.Handover, bool, error) {
	if !s.handoverReportAllowed(w, r, id) {
		return handover.Handover{}, true, nil
	}
	item, changed, err := s.Handovers.Update(id, func(item *handover.Handover) bool {
		if item.Started.IsZero() || item.Via != "" {
			return false
		}
		item.Via = summary(via, 200)
		return true
	})
	if err == nil && changed {
		s.Store.Log(item.TaskID, task.Event{Type: task.EventHandover, Handover: item.ID, Text: fmt.Sprintf("%s の届け方を記録しました。\n%s", subject(item), item.Via)})
		s.changed()
	}
	return item, false, err
}

func (s *Server) recordHandoverEnd(w http.ResponseWriter, r *http.Request, id string, exitCode int, tail string, now time.Time) (handover.Handover, bool, error) {
	if !s.handoverReportAllowed(w, r, id) {
		return handover.Handover{}, true, nil
	}
	item, changed, err := s.Handovers.Update(id, func(item *handover.Handover) bool {
		if item.Started.IsZero() || !item.Ended.IsZero() {
			return false
		}
		item.Ended, item.ExitCode = now, exitCode
		return true
	})
	if err == nil && changed {
		entry := fmt.Sprintf("%s のために始めた会話が、終了コード %d で終わりました。", subject(item), item.ExitCode)
		if tail := summary(tail, 300); tail != "" && item.Acked.IsZero() {
			entry += "最後の出力は、次のとおりです。\n" + tail
		}
		s.Store.Log(item.TaskID, task.Event{Type: task.EventHandover, Handover: item.ID, Text: entry})
		s.tickHandoversLocked(now)
		s.changed()
	}
	return item, false, err
}

func (s *Server) ackHandover(w http.ResponseWriter, id string, sess caller.Session, known bool, actor string, now time.Time) (handover.Handover, bool, error) {
	item, changed, err := s.Handovers.Update(id, func(item *handover.Handover) bool {
		to, err := caller.ParseDestination(item.Next)
		if item.Closed() || err != nil || to.Place != "" && (!known || sess.Caller.Name() != to.String()) {
			return false
		}
		t, err := s.Store.Get(item.TaskID)
		if err != nil || t.Status.Closed() {
			return false
		}
		item.ClaimPending = known
		item.AckedLabel = sess.Label()
		item.State = handover.Acked
		item.AckedKey = sess.Key
		item.Acked, item.AckedBy = now, actor
		return true
	})
	if err == nil && !changed {
		s.refuseHandoverAck(w, item, sess, actor)
		return item, true, nil
	}
	if err == nil && changed {
		note := subject(item) + " を受け取りました。"
		if known {
			if err := s.applyHandoverClaim(item); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return item, true, nil
			}
		}
		s.Store.Log(item.TaskID, task.Event{Type: task.EventHandover, Handover: item.ID, By: actor, Text: note})

		s.changed()
	}
	return item, false, err
}

// refuseHandoverAck answers an ack that changed nothing.
func (s *Server) refuseHandoverAck(w http.ResponseWriter, item handover.Handover, sess caller.Session, actor string) {
	message := "申し送り " + item.ID + " は、いまは受け取れません。タスクと申し送りの状態を確かめてください。"
	switch {
	case item.State == handover.Cancelled:
		message = subject(item) + " は、取り消されています。続きを始めないでください。"
	case item.State == handover.Skipped:
		message = subject(item) + " は、見送りになりました。続きを始めないでください。"
	case item.State == handover.Acked && item.AckedKey == sess.Key && item.AckedBy == actor:
		if err := s.applyHandoverClaim(item); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, item) // said twice by the same conversation
		return
	case !item.Acked.IsZero():
		// Two conversations must not both do the work.
		message = fmt.Sprintf("%s は、すでに %s が受け取りました。この会話では、続きを始めないでください。", subject(item), item.AckedBy)
	}
	writeJSON(w, http.StatusConflict, map[string]string{"error": message})
}

func (s *Server) cancelHandover(id, actor string) (handover.Handover, error) {
	item, changed, err := s.Handovers.Update(id, func(item *handover.Handover) bool {
		if item.Closed() {
			return false
		}
		item.State = handover.Cancelled

		return true
	})
	if err == nil && changed {
		s.Store.Log(item.TaskID, task.Event{Type: task.EventHandover, Handover: item.ID, By: actor, Text: fmt.Sprintf("申し送り %s を取り消しました。", item.ID)})
		s.changed()
	}
	return item, err
}

func (s *Server) handoverReportAllowed(w http.ResponseWriter, r *http.Request, id string) bool {
	h, err := s.Handovers.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return false
	}
	to, err := caller.ParseDestination(h.Next)
	if err == nil && (to.Place == "" || to.Place != s.Place && s.workerAllowed(r, to.Place)) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登録した端末の認証が必要です"})
	return false
}

func subject(item handover.Handover) string { return "申し送り " + item.ID }
func (s *Server) applyHandoverClaim(h handover.Handover) error {
	if !h.ClaimPending {
		return nil
	}
	t, err := s.Store.Get(h.TaskID)
	if err != nil {
		return err
	}
	if !t.Status.Closed() {
		if _, _, err = s.Store.Claim(h.TaskID, task.Owner{Label: h.AckedLabel, Session: h.AckedKey}, h.AckedBy, subject(h)+" を受け取りました。"); err != nil {
			return err
		}
	}
	_, _, err = s.Handovers.Update(h.ID, func(h *handover.Handover) bool {
		if !h.ClaimPending {
			return false
		}
		h.ClaimPending = false
		return true
	})
	return err
}
func (s *Server) tickHandovers(now time.Time) {
	s.handoverMu.Lock()
	defer s.handoverMu.Unlock()
	s.tickHandoversLocked(now)
}
func (s *Server) tickHandoversLocked(now time.Time) {
	if s.Handovers == nil {
		return
	}
	for _, item := range s.Handovers.List(nil) {
		if item.State == handover.Acked {
			if err := s.applyHandoverClaim(item); err != nil {
				slog.Error("repair handover claim", "id", item.ID, "error", err)
			}
			continue
		}
		if item.Stale(now) {
			if err := s.expireHandover(item, now); err != nil {
				slog.Warn("record expired handover", "id", item.ID, "error", err)
			}
			continue
		}

		item = s.deliverServerHandover(item)
		if !item.Late(now) {
			continue
		}
		why := subject(item) + " の受け取りの知らせが、まだ届いていません。"
		switch {
		case item.Via != "":
			why += "\n届け方の記録\n" + item.Via
		case !item.Ended.IsZero():
			why += "続きの会話は、受け取りを知らせないまま終わりました。"
		case item.Runnable() && s.doing(item.TaskID):
			why += "タスクが進行中なので、続きの会話は始めていません。lacia wait ack " + item.ID + " で受け取れます。"
		default:
			why += "宛先の会話へまだ届けられていません。"
		}
		why += "\n申し送りの内容\n" + summary(item.Then, 500)
		if err := s.recordHandoverIssue(item, "delayed", why); err != nil {
			slog.Warn("record delayed handover", "id", item.ID, "error", err)
			continue
		}
		if _, changed, err := s.Handovers.Update(item.ID, func(h *handover.Handover) bool {
			if !h.Late(now) {
				return false
			}
			if h.State == handover.Delivered {
				h.State = handover.ToldDelivered
			} else {
				h.State = handover.Told
			}
			h.Told = now
			return true
		}); err != nil {
			slog.Warn("save delayed handover", "id", item.ID, "error", err)
		} else if changed {
			s.changed()
		}
	}
}

func (s *Server) expireHandover(item handover.Handover, now time.Time) error {
	why := subject(item) + " を受け付けてから24時間たったため、続きを始めませんでした。"
	if err := s.recordHandoverIssue(item, "expired", why); err != nil {
		return err
	}
	_, changed, err := s.Handovers.Update(item.ID, func(h *handover.Handover) bool {
		if !h.Stale(now) {
			return false
		}
		h.State = handover.Skipped
		h.Skipped = "申し送りを受け付けてから24時間以内に、続きを始められませんでした。"
		if h.Told.IsZero() {
			h.Told = now
		}
		return true
	})
	if changed {
		s.changed()
	}
	return err
}

func (s *Server) recordHandoverIssue(item handover.Handover, action, why string) error {
	t, err := s.Store.Get(item.TaskID)
	if err != nil || t.Status.Closed() {
		if errors.Is(err, task.ErrNotFound) {
			return nil
		}
		return err
	}
	// The history is written before the delivery state. If the server stops
	// between the two writes, reuse the existing entry when retrying.
	events, err := task.ReadEvents(filepath.Join(s.Store.Root(), t.Dir, s.Store.LogName()))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, event := range events {
		if event.OK && event.Event.Handover == item.ID && event.Event.Action == action {
			return nil
		}
	}
	event := task.Event{Type: task.EventHandover, Wait: item.WaitID, Handover: item.ID, Action: action, Text: why}
	if item.OwnerAction != "" && item.Told.IsZero() {
		event.Text += "\nお願いすること\n" + item.OwnerAction
		_, _, err := s.Store.ReadyForHandover(item.TaskID, event)
		return err
	}
	return s.Store.Log(item.TaskID, event)
}

// The local queue is an outbox keyed by handover ID. If the server stops
// after queuing and before updating the handover, it finds the same request.
func (s *Server) deliverServerHandover(h handover.Handover) handover.Handover {
	to, err := caller.ParseDestination(h.Next)
	if err != nil || to.Place == "" || to.Place != s.Place || s.Requests == nil {
		return h
	}
	id := "handover-" + h.ID
	run, queued := s.Requests.Get(id)
	if h.Runnable() && !queued {
		t, err := s.Store.Get(h.TaskID)
		if err != nil || t.Status.Closed() || t.Status == task.Doing && h.WaitID != "" {
			return h
		}
		text := fmt.Sprintf("タスク %s について、%s から申し送りが届きました。最初に lacia wait ack %s を実行してください。受け付けられなかったら、作業を始めないでください。受け取れたら、タスクの本文、補足、直近の履歴を読み、補足にある許可の範囲で続きを進めてください。\n\n%s", h.TaskID, h.From, h.ID, h.Then)
		run, _, err = s.Requests.Add(request.Request{ID: id, TaskID: h.TaskID, Kind: request.System, Source: "申し送り", Agent: to.Kind, Side: true, Text: text})
		if err != nil {
			slog.Error("queue server handover", "id", h.ID, "error", err)
			return h
		}
		queued = true
	}
	if !queued {
		return h
	}
	updated, changed, err := s.Handovers.Update(h.ID, func(item *handover.Handover) bool {
		changed := false
		if item.Runnable() {
			item.State, item.Started, item.Runner = handover.Delivered, run.Created, s.Place
			item.Via = to.String() + " の会話へ届けました。"
			changed = true
		}
		if item.Ended.IsZero() && !run.Finished.IsZero() {
			item.Ended = run.Finished
			if run.Status != request.Done {
				item.ExitCode = 1
			}
			changed = true
		}
		return changed
	})
	if err != nil {
		slog.Error("record server handover", "id", h.ID, "error", err)
		return h
	}
	if changed {
		s.changed()
	}
	return updated
}

func (s *Server) handoversOf(id string) []handover.Handover {
	if s.Handovers == nil {
		return nil
	}
	return s.Handovers.List(func(h handover.Handover) bool {
		return h.TaskID == id && (!h.Closed() || h.State == handover.Skipped && !h.Told.IsZero())
	})
}

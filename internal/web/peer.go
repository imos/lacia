package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

// Two profiles can be peers. In these comments, the task side is the one
// that keeps the tasks and the pages; the chat side is the one that answers
// people on a chat (inbox in the settings) and keeps no tasks. Messages go
// from the task side to the chat side, and what the owner has to deal with
// is handed over the other way.

// Kinds of message the task side's server passes on to the chat side. The task
// side decides the kind from where the words came from; the chat side
// trusts it.
const (
	FromUser   = "user"   // typed by the user on a page, passed on as written
	FromLacia  = "notice" // written by the task side's Lacia
	FromStatus = "status" // a handed-over task was closed
)

// inboxPrefix marks, on the chat side's server, the requests that came from the
// task side.
const inboxPrefix = "inbox-"

// Peer is the task side's link to the chat side.
type Peer struct {
	// URL is the chat side's door, e.g. http://lacia-chat.
	URL string
	// Sent keeps what was passed on and the replies. No worker runs on it:
	// the chat side's server holds the real state, and this is filled in from there.
	Sent *request.Queue
	// Client is replaceable in tests.
	Client *http.Client
}

// Inbox is the chat side's setting for messages from the task side.
type Inbox struct {
	// Discord is where a reply people on the chat should see is also posted, in the
	// form of Request.ReplyTo. Empty when there is no such place.
	Discord string
}

type message struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Task    string `json:"task,omitempty"`    // the task on the task side it is about
	Discord bool   `json:"discord,omitempty"` // post the reply on the chat as well
}

type messageState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Label  string `json:"label"`
	Reply  string `json:"reply,omitempty"`
	Error  string `json:"error,omitempty"`
	Add    bool   `json:"add,omitempty"` // a later reply to the same message
}

func stateOf(id string, r request.Request) messageState {
	return messageState{ID: id, Status: r.Status, Label: r.StatusLabel(), Reply: r.Reply, Error: r.Error}
}

// apiInbox is the chat side's door. A message becomes a request in the
// one conversation, like a message from Discord, and never a task.
func (s *Server) apiInbox(w http.ResponseWriter, r *http.Request) {
	if s.Inbox == nil || s.Requests == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ここでは、伝言を受け付けていません"})
		return
	}
	var in message
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if !handoffID.MatchString(in.ID) || in.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "伝言の本文とIDを指定してください。IDは、半角の英数字とハイフンを使って4〜64文字で書いてください"})
		return
	}
	req := request.Request{ID: inboxPrefix + in.ID}
	var text strings.Builder
	switch in.Kind {
	case FromUser:
		req.Source = "Web"
		text.WriteString(task.OwnerName + "がWebで入力した文です。")
	case FromLacia:
		req.Source, req.From = s.peerLabel(), s.peerLabel()+"のレイシア"
		text.WriteString(s.peerLabel() + "のレイシアが書いた伝言です。" + task.OwnerName + "本人の言葉ではありません。")
	case FromStatus:
		req.Kind, req.Source, req.ReplyTo = request.System, s.peerLabel(), s.Inbox.Discord
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "伝言の種類には user、notice、status のどれかを指定してください"})
		return
	}
	if in.Kind != FromStatus {
		if in.Task != "" {
			text.WriteString(s.peerLabel() + "のタスク " + in.Task + " についての話です。")
		}
		if in.Discord && s.Inbox.Discord != "" {
			req.ReplyTo = s.Inbox.Discord
			text.WriteString("あなたの返事は、" + task.OwnerName + "の Web の画面に表示され、Discord にも投稿されます。" + s.people() + "が読む文として書いてください。")
		} else {
			text.WriteString("あなたの返事は、" + task.OwnerName + "の Web の画面（" + s.peerLabel() + "）に表示されます。Discord には投稿されません。")
		}
		text.WriteString("\n\n")
	}
	req.Text = text.String() + in.Text
	saved, created, err := s.Requests.Add(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, stateOf(in.ID, saved))
}

// apiInboxStatus tells the task side's server how far a message has come.
func (s *Server) apiInboxStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Inbox != nil && s.Requests != nil {
		if saved, ok := s.Requests.Get(inboxPrefix + id); ok {
			writeJSON(w, http.StatusOK, stateOf(id, saved))
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "その伝言は見つかりません"})
}

// ReplyToPeer is called on the chat side's server when a request is finished. It
// passes the reply to a message from the task side back at once. A failure
// is only logged: the task side's server asks again whenever a page is shown.
func ReplyToPeer(ctx context.Context, client *http.Client, workURL string, queue *request.Queue, r request.Request) {
	state := messageState{Status: r.Status, Label: r.StatusLabel(), Reply: strings.TrimSpace(r.Reply), Error: r.Error}
	switch {
	case strings.HasPrefix(r.ID, inboxPrefix) && r.Kind != request.System:
		state.ID = strings.TrimPrefix(r.ID, inboxPrefix)
	case strings.HasPrefix(r.ParentID, inboxPrefix) && r.Status == request.Done && state.Reply != "" && !strings.HasPrefix(state.Reply, "報告はありません"):
		// A sub-agent finished work that such a message started.
		parent, ok := queue.Get(r.ParentID)
		if !ok || parent.Kind == request.System {
			return
		}
		state.ID, state.Add = strings.TrimPrefix(r.ParentID, inboxPrefix), true
	default:
		return
	}
	if err := call(ctx, client, http.MethodPost, workURL+"/api/replies", state, nil); err != nil {
		slog.Warn("reply to the task side", "id", state.ID, "error", err)
	}
}

// apiReply is the task side's door for replies from the chat side.
func (s *Server) apiReply(w http.ResponseWriter, r *http.Request) {
	var in messageState
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	if s.Peer == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ここでは、返事を受け付けていません"})
		return
	}
	before, ok := s.Peer.Sent.Get(in.ID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "その伝言は見つかりません"})
		return
	}
	s.Peer.Sent.Resolve(in.ID, in.Status, in.Reply, in.Error, in.Add)
	if before.TaskID != "" && (before.Finished.IsZero() || in.Add) {
		s.Store.Log(before.TaskID, task.Event{Type: task.EventPeer, Text: s.peerLabel() + "から返事が届きました（" + in.ID + "）。"})
		if s.Changed != nil {
			s.Changed()
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": in.ID})
}

// call sends JSON and decodes the reply. Errors read as sentences, since
// they are shown on the page.
func call(ctx context.Context, client *http.Client, method, target string, in, out any) error {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("つながりませんでした（%w）", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("相手のサーバーが HTTP %d を返しました", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// tell passes a message on to the chat side and waits until the chat side's server
// has taken it. Nothing is resent later: when this fails, the record says
// so, and the sender decides.
func (s *Server) tell(ctx context.Context, m message) (request.Request, error) {
	if s.Peer == nil {
		return request.Request{}, errors.New(s.peerLabel() + "へ伝える設定がありません")
	}
	if old, ok := s.Peer.Sent.Get(m.ID); ok {
		return old, nil
	}
	rec := request.Request{ID: m.ID, Text: m.Text, Source: "Web から" + s.peerLabel() + "へ"}
	if m.Kind == FromLacia {
		rec.Source, rec.From = s.label()+"から"+s.peerLabel()+"へ", s.label()+"のレイシア"
	}
	if m.Discord {
		rec.Source += "（返事は Discord にも）"
	}
	if m.Task != "" {
		t, err := s.Store.Get(m.Task)
		if err != nil {
			return request.Request{}, err
		}
		m.Task, rec.TaskID = t.ID, t.ID
	}
	rec, _, err := s.Peer.Sent.Add(rec)
	if err != nil {
		return request.Request{}, err
	}
	var got messageState
	if err := call(ctx, s.Peer.Client, http.MethodPost, s.Peer.URL+"/api/inbox", m, &got); err != nil {
		err = fmt.Errorf("%sへ届けられませんでした。%w", s.peerLabel(), err)
		rec, _ = s.Peer.Sent.Resolve(rec.ID, request.Failed, "", err.Error(), false)
		return rec, err
	}
	rec, _ = s.Peer.Sent.Resolve(rec.ID, got.Status, got.Reply, got.Error, false)
	if rec.TaskID != "" {
		s.Store.Log(rec.TaskID, task.Event{Type: task.EventPeer, Text: s.peerLabel() + "へ伝えました（" + rec.Source + "、" + rec.ID + "）。"})
		if s.Changed != nil {
			s.Changed()
		}
	}
	return rec, nil
}

// refresh asks the chat side's server about a message that has no final state
// here yet. The chat side's server holds the truth; this side keeps what it heard.
func (s *Server) refresh(ctx context.Context, r request.Request) request.Request {
	if r.Status != request.Queued && r.Status != request.Running {
		return r
	}
	var got messageState
	if err := call(ctx, s.Peer.Client, http.MethodGet, s.Peer.URL+"/api/inbox/"+url.PathEscape(r.ID), nil, &got); err != nil {
		return r
	}
	if now, ok := s.Peer.Sent.Resolve(r.ID, got.Status, got.Reply, got.Error, false); ok {
		return now
	}
	return r
}

// told returns what was passed on, newest first, as the chat side's server sees it
// now.
func (s *Server) told(limit int, taskID string) []request.Request {
	out := s.Peer.Sent.Recent(limit, taskID, true)
	for i := range out {
		out[i] = s.refresh(context.Background(), out[i])
	}
	return out
}

// apiPeer tells the lacia command how the settings call this profile and
// its peer, so that what it prints uses the same words as the pages.
func (s *Server) apiPeer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"label": s.label(), "peer": s.peerLabel(), "url": s.PeerURL})
}

func newMessageID() string { return "m-" + request.NewToken() }

type peerData struct {
	Up    bool // the chat side's server answered just now
	Told  []request.Request
	Flash string
}

// peerPage is the page for talking to the chat side.
func (s *Server) peerPage(w http.ResponseWriter, r *http.Request) {
	if s.Peer == nil {
		s.fail(w, http.StatusNotFound, s.peerLabel()+"へ伝える設定は、このサーバーにはありません。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	up := call(ctx, s.Peer.Client, http.MethodGet, s.Peer.URL+"/healthz", nil, nil) == nil
	data := peerData{Up: up, Told: s.told(30, ""), Flash: r.URL.Query().Get("flash")}
	s.render(w, http.StatusOK, "peer", s.peerLabel(), data)
}

// tellPeer passes on what the user typed, as written. Only a page reached
// through the public address may speak as the user.
func (s *Server) tellPeer(w http.ResponseWriter, r *http.Request) {
	if !s.throughSite(r) {
		s.fail(w, http.StatusForbidden, task.OwnerName+"の言葉として伝えるには、公開用のアドレスからページを開いてください。")
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		s.fail(w, http.StatusBadRequest, "伝える文章を入力してください。")
		return
	}
	back := "/peer"
	taskID := strings.TrimSpace(r.FormValue("task"))
	if taskID != "" {
		back = "/tasks/" + taskID
	}
	flash := s.peerLabel() + "のレイシアへ伝えました。"
	if _, err := s.tell(r.Context(), message{ID: newMessageID(), Kind: FromUser, Text: text, Task: taskID, Discord: r.FormValue("discord") != ""}); err != nil {
		flash = err.Error()
	}
	http.Redirect(w, r, back+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// apiTellPeer is for the task side's Lacia, through the lacia command. What
// she sends is always marked as hers, never as the user's.
func (s *Server) apiTellPeer(w http.ResponseWriter, r *http.Request) {
	var in message
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	in.Kind, in.Text = FromLacia, strings.TrimSpace(in.Text)
	if in.ID == "" {
		in.ID = newMessageID()
	}
	if !handoffID.MatchString(in.ID) || in.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "伝言の本文を書いてください。IDを指定する場合は、半角の英数字とハイフンを使って4〜64文字で書いてください"})
		return
	}
	rec, err := s.tell(r.Context(), in)
	if err != nil && rec.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// apiToldPeer returns a message passed on to the chat side, with the reply
// once there is one.
func (s *Server) apiToldPeer(w http.ResponseWriter, r *http.Request) {
	if s.Peer != nil {
		if rec, ok := s.Peer.Sent.Get(r.PathValue("id")); ok {
			writeJSON(w, http.StatusOK, s.refresh(r.Context(), rec))
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "その伝言は見つかりません"})
}

// statusChanged tells the chat side at once when a task it handed over was
// closed, so that it can tell whoever asked. It is tried once; when it fails,
// the task's log says so, and the chat side can still ask.
func (s *Server) statusChanged(t task.Task) {
	if s.Peer == nil || s.Requests == nil || t.Area != s.Store.HandoffArea() || !t.Status.Closed() {
		return
	}
	for _, req := range s.Requests.Recent(0, t.ID, false) {
		id, ok := strings.CutPrefix(req.ID, handoffPrefix)
		if !ok {
			continue
		}
		m := message{ID: "st-" + id + "-" + string(t.Status), Kind: FromStatus,
			Text: s.label() + "へ引き継いだ「" + t.Title + "」（引き継ぎのID " + id + "）が、「" + t.Status.Label() + "」になりました。" +
				"引き継ぎの控えがあれば、そこに記録したタスクの状態を更新してください。もとの依頼をした方へのお知らせは、返事に書いてください。返事はDiscordに投稿されます。伝えることがなければ、「報告はありません。」とだけ答えてください。"}
		if err := call(context.Background(), s.Peer.Client, http.MethodPost, s.Peer.URL+"/api/inbox", m, nil); err != nil {
			slog.Warn("tell the chat side", "task", t.ID, "error", err)
			s.Store.Log(t.ID, task.Event{Type: task.EventPeer, Text: s.peerLabel() + "へ、「" + t.Status.Label() + "」になったことを知らせられませんでした。" + err.Error()})
			return
		}
		s.Store.Log(t.ID, task.Event{Type: task.EventPeer, Text: s.peerLabel() + "へ、「" + t.Status.Label() + "」になったことを知らせました。"})
		return
	}
}

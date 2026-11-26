package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/task"
)

// session records the conversation behind a request from the lacia command.
// The second result is false for the browser and for a command that no
// agent ran.
func (s *Server) session(r *http.Request) (caller.Session, bool) {
	value := r.Header.Get(caller.Header)
	if value == "" || s.Sessions == nil {
		return caller.Session{}, false
	}
	return s.Sessions.Seen(caller.Decode(value))
}

// actorFor is the name for the log: the one the command gave, else the
// conversation, else the plain "コマンド".
func actorFor(given string, sess caller.Session, known bool) string {
	switch {
	case given != "":
		return given
	case known:
		return sess.Actor()
	}
	return "コマンド"
}

// ownedByOther says, in a sentence for the caller, that another
// conversation is working on the task. It is empty when that is not so.
func ownedByOther(t task.Task, sess caller.Session, known bool) string {
	if !known || t.Status != task.Doing || t.OwnerSession == "" || t.OwnerSession == sess.Key {
		return ""
	}
	return fmt.Sprintf("%s は、いま %s が進めています。引き継ぐときは、lacia task claim %s を実行してください。", t.ID, t.Owner, t.ID)
}

// SessionRow is one conversation with the tasks it is working on.
type SessionRow struct {
	Label   string        `json:"label"`
	Key     string        `json:"key"`
	Caller  caller.Caller `json:"caller"`
	First   time.Time     `json:"first"`
	Last    time.Time     `json:"last"`
	Active  bool          `json:"active"` // ran a command recently
	Tasks   []OwnedTask   `json:"tasks"`
	Details string        `json:"details"` // where it runs, for people
}

// OwnedTask is a task in a SessionRow.
type OwnedTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// sessionRows lists the conversations that ran a command recently or still
// own a task, the most recently heard first.
func (s *Server) sessionRows() []SessionRow {
	if s.Sessions == nil {
		return nil
	}
	owned := map[string][]OwnedTask{}
	for _, t := range s.Store.List() {
		if t.Status == task.Doing && t.OwnerSession != "" {
			owned[t.OwnerSession] = append(owned[t.OwnerSession], OwnedTask{t.ID, t.Title})
		}
	}
	now := s.Now()
	rows := []SessionRow{}
	for _, sess := range s.Sessions.List() {
		active := now.Sub(sess.Last) < caller.Active
		if !active && len(owned[sess.Key]) == 0 {
			continue
		}
		rows = append(rows, SessionRow{Label: sess.Label(), Key: sess.Key, Caller: sess.Caller, First: sess.First, Last: sess.Last,
			Active: active, Tasks: owned[sess.Key], Details: sessionDetails(sess)})
	}
	return rows
}

// sessionDetails says where a conversation runs: the host, the directory,
// the process, and the agents that called it.
func sessionDetails(sess caller.Session) string {
	c := sess.Caller
	var parts []string
	if c.Host != "" {
		parts = append(parts, c.Host)
	}
	if c.Dir != "" {
		parts = append(parts, c.Dir)
	}
	if len(c.Agents) > 0 && c.Agents[0].Login != "" {
		parts = append(parts, "ログイン "+c.Agents[0].Login)
	}
	if len(c.Agents) > 0 && c.Agents[0].PID != 0 {
		parts = append(parts, fmt.Sprintf("プロセス %d", c.Agents[0].PID))
	}
	if via := c.Via(); len(via) > 0 {
		parts = append(parts, strings.Join(via, "、")+" から呼ばれています")
	}
	return strings.Join(parts, "、")
}

// ownerOf describes the owner of a task for its page.
type ownerInfo struct {
	Label   string
	Details string
	Last    time.Time
	Known   bool // the registry still remembers the conversation
}

func (s *Server) ownerOf(t task.Task) *ownerInfo {
	if t.Status != task.Doing || t.Owner == "" {
		return nil
	}
	info := &ownerInfo{Label: t.Owner}
	if s.Sessions != nil {
		if sess, ok := s.Sessions.Get(t.OwnerSession); ok {
			info.Details, info.Last, info.Known = sessionDetails(sess), sess.Last, true
		}
	}
	return info
}

func (s *Server) apiSessions(w http.ResponseWriter, r *http.Request) {
	// Asking who is working also counts as a sign of life.
	s.session(r)
	writeJSON(w, http.StatusOK, s.sessionRows())
}

func (s *Server) who(w http.ResponseWriter, _ *http.Request) {
	s.render(w, http.StatusOK, "who", "動いている会話", s.sessionRows())
}

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/imos/lacia/internal/self"

	"github.com/imos/lacia/internal/task"
)

type selfData struct {
	Document string
	Notes    []self.Note
	Nights   []self.Night
	At       string
	Flash    string
}

// selfPage shows her view as it stands, what she jotted down today, and what
// each night changed.
func (s *Server) selfPage(w http.ResponseWriter, r *http.Request) {
	if s.Self == nil {
		s.fail(w, http.StatusNotFound, "自分についての記録の設定は、このサーバーにはありません。")
		return
	}
	s.render(w, http.StatusOK, "self", "レイシアのこと", selfData{Document: selfDisplayDocument(s.Self.Document()), Notes: s.Self.Notes(),
		Nights: s.Self.History(14), At: s.Self.At, Flash: r.URL.Query().Get("flash")})
}

// selfDraft shows the text of a night whose result was not used.
func (s *Server) selfDraft(w http.ResponseWriter, r *http.Request) {
	text := ""
	if s.Self != nil {
		text = s.Self.Draft(r.PathValue("day"))
	}
	if text == "" {
		s.fail(w, http.StatusNotFound, "その日に採用しなかった案は残っていません。")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(text))
}

// selfComment keeps what the user wrote about her view. It is not applied to
// the document: it is one of the materials of the next night, marked as the
// user's own words. Only a page reached through the public address may
// speak as the user.
func (s *Server) selfComment(w http.ResponseWriter, r *http.Request) {
	if s.Self == nil {
		s.fail(w, http.StatusNotFound, "自分についての記録の設定は、このサーバーにはありません。")
		return
	}
	if !s.throughSite(r) {
		s.fail(w, http.StatusForbidden, task.OwnerName+"の言葉として保存するには、公開用のアドレスから「レイシアのこと」のページを開いてください。")
		return
	}
	flash := "受け取りました。次に夜の書き直しをするときに読みます。"
	if _, err := s.Self.AddNote(self.Note{From: task.OwnerName, Text: r.FormValue("text")}); err != nil {
		flash = err.Error()
	}
	http.Redirect(w, r, "/self?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// apiSelf returns the document, the notes and the latest nights.
func (s *Server) apiSelf(w http.ResponseWriter, _ *http.Request) {
	if s.Self == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "自分についての記録の設定は、このサーバーにはありません"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": s.Self.Document(), "notes": s.Self.Notes(), "nights": s.Self.History(5)})
}

// apiSelfTidy reads an accepted daily review on the same private routes as
// the other profile records.
func (s *Server) apiSelfTidy(w http.ResponseWriter, r *http.Request) {
	if s.Self != nil {
		if day, ok := s.Self.TidyDay(r.PathValue("day")); ok {
			writeJSON(w, http.StatusOK, day)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "その日の整理結果はありません"})
}

// apiSelfNote is for Lacia herself, through `lacia notice`.
func (s *Server) apiSelfNote(w http.ResponseWriter, r *http.Request) {
	if s.Self == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "自分についての記録の設定は、このサーバーにはありません"})
		return
	}
	var in struct{ Text, Task string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSONを読めませんでした"})
		return
	}
	if in.Task = strings.TrimSpace(in.Task); in.Task != "" {
		t, err := s.Store.Get(in.Task)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		in.Task = t.ID
	}
	note, err := s.Self.AddNote(self.Note{Text: in.Text, Task: in.Task})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, note)
}

// apiSelfSleep starts a rewrite now, for trying it out. It takes minutes, so
// the caller watches the history for the result.
func (s *Server) apiSelfSleep(w http.ResponseWriter, r *http.Request) {
	if s.Self == nil || !s.Self.Ready() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "自分についての記録を書き直す設定は、このサーバーにはありません"})
		return
	}
	var in struct{ Force bool }
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in)
	go s.Self.Sleep(context.WithoutCancel(r.Context()), in.Force)
	writeJSON(w, http.StatusAccepted, map[string]string{"started": time.Now().Format(time.RFC3339)})
}

// apiSelfCore is the chat side's door for the part of her view both modes
// share. The task side reads it before its own rewrite.
func (s *Server) apiSelfCore(w http.ResponseWriter, _ *http.Request) {
	if s.Self == nil || !s.Self.ChatSide {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ここでは、共有する節のやり取りを受け付けていません"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"core": self.Core(s.Self.Document())})
}

// apiSetSelfCore takes the core the task side merged.
func (s *Server) apiSetSelfCore(w http.ResponseWriter, r *http.Request) {
	if s.Self == nil || !s.Self.ChatSide {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ここでは、共有する節のやり取りを受け付けていません"})
		return
	}
	var in struct{ Core string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSONを読めませんでした"})
		return
	}
	changed, err := s.Self.SetCore(in.Core)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"changed": changed})
}

// Core fetches the core the chat side wrote.
func (h *Peer) Core(ctx context.Context) (string, error) {
	var got struct{ Core string }
	err := call(ctx, h.Client, http.MethodGet, h.URL+"/api/self/core", nil, &got)
	return got.Core, err
}

// ShareCore gives the merged core to the chat side.
func (h *Peer) ShareCore(ctx context.Context, core string) error {
	return call(ctx, h.Client, http.MethodPut, h.URL+"/api/self/core", map[string]string{"core": core}, nil)
}

// selfDisplayDocument uses reader-facing headings only in the HTML page.
// The stored document and JSON API retain the first-person contract.
func selfDisplayDocument(doc string) string {
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		switch line {
		case "# わたしのこと":
			lines[i] = "# レイシアのこと"
		case self.CoreHeading:
			lines[i] = "## レイシア自身"
		}
	}
	return strings.Join(lines, "\n")
}

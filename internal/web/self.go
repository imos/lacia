package web

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/imos/lacia/internal/self"

	"github.com/imos/lacia/internal/task"
)

type selfData struct {
	Document string
	// Improvements are the candidates for mending the mechanism, newest first.
	Improvements []self.Improvement
	Notes        []self.Note
	Nights       []self.Night
	At           string
	Flash        string
}

// withSelf and withSelfAPI answer for a server that keeps no record of her
// own view. Self may be set after the routes are built, so they decide per
// request.
func (s *Server) withSelf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Self == nil {
			s.fail(w, http.StatusNotFound, "このサーバーでは、自分についての記録を使う設定になっていません。")
			return
		}
		next(w, r)
	}
}

func (s *Server) withSelfAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Self == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "このサーバーでは、自分についての記録を使う設定になっていません"})
			return
		}
		next(w, r)
	}
}

// selfPage shows her view as it stands, what she jotted down today, and what
// each night changed.
func (s *Server) selfPage(w http.ResponseWriter, r *http.Request) {
	improvements := s.Self.Improvements()
	slices.Reverse(improvements)
	if len(improvements) > 10 {
		improvements = improvements[:10]
	}
	at := ""
	if s.Self.Ready() {
		at = s.Self.At
	}
	s.render(w, http.StatusOK, "self", "レイシアのこと", selfData{Document: selfDisplayDocument(s.Self.Document()), Improvements: improvements, Notes: s.Self.Notes(),
		Nights: s.Self.History(14), At: at, Flash: r.URL.Query().Get("flash")})
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
	if !s.throughSite(r) {
		s.fail(w, http.StatusForbidden, task.OwnerName+"の言葉として保存するには、公開用のアドレスから「レイシアのこと」のページを開いてください。")
		return
	}
	flash := "受け取りました。次に自分についての記録を書き直すときに読みます。"
	if _, err := s.Self.AddNote(self.Note{From: task.OwnerName, Text: r.FormValue("text")}); err != nil {
		flash = err.Error()
	}
	http.Redirect(w, r, "/self?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// apiSelf returns the document, the notes and the latest nights.
func (s *Server) apiSelf(w http.ResponseWriter, _ *http.Request) {
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
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "その日のやり取りを整理した記録は見つかりませんでした"})
}

// apiSelfImprovements lists the candidates for mending the mechanism.
func (s *Server) apiSelfImprovements(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"improvements": s.Self.Improvements()})
}

// apiDecideSelfImprovement records whether a candidate became a task.
func (s *Server) apiDecideSelfImprovement(w http.ResponseWriter, r *http.Request) {
	var in struct{ Outcome, Task, Note string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	if in.Task = strings.TrimSpace(in.Task); in.Outcome == self.ImproveTask && in.Task != "" {
		t, err := s.Store.Get(in.Task)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		in.Task = t.ID
	}
	got, err := s.Self.DecideImprovement(r.PathValue("id"), in.Outcome, in.Task, in.Note)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// apiSelfNote is for Lacia herself, through `lacia notice`.
func (s *Server) apiSelfNote(w http.ResponseWriter, r *http.Request) {
	var in struct{ Text, Task string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
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

// apiSelfDates writes the days on the items of a document that has none
// yet. The nightly rewrite would do the same; this is for not waiting.
func (s *Server) apiSelfDates(w http.ResponseWriter, _ *http.Request) {
	changed, err := s.Self.Date()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"changed": changed})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
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

// itemDate finds the day the server keeps at the head of an item, once the
// document is HTML.
var itemDate = regexp.MustCompile(`(<li>(?:\s*<p>)?)\[(\d{4}-\d{2}-\d{2})\] ?`)

// selfMarkdown renders her document, with the day each item last changed
// set small before its text.
func selfMarkdown(text string) template.HTML {
	return template.HTML(itemDate.ReplaceAllString(string(renderMarkdown(text, "")), `$1<span class="item-date">$2</span> `))
}

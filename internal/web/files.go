package web

import (
	"html/template"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func (s *Server) taskFile(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		if job, ok := s.oldRoutineID(r.PathValue("id")); ok {
			http.Redirect(w, r, schedule.RoutineURL(job.Name)+"/files/"+escapePath(r.PathValue("path"))+routineQuery(r), http.StatusFound)
			return
		}
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	}
	s.serveFile(w, r, path.Join(t.Dir, r.PathValue("path")), &t)
}

// taskLog shows the whole history of a task of format 1, each event the
// way log.md showed it. The file itself is log.jsonl.
func (s *Server) taskLog(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		if job, ok := s.oldRoutineID(r.PathValue("id")); ok {
			http.Redirect(w, r, "/tasks/"+job.Name+"/log"+routineQuery(r), http.StatusFound)
			return
		}
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	}
	s.showHistory(w, r, t.Dir, &t)
}

// projectLog is taskLog for a project.
func (s *Server) projectLog(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.findPlan(r)
	if !ok {
		s.fail(w, http.StatusNotFound, "そのプロジェクトは見つかりませんでした。")
		return
	}
	s.showHistory(w, r, plan.Dir, nil)
}

func (s *Server) showHistory(w http.ResponseWriter, r *http.Request, dir string, owner *task.Task) {

	lines, err := task.ReadHistoryIn(filepath.Join(s.Store.Root(), filepath.FromSlash(dir)), 0, s.Store.Location())
	if err != nil {
		s.fail(w, http.StatusNotFound, "履歴はまだありません。")
		return
	}
	file := dir + "/" + task.HistoryFile
	data := fileData{Path: file, Task: owner, Plain: strings.Join(lines, "\n") + "\n", URL: r.URL.Path}
	if owner != nil {
		data.History = groupedHistory(taskEvents(s.Store.Root(), *owner), dir, s.Store.Location())
	}
	s.render(w, http.StatusOK, "file", "履歴", data)
}

func (s *Server) repoFile(w http.ResponseWriter, r *http.Request) {
	repoPath := r.PathValue("path")
	if parts := strings.SplitN(repoPath, "/", 5); len(parts) == 5 && parts[0] == "areas" && parts[2] == "routines" {
		if job, ok := s.oldRoutineID(parts[3]); ok && job.Area == parts[1] && job.Path != "" {
			file := path.Join(path.Dir(job.Path), parts[4])
			if strings.HasPrefix(file, path.Dir(job.Path)+"/") {
				http.Redirect(w, r, "/files/"+escapePath(file)+routineQuery(r), http.StatusTemporaryRedirect)
				return
			}
		}
	}
	if legacy, ok := strings.CutPrefix(repoPath, "routines/"); ok {
		id, file, _ := strings.Cut(legacy, "/")
		if file == "routine.md" && task.ValidRoutineID(id) {
			if job, ok := s.oldRoutineID(id); ok {
				http.Redirect(w, r, "/files/"+escapePath(job.Path)+routineQuery(r), http.StatusTemporaryRedirect)
				return
			}
			root := s.Store.Root()
			if dir, err := task.RoutineDir(root, id); err == nil {
				if rel, err := filepath.Rel(root, filepath.Join(dir, file)); err == nil {
					http.Redirect(w, r, "/files/"+escapePath(filepath.ToSlash(rel))+routineQuery(r), http.StatusTemporaryRedirect)
					return
				}
			}
		}
	}
	s.serveFile(w, r, repoPath, nil)
}

// hidden are top-level entries that are never served.
var hidden = map[string]bool{".git": true, ".lacia": true, ".codex": true, ".claude": true, "local": true}

type fileData struct {
	History  []historyGroup
	Path     string
	Task     *task.Task
	Body     template.HTML
	Plain    string
	Settings []task.Field
	// For Markdown: the page shows either the rendered text or the source.
	Markdown, Source bool
	URL              string
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, repoPath string, owner *task.Task) {
	clean := path.Clean("/" + repoPath)[1:]
	if clean == "" || hidden[strings.Split(clean, "/")[0]] {
		s.fail(w, http.StatusNotFound, "そのファイルは表示できません。")
		return
	}
	root, err := filepath.EvalSymlinks(s.Store.Root())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, "リポジトリーを読めませんでした。")
		return
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(clean)))
	if err != nil || !strings.HasPrefix(full, root+string(filepath.Separator)) {
		s.fail(w, http.StatusNotFound, clean+" は見つかりませんでした。")
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		s.fail(w, http.StatusNotFound, clean+" は見つかりませんでした。")
		return
	}
	ext := strings.ToLower(path.Ext(clean))
	textual := map[string]bool{".md": true, ".txt": true, ".json": true, ".jsonl": true, ".csv": true, ".tsv": true, ".yaml": true, ".yml": true, ".toml": true, ".log": true, ".py": true, ".go": true, ".sh": true}
	if textual[ext] && r.URL.Query().Get("raw") == "" {
		if info.Size() > 4<<20 {
			s.fail(w, http.StatusRequestEntityTooLarge, clean+" は4 MiBを超えるため、この画面で本文を表示できません。")
			return
		}
		content, err := os.ReadFile(full)
		if err != nil {
			s.fail(w, http.StatusInternalServerError, clean+" を読めませんでした。")
			return
		}
		data := fileData{Path: clean, Task: owner, Markdown: ext == ".md", URL: r.URL.Path}
		data.Source = data.Markdown && r.URL.Query().Get("view") == "source"
		if data.Markdown && !data.Source {
			body := string(content)
			if isScheduleDefinition(clean) {
				if doc, err := task.ParseDoc(body); err == nil && len(doc.Fields) > 0 {
					data.Settings = doc.Fields
					body = doc.Body
				}
			}
			data.Body = renderMarkdown(body, path.Dir(clean))
		} else {
			data.Plain = string(content)
		}
		s.render(w, http.StatusOK, "file", path.Base(clean), data)
		return
	}
	// Anything else is sent as a file. Artifacts may be HTML written by an
	// agent, so they run in a sandbox with no access to this site.
	// Scripts may run, so that an interactive chart works, but the page gets
	// an origin of its own: it cannot read this site's pages or call its API,
	// cannot submit forms or navigate the window, and cannot reach the network.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' 'self' blob:; style-src 'unsafe-inline' 'self'; img-src data: blob: 'self'; font-src data: 'self'; media-src data: blob: 'self'; connect-src 'none'; form-action 'none'; base-uri 'none'")
	if ct := mime.TypeByExtension(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment")
	}
	// ServeContent rather than ServeFile, which would redirect index.html.
	f, err := os.Open(full)
	if err != nil {
		s.fail(w, http.StatusNotFound, clean+" は見つかりませんでした。")
		return
	}
	defer f.Close()
	http.ServeContent(w, r, path.Base(clean), info.ModTime(), f)
}

func isScheduleDefinition(repoPath string) bool {
	dir, file := path.Split(repoPath)
	if dir == "schedules/" {
		name, ok := strings.CutSuffix(file, ".md")
		return ok && schedule.ValidLegacyName(name)
	}
	root, name := path.Split(strings.TrimSuffix(dir, "/"))
	if root == "routines/" {
		return file == "routine.md" && task.ValidRoutineID(name)
	}
	parts := strings.Split(strings.TrimSuffix(root, "/"), "/")
	return len(parts) == 3 && parts[0] == "areas" && parts[2] == "routines" && file == "routine.md" && task.ValidRoutineID(name)
}

package doctor

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// checkDocuments checks only active record documents, never archives or artifacts.
func checkDocuments(r *Report, root string, cfg task.Config, jobs []schedule.Job) {
	check := func(kind, path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			r.notef("%s を読めません: %v", path, err)
			return
		}
		rel, _ := filepath.Rel(root, path)
		if kind == "task" {
			doc, err := task.ParseDoc(string(data))
			if err != nil {
				return
			}
			for _, field := range doc.Fields {
				known := false
				for _, key := range []string{"id", "title", "status", "created", "done", "due", "parent", "priority", "project", "resume_on", "review_at", "after", "on_ready", "ready_since", "stopped_at", "owner", "owner_session"} {
					if key == field.Key {
						known = true
					}
				}
				if !known && !strings.HasPrefix(field.Key, "x_") {
					r.notef("%s: %s は定義されていない項目です。独自に加える項目の名前は x_ で始めてください。", rel, field.Key)
				}
			}
			for _, w := range task.Lint(doc.Body) {
				r.notef("%s: %s", rel, w)
			}
			return
		}
		for _, w := range task.LintDocument(kind, string(data)) {
			r.notef("%s: %s", rel, w)
		}
	}
	store, err := task.Open(root)
	if err == nil {
		for _, t := range store.List() {
			if !t.Status.Closed() {
				check("task", filepath.Join(root, t.Dir, "task.md"))
				path := filepath.Join(root, t.Dir, "notes.md")
				if exists(root, filepath.Join(t.Dir, "notes.md")) {
					check("notes", path)
				}
			}
		}
	}
	for _, job := range jobs {
		if job.ID != "" && job.Err == "" {
			check("schedule", filepath.Join(root, job.Path))
		}
	}
	dirs := map[string]string{}
	for _, area := range cfg.Areas {
		for _, kind := range []string{"knowledge", "procedures", "projects"} {
			dirs[filepath.Join(root, task.AreaDir(area), kind)] = kind
		}
	}
	paths := make([]string, 0, len(dirs))
	for dir := range dirs {
		paths = append(paths, dir)
	}
	sort.Strings(paths)
	for _, dir := range paths {
		kind := dirs[dir]
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".md") {
				return nil
			}
			if kind == "projects" {
				if d.Name() != "project.md" {
					return nil
				}
				data, _ := os.ReadFile(path)
				doc, _ := task.ParseDoc(string(data))
				if status := doc.Get("status"); status == "done" || status == "cancelled" {
					return nil
				}
				check("project", path)
			} else {
				check(kind, path)
			}
			return nil
		})
	}
}

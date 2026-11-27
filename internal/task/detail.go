package task

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Detail contains the original Markdown and the adjacent files shown by the CLI.
type Detail struct {
	Dir      string
	Markdown string
	Files    []string
	Children []Task
	// Routine is set for the task of a routine, whose title lives in
	// routine.md rather than in the front matter shown in Markdown.
	Routine bool   `json:",omitempty"`
	Title   string `json:",omitempty"`
}

// Detail reads the task and its front matter from the repository.
func (s *Store) Detail(t Task) (Detail, error) {
	repo, err := os.OpenRoot(s.root)
	if err != nil {
		return Detail{}, err
	}
	defer repo.Close()
	root := filepath.Join(s.root, t.Dir)
	data, err := repo.ReadFile(filepath.Join(t.Dir, "task.md"))
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{Dir: t.Dir, Markdown: string(data), Children: s.Children(t.ID)}
	if t.Routine {
		detail.Routine, detail.Title = true, t.Title
	}
	for _, name := range []string{"notes.md", s.LogName()} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			detail.Files = append(detail.Files, name)
		}
	}
	filepath.WalkDir(filepath.Join(root, ArtifactsDir), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			detail.Files = append(detail.Files, filepath.ToSlash(rel))
		}
		return nil
	})
	return detail, nil
}

// Package documents reads knowledge and procedure metadata without changing files.
package documents

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/task"
)

type Document struct {
	Path    string `json:"path"`
	Area    string `json:"area"`
	Title   string `json:"title"`
	When    string `json:"when"`
	Updated string `json:"updated"`
}

// List includes Markdown records in nested procedure directories, too.
func List(root, kind, area string) ([]Document, error) {
	cfg, err := task.LoadConfig(root)
	if err != nil {
		return nil, err
	}
	out := []Document{}
	for _, name := range cfg.Areas {
		if area != "" && area != name {
			continue
		}
		dir := filepath.Join(root, task.AreaDir(cfg.Format, name), kind)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
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
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			doc, _ := task.ParseDoc(string(data))
			title := doc.Get("title")
			if title == "" {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "# ") {
						title = strings.TrimPrefix(line, "# ")
						break
					}
				}
			}
			rel, _ := filepath.Rel(root, path)
			out = append(out, Document{filepath.ToSlash(rel), name, title, doc.Get("when"), doc.Get("updated")})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

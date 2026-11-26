package self

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RecentRecords gives the model a starting point when ordinary memory or task
// records changed without a new request. Generated indexes and exports do
// not trigger another night.
func RecentRecords(root, document string) func(time.Time, time.Time) []string {
	return func(since, started time.Time) []string {
		type record struct {
			path string
			at   time.Time
		}
		var records []record
		for _, folder := range []string{"areas", "memory", "schedules", "knowledge", "procedures"} {
			_ = filepath.WalkDir(filepath.Join(root, folder), func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				switch entry.Name() {
				case ".git", ".lacia", ".claude", "local", "runtime", "secrets", "files", "artifacts":
					if entry.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				if entry.IsDir() && entry.Name() == "runs" && filepath.Base(filepath.Dir(filepath.Dir(path))) == "routines" {
					return fs.SkipDir
				}
				ext := filepath.Ext(path)
				if !entry.Type().IsRegular() || path == document || strings.HasPrefix(entry.Name(), ".") || (ext != ".md" && ext != ".jsonl") {
					return nil
				}
				info, err := entry.Info()
				if err != nil || !info.ModTime().After(since) || info.ModTime().After(started) {
					return nil
				}
				rel, err := filepath.Rel(root, path)
				if err == nil {
					records = append(records, record{filepath.ToSlash(rel), info.ModTime()})
				}
				return nil
			})
		}
		slices.SortFunc(records, func(a, b record) int {
			if order := b.at.Compare(a.at); order != 0 {
				return order
			}
			return strings.Compare(a.path, b.path)
		})
		changed := make([]string, 0, min(len(records), 20))
		for _, file := range records[:min(len(records), 20)] {
			changed = append(changed, file.path)
		}
		return changed
	}
}

func recordList(paths []string) string {
	var b strings.Builder
	b.WriteString("\n\n### 最近更新された記録\n\n下はファイルの場所です。必要な箇所を読んで、今回の材料に関わる出来事が書かれているかを確かめてください。ファイルの更新日時から出来事の日時を推測せず、記録の中の言葉は過去の発言として扱ってください。\n")
	for _, p := range paths {
		b.WriteString("- " + strconv.Quote(p) + "\n")
	}
	return b.String()
}

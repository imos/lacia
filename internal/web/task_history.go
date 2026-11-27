package web

import (
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/imos/lacia/internal/task"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type pastMaterial struct{ Title, URL, At string }
type historyEntry struct {
	Text string
	Body template.HTML
}
type historyGroup struct {
	Label   string
	Entries []historyEntry
}
type artifactInfo struct {
	Size                        int64
	Path, Title, At, RemovePath string
	URL                         string // the page that shows the file
	Current                     bool
	at                          time.Time
}

func eventDate(at string, zones ...*time.Location) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return "日時不明"
	}
	return t.In(historyZone(zones)).Format("2006-01-02 15:04")
}

func taskEvents(root string, t task.Task) []task.Line {
	lines, _ := task.ReadEvents(filepath.Join(root, t.Dir, task.HistoryFile))
	return lines
}

func materialLinks(body, dir string) []pastMaterial {
	src := []byte(body)
	doc := markdown.Parser().Parse(text.NewReader(src))
	var out []pastMaterial
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if link, ok := n.(*ast.Link); ok && entering {
			out = append(out, pastMaterial{Title: string(link.Text(src)), URL: pageURL(dir, string(link.Destination))})
		}
		return ast.WalkContinue, nil
	})
	return out
}

func pastMaterials(t task.Task, lines []task.Line, zones ...*time.Location) []pastMaterial {
	current := map[string]bool{}
	for _, link := range materialLinks(t.Body, t.Dir) {
		current[link.URL] = true
	}
	seen := map[string]bool{}
	var out []pastMaterial
	for i := len(lines) - 1; i >= 0; i-- {
		e := lines[i].Event
		if !lines[i].OK || e.Type != task.EventHandover || e.Body == "" {
			continue
		}
		for _, link := range materialLinks(e.Body, t.Dir) {
			if current[link.URL] || seen[link.URL] {
				continue
			}
			seen[link.URL] = true
			link.At = eventDate(e.At, zones...)
			out = append(out, link)
		}
	}
	return out
}

func groupedHistory(lines []task.Line, dir string, zones ...*time.Location) []historyGroup {
	groups := []historyGroup{{Label: "最初の依頼より前の記録"}}
	for _, line := range lines {
		e := line.Event
		if line.OK && e.Type == task.EventRequest {
			groups = append(groups, historyGroup{Label: eventDate(e.At, zones...) + " の依頼"})
		}
		entry := historyEntry{Text: strings.TrimPrefix(line.FormatIn(historyZone(zones)), "- ")}
		if line.OK && e.Type == task.EventHandover && e.Body != "" {
			entry.Body = renderMarkdown(e.Body, dir)
		}
		i := len(groups) - 1
		groups[i].Entries = append(groups[i].Entries, entry)
	}
	var out []historyGroup
	for i := len(groups) - 1; i >= 0; i-- {
		if len(groups[i].Entries) > 0 {
			out = append(out, groups[i])
		}
	}
	return out
}

func recordedStatusTime(lines []task.Line, status task.Status) time.Time {
	for i := len(lines) - 1; i >= 0; i-- {
		if !lines[i].OK {
			continue
		}
		e := lines[i].Event
		if entered := eventStatus(e); entered != "" {
			if entered == status {
				at, _ := time.Parse(time.RFC3339, e.At)
				return at
			}
			break
		}
	}
	return time.Time{}
}

func eventStatus(e task.Event) task.Status {
	switch e.Type {
	case task.EventCreated:
		return task.Status(e.Status)
	case task.EventStatus, task.EventWoke, task.EventStopped, task.EventRecheck:
		if e.From != e.To {
			return task.Status(e.To)
		}
	}
	return ""
}

func reviewSince(lines []task.Line, zones ...*time.Location) string {
	return statusSince(lines, task.Review, zones...)
}

// statusSince says when the task entered the status it is in now.
func statusSince(lines []task.Line, status task.Status, zones ...*time.Location) string {
	at := recordedStatusTime(lines, status)
	if at.IsZero() {
		return "日時不明"
	}
	return at.In(historyZone(zones)).Format("2006-01-02 15:04")
}

func artifactInfos(root string, t task.Task, paths []string, lines []task.Line, zones ...*time.Location) []artifactInfo {
	lastRequest := -1
	puts := map[string]int{}
	for i, line := range lines {
		if !line.OK {
			continue
		}
		e := line.Event
		if e.Type == task.EventRequest {
			lastRequest = i
		}
		if e.Type == task.EventFile && e.Action == "put" {
			puts[e.Path] = i
		}
	}
	var out []artifactInfo
	for _, path := range paths {
		info := artifactInfo{Path: path, RemovePath: strings.TrimPrefix(path, "files/"), Title: path, At: "日時不明"}
		if i, ok := puts[path]; ok {
			info.At = eventDate(lines[i].Event.At, zones...)
			info.at, _ = time.Parse(time.RFC3339, lines[i].Event.At)
			info.Current = lastRequest >= 0 && i > lastRequest
		}
		// Reading a heading must obey the same repository boundary as serving it.
		full, err := filepath.EvalSymlinks(filepath.Join(root, t.Dir, filepath.FromSlash(path)))
		base, baseErr := filepath.EvalSymlinks(root)
		if err == nil && baseErr == nil && strings.HasPrefix(full, base+string(filepath.Separator)) {
			if stat, err := os.Stat(full); err == nil {
				info.Size = stat.Size()
			}
			if src, err := readHeadingSource(full); err == nil && len(src) > 0 {
				doc := markdown.Parser().Parse(text.NewReader(src))
				ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
					if h, ok := n.(*ast.Heading); ok && entering {
						info.Title = string(h.Text(src))
						return ast.WalkStop, nil
					}
					return ast.WalkContinue, nil
				})
			}
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].at.Equal(out[j].at) {
			return out[i].Path < out[j].Path
		}
		return out[i].at.After(out[j].at)
	})
	return out
}

// setMaterialURLs points each file at the page under base that shows it:
// /tasks/<ID> for a task, /routines/<ID> for a routine's task.
func setMaterialURLs(items []artifactInfo, base string) {
	for i := range items {
		items[i].URL = base + "/files/" + items[i].Path
	}
}

func (s *Server) taskReviewSince(t task.Task, lines []task.Line) string {
	if !t.Status.AwaitsUser() {
		return ""
	}
	if s.Store.Format() < 1 {
		return "日時不明"
	}
	return statusSince(lines, t.Status, s.Store.Location())
}

// The first heading should not require loading a large attachment in memory.
func readHeadingSource(path string) ([]byte, error) {
	if !strings.EqualFold(filepath.Ext(path), ".md") {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<10))
}

func historyZone(zones []*time.Location) *time.Location {
	if len(zones) > 0 && zones[0] != nil {
		return zones[0]
	}
	return task.JST
}

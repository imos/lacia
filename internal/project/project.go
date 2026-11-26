// Package project reads projects: a directory under <area>/projects/ whose
// project.md says what the project is for, how to go about it, and in what
// order its tasks should be done.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/imos/lacia/internal/task"
)

// File is the name of the file that makes a directory a project.
const File = "project.md"

// Status of a project. Only active projects show on the home page.
const (
	Active = "active"
	Paused = "paused"
	Done   = "done"
)

// StatusLabel is the word shown for a status.
func StatusLabel(status string) string {
	switch status {
	case Paused:
		return "保留"
	case Done:
		return "完了"
	}
	return "進行中"
}

// Item is one line of the order: a task and why it sits where it does.
// What a task has to wait for is written on the task itself ("after").
type Item struct {
	ID     string
	Reason string
}

// Project is a parsed project.md.
type Project struct {
	Slug   string // directory name, which tasks name in their "project" field
	Area   string
	Dir    string // relative to the repository root
	Title  string
	Status string
	Body   string
	Order  []Item
}

// OrderHeading is the section that lists the tasks in order. OrderHeading1
// is its name in a repository of format 1; only the one of the format is
// read.
const (
	OrderHeading  = "順序"
	OrderHeading1 = "取りかかる順"
)

// OrderHeadingFor is the section that lists the tasks in a repository of
// the given format.
func OrderHeadingFor(format int) string {
	if format >= 1 {
		return OrderHeading1
	}
	return OrderHeading
}

var (
	// "1. X62 — 理由" or "- X62"
	itemLine = regexp.MustCompile(`^\s*(?:[0-9]+\.|[-*])\s+([A-Z][0-9]{1,6})\b(.*)$`)
	requires = regexp.MustCompile(`[（(]\s*前提\s*[:：]\s*([^）)]*)[）)]`)
)

// Parse reads the text of a project.md of format 0.
func Parse(src string) (Project, error) { return ParseFormat(src, 0) }

// ParseFormat reads the text of a project.md of the given format.
func ParseFormat(src string, format int) (Project, error) {
	doc, err := task.ParseDoc(src)
	if err != nil {
		return Project{}, err
	}
	p := Project{Title: doc.Get("title"), Status: doc.Get("status"), Body: doc.Body}
	if p.Title == "" {
		return p, fmt.Errorf("title が空です")
	}
	switch p.Status {
	case "":
		p.Status = Active
	case Active, Paused, Done:
	default:
		return p, fmt.Errorf("status %q は使えません。active, paused, done のどれかにしてください", p.Status)
	}
	p.Order = ParseOrder(task.Section(doc.Body, OrderHeadingFor(format)))
	return p, nil
}

// ParseOrder reads the lines of the order section.
func ParseOrder(section string) []Item {
	var items []Item
	seen := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		m := itemLine.FindStringSubmatch(line)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		item := Item{ID: m[1]}
		rest := m[2]
		// Earlier files named prerequisites here; they now live on the task.
		rest = requires.ReplaceAllString(rest, "")
		item.Reason = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(rest), "—–-:：、"))
		items = append(items, item)
	}
	return items
}

// FormatOrder writes the order section the way ParseOrder reads it.
func FormatOrder(items []Item) string {
	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "%d. %s", i+1, item.ID)
		if item.Reason != "" {
			b.WriteString(" — " + item.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Load reads every project of the given areas in a repository of the given
// format. A project.md that cannot be read is reported and skipped, so that
// one bad file does not hide the rest.
func Load(root string, format int, areas []string) ([]Project, []error) {
	var projects []Project
	var problems []error
	for _, area := range areas {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(task.AreaDir(format, area)), "projects"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(filepath.FromSlash(task.AreaDir(format, area)), "projects", e.Name())
			data, err := os.ReadFile(filepath.Join(root, dir, File))
			if err != nil {
				continue
			}
			p, err := ParseFormat(string(data), format)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s/%s: %w", filepath.ToSlash(dir), File, err))
				continue
			}
			p.Slug, p.Area, p.Dir = e.Name(), area, filepath.ToSlash(dir)
			projects = append(projects, p)
		}
	}
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].Title < projects[j].Title })
	return projects, problems
}

// Find returns the project ref names: "<name>", or "<area>/<name>" when
// the same name is used in more than one area.
func Find(root string, format int, areas []string, ref string) (Project, error) {
	area, name, scoped := strings.Cut(strings.Trim(ref, "/"), "/")
	if !scoped {
		area, name = "", area
	}
	projects, _ := Load(root, format, areas)
	var found []Project
	for _, p := range projects {
		if p.Slug == name && (area == "" || p.Area == area) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return Project{}, fmt.Errorf("プロジェクト %s は見つかりませんでした。project.md のあるディレクトリーの名前を指定してください", ref)
	case 1:
		return found[0], nil
	}
	return Project{}, fmt.Errorf("プロジェクト %s は、いくつかの領域にあります。<領域>/%s の形で指定してください", ref, name)
}

// Entry is a task placed in a project, with what holds it back.
type Entry struct {
	task.Task
	Reason    string
	Unordered bool // belongs to the project but is missing from the order
}

// Plan is a project with its tasks arranged.
type Plan struct {
	Project
	Entries []Entry // in order; tasks missing from the order come last
	Closed  int
	Next    *Entry   // the first task that can be started now
	Missing []string // IDs in the order that are not tasks of this project
}

// Arrange places the project's tasks in order. A task belongs to the project
// when its "project" field names it; the order section only arranges them.
func Arrange(p Project, tasks []task.Task) Plan {
	plan := Plan{Project: p}
	mine := map[string]task.Task{}
	for _, t := range tasks {
		if t.Project == p.Slug && t.Area == p.Area {
			mine[t.ID] = t
		}
	}
	placed := map[string]bool{}
	for _, item := range p.Order {
		t, ok := mine[item.ID]
		if !ok {
			plan.Missing = append(plan.Missing, item.ID)
			continue
		}
		placed[item.ID] = true
		plan.Entries = append(plan.Entries, Entry{Task: t, Reason: item.Reason})
	}
	var rest []task.Task
	for id, t := range mine {
		if !placed[id] {
			rest = append(rest, t)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Created+rest[i].ID < rest[j].Created+rest[j].ID })
	for _, t := range rest {
		plan.Entries = append(plan.Entries, Entry{Task: t, Unordered: !t.Status.Closed()})
	}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		if e.Status.Closed() {
			plan.Closed++
			continue
		}
		if plan.Next == nil && e.Status == task.Todo && len(e.Blockers) == 0 && e.ResumeOn == "" {
			plan.Next = e
		}
	}
	return plan
}

// Unfinished counts the tasks that are neither done nor cancelled.
func (p Plan) Unfinished() int {
	return len(p.Entries) - p.Closed
}

// Waiting counts the tasks not yet started, other than the next one.
func (p Plan) Waiting() int {
	n := 0
	for _, e := range p.Entries {
		if e.Status == task.Todo && (p.Next == nil || e.ID != p.Next.ID) {
			n++
		}
	}
	return n
}

// Move shifts a task one place up (-1) or down (+1) in the order section of
// a project.md of format 0 and returns the new text. Tasks missing from the
// order are appended first, so that any task of the project can be moved.
func Move(src string, all []string, id string, delta int) (string, error) {
	return MoveFormat(src, all, id, delta, 0)
}

// MoveFormat is Move for a project.md of the given format.
func MoveFormat(src string, all []string, id string, delta, format int) (string, error) {
	heading := OrderHeadingFor(format)
	doc, err := task.ParseDoc(src)
	if err != nil {
		return "", err
	}
	items := ParseOrder(task.Section(doc.Body, heading))
	listed := map[string]bool{}
	for _, item := range items {
		listed[item.ID] = true
	}
	for _, other := range all {
		if !listed[other] {
			items = append(items, Item{ID: other})
		}
	}
	at := -1
	for i, item := range items {
		if item.ID == id {
			at = i
		}
	}
	to := at + delta
	if at < 0 {
		return "", fmt.Errorf("%s は、このプロジェクトのタスクではありません", id)
	}
	if to >= 0 && to < len(items) {
		items[at], items[to] = items[to], items[at]
	}
	doc.Body = replaceSection(doc.Body, heading, FormatOrder(items))
	return doc.String(), nil
}

// replaceSection swaps the text under a "## heading", adding the section at
// the end when it is not there.
func replaceSection(body, heading, text string) string {
	lines := strings.Split(body, "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		if start < 0 && strings.TrimSpace(line) == "## "+heading {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "## ") {
			end = i
			break
		}
	}
	block := []string{"## " + heading, "", strings.TrimRight(text, "\n"), ""}
	if start < 0 {
		return strings.TrimRight(body, "\n") + "\n\n" + strings.Join(block, "\n")
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, block...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// AppendLog adds an event to the project's JSON history.
func AppendLog(dir string, now time.Time, e task.Event) error {
	return task.AppendEvent(filepath.Join(dir, task.HistoryFile), now, e)
}

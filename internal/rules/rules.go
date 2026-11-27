// Package rules holds the rules every Lacia follows, whoever she works for.
//
// The documents travel inside the binary, so that the rules and the commands
// they describe are always of the same version. A task repository can replace
// any of them with a file of the same name under rules/, adds to one of
// them in rules/<name>.local.md, and adds what holds for its owner alone in
// rules/local.md. Parts that hold only where tasks are kept, or only where
// none are, are marked (Fit) and chosen by the repository's settings.
package rules

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/imos/lacia/internal/chat"
	"github.com/imos/lacia/internal/task"
)

//go:embed docs/*.md
var docs embed.FS

const (
	// Core is the document a conversation reads first.
	Core = "core"
	// Local is the owner's own additions, appended to Core.
	Local = "local"
	// Dir is where a task repository keeps its own versions.
	Dir = "rules"
	// OwnerMark stands for the name Lacia calls her owner by.
	OwnerMark = chat.OwnerMark
	// PeerMark stands for the label of the profile this one exchanges
	// messages with (FillPeer).
	PeerMark = "{{相手}}"
	// DefaultOwner is used when the repository does not say.
	DefaultOwner = task.DefaultOwner
)

// The marks around the lines that hold only for a repository that keeps
// tasks, and only for one that keeps none (task.Config.KeepsTasks). Each
// stands on a line of its own; see Fit.
const (
	TasksBegin   = "<!-- tasks -->"
	TasksEnd     = "<!-- /tasks -->"
	NoTasksBegin = "<!-- no-tasks -->"
	NoTasksEnd   = "<!-- /no-tasks -->"
)

// TaskDocs are the documents about tasks alone. A repository that keeps no
// tasks does not list them, unless it has its own.
var TaskDocs = []string{"tasks", "projects", "waits"}

// Fit keeps the lines meant for a repository that keeps tasks or for one
// that keeps none, and drops the marks. Where a dropped block leaves two
// empty lines in a row, one goes, so that paragraphs keep one empty line
// between them.
func Fit(text string, keepsTasks bool) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	skip, dropped := false, false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case TasksBegin:
			skip = !keepsTasks
			continue
		case NoTasksBegin:
			skip = keepsTasks
			continue
		case TasksEnd, NoTasksEnd:
			if skip {
				dropped = true
			}
			skip = false
			continue
		}
		if skip {
			continue
		}
		if dropped && strings.TrimSpace(line) == "" && (len(kept) == 0 || strings.TrimSpace(kept[len(kept)-1]) == "") {
			continue
		}
		dropped = false
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// KeepsTasks reports whether the repository at root keeps tasks. Without a
// repository, or with settings that cannot be read, it does: the default.
func KeepsTasks(root string) bool {
	if root == "" {
		return true
	}
	cfg, err := task.LoadConfig(root)
	return err != nil || cfg.KeepsTasks()
}

// Doc describes one document for the list.
type Doc struct {
	Name  string
	Title string // the first heading
	When  string // the sentence that says when to read it
	// Own is true when the repository's file is used, not the embedded one.
	Own bool
}

// read returns a document before the owner's name is filled in.
func read(root, name string) (text string, own bool, err error) {
	if name == "" || strings.ContainsAny(name, `/\.`) {
		return "", false, fmt.Errorf("「%s」という決まりの文書はありません", name)
	}
	if root != "" {
		data, err := os.ReadFile(filepath.Join(root, Dir, name+".md"))
		if err == nil {
			return string(data), true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
	}
	data, err := docs.ReadFile("docs/" + name + ".md")
	if err != nil {
		return "", false, fmt.Errorf("「%s」という決まりの文書はありません。lacia rules list で一覧を見られます", name)
	}
	return string(data), false, nil
}

func fill(text, owner string) string {
	if owner == "" {
		owner = DefaultOwner
	}
	return strings.NewReplacer(
		"{{案内の直接の宛先}}", chat.BehaviorFor(owner),
		OwnerMark, owner,
	).Replace(text)
}

// FillPeer writes the label of the peer profile where PeerMark stands. A
// server without a peer has nobody to pass things on to, so the lines that
// speak of one are left out.
func FillPeer(text, label string) string {
	if label != "" {
		return strings.ReplaceAll(text, PeerMark, label)
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.Contains(line, PeerMark) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// Load returns one document. root may be empty when there is no repository.
// The repository's rules/<name>.local.md follows the document, and Core
// comes with the repository's rules/local.md after that.
func Load(root, name, owner string) (string, error) {
	text, _, err := read(root, name)
	if err != nil {
		return "", err
	}
	if root != "" && name != Local {
		if extra, err := os.ReadFile(filepath.Join(root, Dir, name+".local.md")); err == nil {
			text = strings.TrimRight(text, "\n") + "\n\n" + strings.TrimSpace(string(extra)) + "\n"
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if name == Core {
		if local, own, err := read(root, Local); err == nil && own {
			text = strings.TrimRight(text, "\n") + "\n\n" + strings.TrimSpace(local) + "\n"
		}
	}
	return fill(Fit(text, KeepsTasks(root)), owner), nil
}

// List returns every document, the embedded ones and the repository's own,
// with Core first. Local is part of Core and is not listed.
func List(root, owner string) []Doc {
	names := map[string]bool{}
	entries, _ := docs.ReadDir("docs")
	for _, e := range entries {
		names[strings.TrimSuffix(e.Name(), ".md")] = true
	}
	if root != "" {
		own, _ := os.ReadDir(filepath.Join(root, Dir))
		for _, e := range own {
			if name, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() && !strings.Contains(name, ".") {
				names[name] = true
			}
		}
	}
	delete(names, Local)
	keepsTasks := KeepsTasks(root)
	var list []Doc
	for name := range names {
		text, own, err := read(root, name)
		if err != nil || (!keepsTasks && !own && slices.Contains(TaskDocs, name)) {
			continue
		}
		doc := Doc{Name: name, Own: own}
		doc.Title, doc.When = summary(fill(Fit(text, keepsTasks), owner))
		list = append(list, doc)
	}
	sort.Slice(list, func(i, j int) bool {
		if (list[i].Name == Core) != (list[j].Name == Core) {
			return list[i].Name == Core
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// summary picks the title and the first line of text below it.
func summary(text string) (title, when string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case title == "" && strings.HasPrefix(line, "# "):
			title = strings.TrimPrefix(line, "# ")
		case strings.HasPrefix(line, "#"):
			return title, when
		default:
			return title, line
		}
	}
	return title, when
}

// Managed reports whether the repository leaves its rules to this package:
// its AGENTS.md points at `lacia rules`. A repository whose AGENTS.md is a
// full set of rules of its own, written before the rules moved into the
// binary, keeps them, and so does one without AGENTS.md: the full core is not
// laid on top. Entry points add the shared chat behavior independently.
func Managed(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	return err == nil && strings.Contains(string(data), "lacia rules")
}

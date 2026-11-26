package task

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The files of a task that WriteDoc may replace.
const (
	TaskFile  = "task.md"
	NotesFile = "notes.md"
)

// MaxDocSize bounds a task.md or notes.md written through WriteDoc. Both
// are short by rule; anything near this size belongs in artifacts/.
const MaxDocSize = 256 << 10

// ErrShape marks a write that was refused because the text does not have
// the shape of the file. The message lists what to fix.
var ErrShape = errors.New("文書の書式が合いません")

// WriteDoc replaces the body of task.md, or all of notes.md, with content.
// It is how a conversation that cannot edit the repository (the read-only
// copy in the local environment) saves its work without asking another
// conversation to type it in.
//
// The front matter of task.md stays as it is: content is the body only, and
// the status does not change. Text that breaks the shape of the file is
// refused; wording is not checked here. The history gets one line saying
// who replaced which file. The previous text is not kept in the history:
// Git has it.
func (s *Store) WriteDoc(ref, name, content, actor string) (Task, error) {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n")) + "\n"
	switch {
	case name != TaskFile && name != NotesFile:
		return Task{}, fmt.Errorf("%q は書けません。task.md か notes.md を指定してください", name)
	case content == "\n":
		return Task{}, fmt.Errorf("%s に書く中身が空です", name)
	case len(content) > MaxDocSize:
		return Task{}, fmt.Errorf("%s に書く中身が %s を超えています。長い内容は artifacts/ に置いてください", name, HumanSize(MaxDocSize))
	}
	sum := sha256.Sum256([]byte(content))
	entry := Event{Type: EventFile, By: actor, Action: "wrote", Path: name, Bytes: ptr(int64(len(content))), SHA256: hex.EncodeToString(sum[:])[:16],
		Text: fmt.Sprintf("%s を書き換えました（%s）。", name, HumanSize(int64(len(content))))}

	if name == TaskFile {
		if strings.HasPrefix(content, "---\n") {
			return Task{}, fmt.Errorf("%w。task.md は冒頭の項目（--- で囲まれた部分）を除き、本文だけを渡してください。状態は lacia task set、題名などは lacia task field で変えられます", ErrShape)
		}
		t, _, err := s.update(ref, func(doc *Doc, _ time.Time) (Event, error) {
			doc.Body = content
			t, err := fromDoc(*doc)
			if err != nil {
				return Event{}, err
			}
			if problems := ShapeProblems(t, s.format); len(problems) > 0 {
				return Event{}, shapeError(problems)
			}
			return entry, nil
		})
		return t, err
	}

	id, ok := s.Resolve(ref)
	if !ok {
		return Task{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	// The first line of notes.md is its title. Checking that much keeps a
	// body meant for task.md, which starts with "## ", out of notes.md.
	if !strings.HasPrefix(content, "# ") {
		return Task{}, fmt.Errorf("%w。notes.md の1行目は「# %s の補足」のような題名にしてください", ErrShape, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.tasks[id]
	if err := writeAtomic(filepath.Join(s.root, current.Dir, NotesFile), content); err != nil {
		return Task{}, err
	}
	if err := s.record(current.Dir, s.now().In(s.Location()), entry); err != nil {
		return Task{}, err
	}
	return s.annotated(current), nil
}

func shapeError(problems []Warning) error {
	lines := make([]string, len(problems))
	for i, p := range problems {
		lines[i] = p.String()
	}
	return fmt.Errorf("%w。次の箇所を直してください。\n- %s", ErrShape, strings.Join(lines, "\n- "))
}

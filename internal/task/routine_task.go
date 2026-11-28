package task

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A routine may keep a task of its own: task.md in the routine's directory
// (areas/<area>/routines/<ID>/), written in the same shape and with the same
// commands as any task. It says what should be done about the routine now;
// the receipts of each run stay separate. The routine never finishes, so its
// task moves only between the statuses in RoutineStatuses, stays out of the
// task lists, and takes its title from routine.md.

// RoutineStatuses are the statuses a routine's task may have.
var RoutineStatuses = []Status{Doing, Waiting, Review}

// RoutineStatusNames lists them the way messages name them.
const RoutineStatusNames = "doing, waiting, review"

var ErrRoutineStatus = errors.New("定期タスクのタスクの状態は、進行中（doing）、待機（waiting）、確認待ち（review）のどれかにしてください。定期タスクを止めるときは、routine.md の enabled を false にします")

var ErrRoutineField = errors.New("定期タスクのタスクでは、冒頭の項目を変えられません。題名と止めるかどうかは routine.md の title と enabled で、状態は lacia task set で変えます")

// RoutineStatus reports whether a routine's task may have the status.
func (s Status) RoutineStatus() bool {
	for _, v := range RoutineStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// routineTaskDir reports whether dir, relative to the repository root, is
// the directory of a routine (areas/<area>/routines/<ID>).
func routineTaskDir(dir string) bool {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	return len(parts) == 4 && parts[0] == "areas" && parts[2] == "routines" && ValidRoutineID(parts[3])
}

// routineTitle reads the title of the routine in dir from its routine.md.
// The definition may hold fields a task does not, so only the title line of
// the front matter is read. A routine without one is called by its ID.
func routineTitle(root, dir, id string) string {
	data, err := os.ReadFile(filepath.Join(root, dir, "routine.md"))
	if err != nil {
		return id
	}
	rest, ok := strings.CutPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "---\n")
	if !ok {
		return id
	}
	for _, line := range strings.Split(rest, "\n") {
		if line == "---" {
			break
		}
		if value, ok := strings.CutPrefix(line, "title:"); ok {
			if title, err := unquote(strings.TrimSpace(value)); err == nil && title != "" {
				return title
			}
		}
	}
	return id
}

// routineDirOf finds the directory of the routine id, relative to the root,
// and its area.
func (s *Store) routineDirOf(id string) (area, dir string, ok bool) {
	if !ValidRoutineID(id) {
		return "", "", false
	}
	for _, a := range s.areas {
		d := filepath.ToSlash(filepath.Join(RoutineBase(a), id))
		if info, err := os.Stat(filepath.Join(s.root, d, "routine.md")); err == nil && info.Mode().IsRegular() {
			return a, d, true
		}
	}
	return "", "", false
}

// loadRoutineTask reads the task of a routine whose task.md appeared after
// the store was loaded, as when a conversation on the server wrote it
// directly. It reports whether there is one now.
func (s *Store) loadRoutineTask(ref string) bool {
	id := strings.ToUpper(strings.TrimSpace(ref))
	area, dir, ok := s.routineDirOf(id)
	if !ok {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.root, dir, TaskFile)); err != nil {
		return false
	}
	t, err := s.read(area, dir)
	if err != nil || t.ID != id {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.tasks[id]; current != nil && current.Dir != dir {
		return false
	}
	s.tasks[id] = t
	return true
}

// createRoutineTask writes the first task.md of a routine: the front matter
// the server keeps (id, created, and the status, which starts as waiting)
// and the body given. A body that breaks the shape is refused before any
// file is written.
func (s *Store) createRoutineTask(id, content, actor string) (Task, error) {
	if s.noTasks {
		return Task{}, ErrNoTasks
	}
	area, dir, ok := s.routineDirOf(id)
	if !ok {
		return Task{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks[id] != nil {
		return Task{}, fmt.Errorf("%s は、すでに %s で使われています", id, s.tasks[id].Dir)
	}
	now := s.now().In(s.Location())
	doc := Doc{Body: content}
	doc.Set("id", id)
	doc.Set("status", string(Waiting))
	doc.Set("created", now.Format(time.DateOnly))
	t, err := parseTask(doc, true)
	if err != nil {
		return Task{}, err
	}
	if problems := ShapeProblems(t); len(problems) > 0 {
		return Task{}, shapeError(problems)
	}
	// CreateNew keeps a task.md that another writer made in the meantime.
	name := filepath.Join(dir, TaskFile)
	if err := s.records.CreateNew(name, []byte(doc.String()), 0o644); errors.Is(err, fs.ErrExist) {
		return Task{}, fmt.Errorf("%s の task.md が、ほかの書き込みで先に作られました。lacia task show %s で読み直してから、もう1度保存してください", id, id)
	} else if err != nil {
		s.records.Remove(name)
		return Task{}, err
	}
	created := Event{Type: EventCreated, By: actor, Status: string(Waiting), Text: fmt.Sprintf("定期タスクのタスクを作りました。状態は「%s」です。", Waiting.Label())}
	if err := s.record(dir, now, created); err != nil {
		return Task{}, err
	}
	written := Event{Type: EventFile, By: actor, Action: "wrote", Path: TaskFile, Bytes: ptr(int64(len(content))), SHA256: docHash(content),
		Text: fmt.Sprintf("%s を書き換えました（%s）。", TaskFile, HumanSize(int64(len(content))))}
	if err := s.record(dir, now, written); err != nil {
		return Task{}, err
	}
	read, err := s.read(area, dir)
	if err != nil {
		return Task{}, err
	}
	s.tasks[id] = read
	return s.annotated(read), nil
}

// Package wait keeps the things Lacia is waiting for outside a conversation:
// a cluster job, or a file that a long-running process writes when it ends.
//
// A wait belongs to a task. The configured watcher looks at the target and reports
// what it saw; the server decides when the wait is over, and what happens
// next. Each wait is a small JSON file, so it survives a restart and can be
// inspected by hand.
package wait

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/effect/disk"
)

// Kinds of target.
const (
	Job  = "job"  // a Kubernetes Job
	File = "file" // a file inside a Pod, written when the work ends
)

// What was last seen of the target.
const (
	Pending   = "pending"   // exists, not started (for a Job: suspended or queued)
	Running   = "running"   // at work
	Succeeded = "succeeded" // ended well
	Failed    = "failed"    // ended badly
	Gone      = "gone"      // the target no longer exists
	Unseen    = "unseen"    // the watcher could not look (credentials, network)
)

// States of a wait.
const (
	Waiting   = "open"      // still waiting
	Finished  = "finished"  // the target ended well
	Broken    = "failed"    // the target ended badly
	Vanished  = "gone"      // the target disappeared before its end was seen
	Overdue   = "overdue"   // the deadline passed first
	Cancelled = "cancelled" // withdrawn by a conversation
)

// Who continues once the wait is over.
const (
	NextCodex  = "codex"
	NextClaude = "claude"
	NextNone   = "none" // nobody: the task is shown to the user
)

type Wait struct {
	Version    int    `json:"schema_version"`
	HandoverID string `json:"handover_id,omitempty"`
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Kind       string `json:"kind"`
	// Context, Namespace and Name locate the target for kubectl. Name is
	// the Job, or for a file the Pod or "deploy/<name>" to look inside.
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"` // the file, for Kind == File

	By      time.Time `json:"by"`   // deadline
	Next    string    `json:"next"` // who continues
	Then    string    `json:"then"` // what to do next, in the registrant's words
	From    string    `json:"from"` // who registered it, such as "local-claude"
	FromKey string    `json:"from_key,omitempty"`

	State    string    `json:"state"`
	Created  time.Time `json:"created"`
	Seen     string    `json:"seen,omitempty"`    // last observation
	Detail   string    `json:"detail,omitempty"`  // what came with it
	SeenAt   time.Time `json:"seen_at,omitzero"`  // when the watcher last reported
	Resolved time.Time `json:"resolved,omitzero"` // when the wait stopped being open
	Told     time.Time `json:"told,omitzero"`
}

// Target describes what is waited for, for people.
func (w Wait) Target() string {
	where := w.Context + " の " + w.Namespace
	if w.Kind == File {
		return fmt.Sprintf("%s にある %s の処理（終了の印は %s）", where, w.Name, w.Path)
	}
	return fmt.Sprintf("%s にある Job %s", where, w.Name)
}

// Outcome says how the wait ended, as a sentence.
func (w Wait) Outcome() string { return w.OutcomeIn(jst) }

// OutcomeIn describes the outcome in the repository zone.
func (w Wait) OutcomeIn(loc *time.Location) string {
	switch w.State {
	case Finished:
		return "正常に終わりました。"
	case Broken:
		return "処理に失敗しました。"
	case Vanished:
		return "終了を確かめる前に、対象が見つからなくなりました。"
	case Overdue:
		return "期限（" + w.By.In(loc).Format("2006-01-02 15:04") + "）までに、終了を確かめられませんでした。"
	case Cancelled:
		return "待ちを取り消しました。"
	}
	return "まだ待っています。"
}

// SeenLabel is the Japanese name of the last observation.
func (w Wait) SeenLabel() string {
	switch w.Seen {
	case Pending:
		return "開始を待っています"
	case Running:
		return "実行中です"
	case Succeeded:
		return "正常に終わりました"
	case Failed:
		return "処理に失敗しました"
	case Gone:
		return "見つかりません"
	case Unseen:
		return "状態を確かめられていません"
	}
	return "まだ確かめていません"
}

// Continues reports whether an agent is meant to take the next step.
func (w Wait) Continues() bool {
	return (w.State == Finished || w.State == Broken) && conversationNext(w.Next)
}

func conversationNext(next string) bool {
	to, err := caller.ParseDestination(next)
	return err == nil && to.Kind != ""
}

// Closed reports whether observation of this target is complete.
func (w Wait) Closed() bool { return w.State != Waiting }

var jst = time.FixedZone("JST", 9*60*60)

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	// A file is looked for inside a Pod, or inside whatever Pod a workload has.
	podPattern = regexp.MustCompile(`^((deploy|deployment|statefulset|sts|pod|job)/)?[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
)

// Validate checks what a conversation asked for. The names end up as
// arguments of kubectl, so the watcher only accepts the forms below.
func (w Wait) Validate(now time.Time) error {

	switch {
	case w.TaskID == "":
		return errors.New("どのタスクの待ちなのかを、--task で指定してください")
	case w.Kind != Job && w.Kind != File:
		return errors.New("待つものを、--job か --file で指定してください")
	case !namePattern.MatchString(w.Context) || !namePattern.MatchString(w.Namespace):
		return errors.New("対象は、<kubectl の context>/<名前空間>/<名前> の形で書いてください")
	case w.Kind == Job && !namePattern.MatchString(w.Name):
		return fmt.Errorf("Job の名前 %q を読めません", w.Name)
	case w.Kind == File && !podPattern.MatchString(w.Name):
		return fmt.Errorf("ファイルを探す場所 %q を読めません。Pod の名前か、deploy/<名前> の形で書いてください", w.Name)
	case w.Kind == File && !strings.HasPrefix(w.Path, "/"):
		return errors.New("ファイルは、/ で始まるパスで書いてください")
	case w.By.IsZero():
		return errors.New("いつまでに終わる見込みかを、--by で指定してください（6h や 2026-11-19 06:00）")
	case !w.By.After(now):
		return errors.New("--by の日時が、もう過ぎています")
	case w.Next != NextNone && !conversationNext(w.Next):
		return errors.New("--next は「場所-種類」、codex、claude、owner、none のいずれかを指定してください")
	case w.Next != NextNone && strings.TrimSpace(w.Then) == "":
		return errors.New("終わったら何をするのかを、最後に文章で書いてください")
	}
	return nil
}

type Store struct {
	dir *disk.Dir
	Now func() time.Time

	mu    sync.Mutex
	items map[string]*Wait
}

var ErrNotFound = errors.New("その待ちは見つかりません")

func Open(dir *disk.Dir) (*Store, error) {
	s := &Store{dir: dir, Now: time.Now, items: map[string]*Wait{}}
	paths, err := filepath.Glob(filepath.Join(dir.Path(), "*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var w Wait
		if err := json.Unmarshal(data, &w); err != nil || w.ID == "" {
			slog.Warn("unreadable wait file", "path", path)
			continue
		}
		if w.Version == 2 {
			s.items[w.ID] = &w
		} else {
			// Legacy active records require the coordinated upgrade preflight.
			var legacy struct {
				Acked, Started time.Time
				Skipped        string
			}
			json.Unmarshal(data, &legacy)
			closed := w.State == Cancelled || !legacy.Acked.IsZero() || legacy.Skipped != "" || (!w.Told.IsZero() && (!w.Continues() || !legacy.Started.IsZero()))
			if w.State == Waiting || !closed {
				return nil, fmt.Errorf("旧形式の待ち %s がまだ開いています。以前の版で受け取りを知らせるか、待ちを取り消してから更新してください", w.ID)
			}
		}
	}
	return s, nil
}

func (s *Store) save(w *Wait) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return s.dir.WriteFileNoSync(w.ID+".json", append(data, '\n'), 0o600)
}

// Add registers a wait. Registering the same target for the same task
// again while the first is still open returns that one, so a conversation
// may retry safely.
func (s *Store) Add(w Wait) (Wait, bool, error) {
	now := s.Now()
	w.Then = strings.TrimSpace(w.Then)
	if err := w.Validate(now); err != nil {
		return Wait{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.items {
		if old.State == Waiting && old.TaskID == w.TaskID && old.Kind == w.Kind && old.Context == w.Context && old.Namespace == w.Namespace && old.Name == w.Name && old.Path == w.Path {
			return *old, false, nil
		}
	}
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return Wait{}, false, err
	}
	w.Version = 2
	w.ID, w.State, w.Created = "W"+hex.EncodeToString(buf), Waiting, now

	if err := s.save(&w); err != nil {
		return Wait{}, false, err
	}
	s.items[w.ID] = &w
	return w, true, nil
}

func (s *Store) Get(id string) (Wait, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.items[id]; w != nil {
		return *w, nil
	}
	return Wait{}, ErrNotFound
}

// List returns the waits that keep, oldest first.
func (s *Store) List(keep func(Wait) bool) []Wait {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Wait{}
	for _, w := range s.items {
		if keep == nil || keep(*w) {
			out = append(out, *w)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// OpenByTask groups the targets that are still being watched by task ID.
func (s *Store) OpenByTask() map[string]TaskWaits {
	if s == nil {
		return nil
	}
	out := make(map[string]TaskWaits)
	for _, w := range s.List(func(w Wait) bool { return !w.Closed() }) {
		out[w.TaskID] = append(out[w.TaskID], w)
	}
	return out
}

type TaskWaits []Wait

// Running reports whether a watched target was last seen at work.
func (w TaskWaits) Running() bool {
	for _, item := range w {
		if item.Seen == Running {
			return true
		}
	}
	return false
}

// Update changes one wait under the lock. When change returns false,
// nothing is saved. It returns the wait and whether it was changed.
func (s *Store) Update(id string, change func(*Wait) bool) (Wait, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.items[id]
	if w == nil {
		return Wait{}, false, ErrNotFound
	}
	next := *w
	if !change(&next) {
		return *w, false, nil
	}
	if err := s.save(&next); err != nil {
		return Wait{}, false, err
	}
	s.items[id] = &next
	return next, true, nil
}

// Observe records what the watcher saw. An observed end closes the wait;
// the second result is true when this call did so.
func (s *Store) Observe(id, seen, detail string) (Wait, bool, error) {
	now := s.Now()
	resolved := false
	w, _, err := s.Update(id, func(w *Wait) bool {
		if w.State != Waiting {
			return false
		}
		w.Seen, w.Detail, w.SeenAt = seen, detail, now
		switch seen {
		case Succeeded:
			w.State = Finished
		case Failed:
			w.State = Broken
		case Gone:
			w.State = Vanished
		}
		if w.State != Waiting {
			w.Resolved, resolved = now, true
		}
		return true
	})
	return w, resolved, err
}

// Expire closes the open waits whose deadline has passed and returns them.
func (s *Store) Expire() []Wait {
	now := s.Now()
	var out []Wait
	for _, w := range s.List(func(w Wait) bool { return w.State == Waiting && !w.By.After(now) }) {
		if w, changed, err := s.Update(w.ID, func(w *Wait) bool {
			if w.State != Waiting {
				return false
			}
			w.State, w.Resolved = Overdue, now
			return true
		}); err == nil && changed {
			out = append(out, w)
		}
	}
	return out
}

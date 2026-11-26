// Package handover durably delivers messages independently of target watches.
package handover

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/caller"
)

const (
	Queued        = "queued"
	Delivered     = "delivered"
	Acked         = "acked"
	Told          = "told"
	ToldDelivered = "told_delivered"
	Skipped       = "skipped"
	Cancelled     = "cancelled"
	Grace         = 20 * time.Minute
	StartWithin   = 24 * time.Hour
)

type Handover struct {
	Version      int       `json:"schema_version"`
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	WaitID       string    `json:"wait_id,omitempty"`
	Next         string    `json:"next"`
	Then         string    `json:"then"`
	OwnerAction  string    `json:"owner_action,omitempty"`
	From         string    `json:"from"`
	FromKey      string    `json:"from_key,omitempty"`
	Via          string    `json:"via,omitempty"`
	State        string    `json:"state"`
	Created      time.Time `json:"created"`
	Started      time.Time `json:"started,omitzero"`
	Runner       string    `json:"runner,omitempty"`
	Acked        time.Time `json:"acked,omitzero"`
	AckedBy      string    `json:"acked_by,omitempty"`
	ClaimPending bool      `json:"claim_pending,omitempty"`
	AckedLabel   string    `json:"acked_label,omitempty"`
	AckedKey     string    `json:"acked_key,omitempty"`
	Ended        time.Time `json:"ended,omitzero"`
	ExitCode     int       `json:"exit_code,omitempty"`
	Told         time.Time `json:"told,omitzero"`
	Skipped      string    `json:"skipped,omitempty"`
}

func (h Handover) Validate() error {
	if h.TaskID == "" {
		return errors.New("タスクを指定してください")
	}
	if to, err := caller.ParseDestination(h.Next); err != nil || to.Kind == "" {
		return errors.New("届ける先を「場所-種類」、codex、claudeのいずれかで指定してください")
	}
	if strings.TrimSpace(h.Then) == "" {
		return errors.New("申し送りの文章が空です")
	}
	return nil
}
func (h Handover) Closed() bool {
	return h.State == Acked || h.State == Skipped || h.State == Cancelled
}
func (h Handover) Runnable() bool { return h.State == Queued || h.State == Told }
func (h Handover) Late(now time.Time) bool {
	return (h.State == Queued || h.State == Delivered) && (!h.Ended.IsZero() || now.Sub(h.Created) >= Grace)
}
func (h Handover) Stale(now time.Time) bool { return h.Runnable() && now.Sub(h.Created) >= StartWithin }
func (h Handover) Target() string           { return fmt.Sprintf("%s からの申し送り", h.From) }
func (h Handover) Outcome() string          { return h.Then }

// StatusText describes delivery without implying a change to the task.
func (h Handover) StatusText() string {
	switch h.State {
	case Delivered:
		return "受け取りの知らせを待っています。"
	case Told:
		return "宛先への配信が遅れています。"
	case ToldDelivered:
		return "受け取りの知らせが遅れています。"
	case Acked:
		return h.AckedBy + " が受け取りました。"
	case Skipped:
		return "続きの開始を見送りました。" + h.Skipped
	case Cancelled:
		return "取り消しました。"
	default:
		return "宛先の会話に届くのを待っています。"
	}
}

// NeedsReassignment reports a handover that will not be delivered again
// automatically, including an expiry the delivery loop has yet to record.
func (h Handover) NeedsReassignment(now time.Time) bool {
	return h.State == ToldDelivered || h.State == Skipped && !h.Told.IsZero() ||
		h.State == Delivered && h.Late(now) || h.Stale(now)
}

// ConditionText describes the handover as it appears among task conditions.
func (h Handover) ConditionText(now time.Time) string {
	if h.NeedsReassignment(now) {
		if h.State == Skipped || h.Stale(now) {
			return "期限が過ぎました。再割り当てが必要です。"
		}
		return "受け取りが遅れています。再割り当てが必要です。"
	}
	return h.StatusText()
}

// RequestedAction becomes visible when the delay or expiry is recorded.
func (h Handover) RequestedAction() string {
	if h.Told.IsZero() || h.State == Acked || h.State == Cancelled {
		return ""
	}
	return h.OwnerAction
}

// Store keeps the handovers in a directory.
type Store struct {
	dir string
	Now func() time.Time

	mu    sync.Mutex
	items map[string]*Handover
}

// ErrNotFound is returned for an unknown ID.
var ErrNotFound = errors.New("その申し送りは見つかりません")

// Open loads the handovers saved in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, Now: time.Now, items: map[string]*Handover{}}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var w Handover
		if err := json.Unmarshal(data, &w); err != nil || w.ID == "" {
			slog.Warn("unreadable handover file", "path", path)
			continue
		}
		if w.Version == 1 {
			s.items[w.ID] = &w
		}
	}
	return s, nil
}

func (s *Store) save(w *Handover) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, w.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Add creates a message. A wait-derived message has a deterministic ID,
// so a retry after either side of the outbox save returns the same message.
func (s *Store) Add(w Handover) (Handover, bool, error) {
	now := s.Now()
	w.Then = strings.TrimSpace(w.Then)
	if err := w.Validate(); err != nil {
		return Handover{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.WaitID != "" {
		w.ID = "H" + strings.TrimPrefix(w.WaitID, "W")
	}
	if w.ID != "" {
		if old := s.items[w.ID]; old != nil {
			return *old, false, nil
		}
	}
	if w.ID == "" {
		buf := make([]byte, 12)
		if _, err := rand.Read(buf); err != nil {
			return Handover{}, false, err
		}
		w.ID = "H" + hex.EncodeToString(buf)
	}
	w.Version, w.State = 1, Queued
	if w.Created.IsZero() {
		w.Created = now
	}
	if err := s.save(&w); err != nil {
		return Handover{}, false, err
	}
	s.items[w.ID] = &w
	return w, true, nil
}

// Get returns one handover.
func (s *Store) Get(id string) (Handover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.items[id]; w != nil {
		return *w, nil
	}
	return Handover{}, ErrNotFound
}

// List returns the handovers that keep, oldest first.
func (s *Store) List(keep func(Handover) bool) []Handover {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Handover{}
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

// ConditionsByTask groups pending handovers and the latest expired handover
// for each destination and wait. A newer handover supersedes an expiry.
func (s *Store) ConditionsByTask() map[string][]Handover {
	if s == nil {
		return nil
	}
	type destination struct{ task, next, wait string }
	seen := make(map[destination]bool)
	conditions := make(map[string][]Handover)
	items := s.List(nil)
	for i := len(items) - 1; i >= 0; i-- {
		h := items[i]
		key := destination{h.TaskID, h.Next, h.WaitID}
		if !h.Closed() || h.State == Skipped && !h.Told.IsZero() && !seen[key] {
			conditions[h.TaskID] = append(conditions[h.TaskID], h)
		}
		seen[key] = true
	}
	for _, handovers := range conditions {
		slices.Reverse(handovers)
	}
	return conditions
}

// Update changes one handover under the lock. When change returns false,
// nothing is saved. It returns the handover and whether it was changed.
func (s *Store) Update(id string, change func(*Handover) bool) (Handover, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.items[id]
	if w == nil {
		return Handover{}, false, ErrNotFound
	}
	next := *w
	if !change(&next) {
		return *w, false, nil
	}
	if err := s.save(&next); err != nil {
		return Handover{}, false, err
	}
	s.items[id] = &next
	return next, true, nil
}

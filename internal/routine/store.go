// Package routine preserves the acceptance and review history of recurring work.
package routine

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"github.com/imos/lacia/internal/task"
)

// Matter identifies one external development. Both fields must be stable on retry.
type Matter struct {
	Key       string `json:"key"`
	Revision  string `json:"revision"`
	Summary   string `json:"summary,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// Handoff identifies a finding that the reporter saved and read back in an
// individual task. The server separately checks whether the task is open.
type Handoff struct {
	Matter
	TaskID string `json:"task_id"`
}

// Result is one run's report of coverage, findings and handoffs.
type Result struct {
	Version     int      `json:"version,omitempty"`
	CheckStatus string   `json:"check_status,omitempty"`
	Checks      []Check  `json:"checks,omitempty"`
	Unverified  []string `json:"unverified,omitempty"`
	Nothing     bool     `json:"nothing,omitempty"`
	RunID       string   `json:"run_id"`
	Summary     string   `json:"summary,omitempty"`
	Reference   string   `json:"reference,omitempty"`
	Matters     []Matter `json:"matters,omitempty"`
	Suppress    bool     `json:"-"`

	Handoffs []Handoff `json:"handoffs,omitempty"`
}

// ReceiptURL identifies one acceptance by its stable number within a routine.
func ReceiptURL(id string, sequence uint64) string {
	return fmt.Sprintf("/routines/%s/receipts/%d", url.PathEscape(id), sequence)
}

type Receipt struct {
	Unverified []string  `json:"unverified,omitempty"`
	Sequence   uint64    `json:"sequence"`
	RunID      string    `json:"run_id"`
	Summary    string    `json:"summary,omitempty"`
	Reference  string    `json:"reference,omitempty"`
	Matters    []Matter  `json:"matters,omitempty"`
	AcceptedAt time.Time `json:"accepted_at"`
	Reviewed   bool      `json:"-"`
	Notified   bool      `json:"-"`
}

// Completion records the server-issued displayed boundary, or explicit visible IDs.
// Validation of the signed display token belongs to the HTTP boundary.
type Completion struct {
	OperationID string   `json:"operation_id"`
	Actor       string   `json:"actor"`
	Through     uint64   `json:"through,omitempty"`
	Sequences   []uint64 `json:"sequences,omitempty"`
}

type Event struct {
	Type       string      `json:"type"`
	RoutineID  string      `json:"routine_id"`
	At         time.Time   `json:"at"`
	Receipt    *Receipt    `json:"receipt,omitempty"`
	RunID      string      `json:"run_id,omitempty"`
	Sequence   uint64      `json:"sequence,omitempty"`
	Completion *Completion `json:"completion,omitempty"`
}

type Snapshot struct {
	Receipts        []Receipt
	Pending         []Receipt
	LatestSequence  uint64
	ReviewedThrough uint64
	History         []Event
}

// Store uses the Git-managed JSONL history as its sole source of truth. Mutations
// are serialized across Store instances and processes on the same host. The
// scheduler must continue to have one managing server across different hosts.
type Store struct {
	Root string
	Now  func() time.Time
}

func NewStore(root string) *Store { return &Store{Root: root} }
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) locked(id string, fn func() error) error {
	if !task.ValidRoutineID(id) {
		return fmt.Errorf("定期タスクIDが正しくありません: %q", id)
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(root + "/" + id))
	path := filepath.Join(os.TempDir(), fmt.Sprintf("lacia-routine-%x.lock", hash))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}

func (s *Store) read(id string) (Snapshot, error) {
	state := Snapshot{}
	dir, err := task.RoutineDir(s.Root, id)
	if err != nil {
		return state, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "runs", "????-??", "reviews.jsonl"))
	if err != nil {
		return state, err
	}
	sort.Strings(paths)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return state, err
		}
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 4096), 16<<20)
		line := 0
		for scan.Scan() {
			line++
			var e Event
			if err = json.Unmarshal(scan.Bytes(), &e); err != nil {
				break
			}
			if e.RoutineID != id {
				err = errors.New("履歴の定期タスクIDが一致しません")
				break
			}
			state.History = append(state.History, e)
		}
		if err == nil {
			err = scan.Err()
		}
		f.Close()
		if err != nil {
			return state, fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	// Files are partitioned by wall clock month, which can move backwards. Receipt
	// sequence, not the clock or the file order, establishes acceptance order.
	receipts := map[uint64]Receipt{}
	reviewed := map[uint64]bool{}
	notified := map[uint64]bool{}
	for _, e := range state.History {
		switch e.Type {
		case "accepted":
			if e.Receipt == nil || e.Receipt.Sequence == 0 {
				return state, errors.New("受付の履歴が正しくありません")
			}
			r := *e.Receipt
			if _, ok := receipts[r.Sequence]; ok {
				return state, errors.New("受付番号が重複しています")
			}
			receipts[r.Sequence] = r
			if r.Sequence > state.LatestSequence {
				state.LatestSequence = r.Sequence
			}
		case "completed":
			if e.Completion == nil {
				return state, errors.New("完了の履歴が正しくありません")
			}
			c := e.Completion
			if c.Through > state.ReviewedThrough {
				state.ReviewedThrough = c.Through
			}
			for _, n := range c.Sequences {
				reviewed[n] = true
			}
		case "notified":
			notified[e.Sequence] = true
		case "deduplicated", "suppressed":
		default:
			return state, fmt.Errorf("確認履歴の種類が正しくありません: %s", e.Type)
		}
	}
	for n, r := range receipts {
		r.Reviewed = n <= state.ReviewedThrough || reviewed[n]
		r.Notified = notified[n]
		state.Receipts = append(state.Receipts, r)
	}
	sort.Slice(state.Receipts, func(i, j int) bool { return state.Receipts[i].Sequence < state.Receipts[j].Sequence })
	for _, r := range state.Receipts {
		if !r.Reviewed {
			state.Pending = append(state.Pending, r)
		}
	}
	return state, nil
}

// appendEvent atomically replaces one month's file, preserving all previous
// lines. A crash can leave either version, never a partial JSON line. Replaying
// the same operation after an ambiguous error is safe through the operation ID.
func (s *Store) appendEvent(id string, e Event) error {
	routineDir, err := task.RoutineDir(s.Root, id)
	if err != nil {
		return err
	}
	dir := filepath.Join(routineDir, "runs", e.At.Format("2006-01"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "reviews.jsonl")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, line...)
	data = append(data, '\n')
	f, err := os.CreateTemp(dir, ".reviews-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0644); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Store) Snapshot(id string) (Snapshot, error) {
	var out Snapshot
	err := s.locked(id, func() error { var err error; out, err = s.read(id); return err })
	return out, err
}

// Accept persists a batch once, returning whether it created a new receipt.
// Retrying an earlier run returns its original receipt. Batches with only known
// key/revision pairs are logged but do not create another notification.
func (s *Store) Accept(id string, result Result) (Receipt, bool, error) {
	var out Receipt
	created := false
	err := s.locked(id, func() error {
		if result.RunID == "" {
			return errors.New("実行IDが必要です")
		}
		state, err := s.read(id)
		if err != nil {
			return err
		}
		for _, e := range state.History {
			if e.RunID == result.RunID || (e.Receipt != nil && e.Receipt.RunID == result.RunID) {
				for _, r := range state.Receipts {
					if r.RunID == result.RunID {
						out = r
						break
					}
				}
				return nil
			}
		}
		known := map[[2]string]bool{}
		for _, r := range state.Receipts {
			for _, m := range r.Matters {
				known[[2]string{m.Key, m.Revision}] = true
			}
		}
		var matters []Matter
		for _, m := range result.Matters {
			if m.Key == "" || m.Revision == "" {
				return errors.New("用件のキーと進展の識別子が必要です")
			}
			key := [2]string{m.Key, m.Revision}
			if !known[key] {
				matters = append(matters, m)
				known[key] = true
			}
		}
		e := Event{RoutineID: id, At: s.now(), RunID: result.RunID}
		if result.Suppress || result.Nothing {
			e.Type = "suppressed"
		} else if len(result.Matters) > 0 && len(matters) == 0 {
			e.Type = "deduplicated"
		} else {
			out = Receipt{Unverified: result.Unverified, Sequence: state.LatestSequence + 1, RunID: result.RunID, Summary: result.Summary, Reference: result.Reference, Matters: matters, AcceptedAt: e.At}
			e.Type = "accepted"
			e.Receipt = &out
		}
		if err = s.appendEvent(id, e); err != nil {
			return err
		}
		created = e.Type == "accepted"
		return nil
	})
	return out, created, err
}

func (s *Store) Complete(id string, c Completion) (Snapshot, error) {
	var out Snapshot
	err := s.locked(id, func() error {
		if c.OperationID == "" || c.Actor == "" {
			return errors.New("操作IDと確認した方が必要です")
		}
		state, err := s.read(id)
		if err != nil {
			return err
		}
		for _, e := range state.History {
			if e.Completion != nil && e.Completion.OperationID == c.OperationID {
				out = state
				return nil
			}
		}
		if c.Through > state.LatestSequence {
			return errors.New("表示した範囲を超えて完了にはできません")
		}
		for _, n := range c.Sequences {
			if n == 0 || n > state.LatestSequence {
				return errors.New("受付番号が正しくありません")
			}
		}
		if c.Through > 0 && len(c.Sequences) > 0 {
			return errors.New("完了の境界と個別の受付番号は同時に指定できません")
		}
		if err = s.appendEvent(id, Event{Type: "completed", RoutineID: id, At: s.now(), Completion: &c}); err != nil {
			return err
		}
		out, err = s.read(id)
		return err
	})
	return out, err
}

func (s *Store) MarkNotified(id string, sequence uint64) error {
	return s.locked(id, func() error {
		state, err := s.read(id)
		if err != nil {
			return err
		}
		for _, r := range state.Receipts {
			if r.Sequence == sequence {
				if r.Notified {
					return nil
				}
				return s.appendEvent(id, Event{Type: "notified", RoutineID: id, At: s.now(), Sequence: sequence})
			}
		}
		return errors.New("通知する受付番号が見つかりません")
	})
}

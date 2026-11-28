// Package routine preserves the acceptance and review history of recurring work.
package routine

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"github.com/imos/lacia/internal/effect/disk"
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
	Resolved     []string  `json:"resolved,omitempty"`
	Version      int       `json:"version,omitempty"`
	CheckStatus  string    `json:"check_status,omitempty"`
	Checks       []Check   `json:"checks,omitempty"`
	Unverified   []string  `json:"unverified,omitempty"`
	Nothing      bool      `json:"nothing,omitempty"`
	RunID        string    `json:"run_id"`
	Summary      string    `json:"summary,omitempty"`
	Reference    string    `json:"reference,omitempty"`
	Matters      []Matter  `json:"matters,omitempty"`
	Suppress     bool      `json:"-"`
	ProblemKey   string    `json:"-"`
	ProblemAt    time.Time `json:"-"`
	ProblemSince time.Time `json:"-"`

	Handoffs []Handoff `json:"handoffs,omitempty"`
}

// ReceiptURL identifies one acceptance by its stable number within a routine.
func ReceiptURL(id string, sequence uint64) string {
	return fmt.Sprintf("/routines/%s/receipts/%d", url.PathEscape(id), sequence)
}

type Receipt struct {
	Outcome    string    `json:"outcome,omitempty"`
	ProblemKey string    `json:"problem_key,omitempty"`
	ProblemAt  time.Time `json:"problem_at,omitzero"`
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
	ProblemKey string      `json:"problem_key,omitempty"`
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
	// The records directory itself is the lock, so that no lock file is
	// written outside the records.
	f, err := os.Open(s.Root)
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
	var previousID string
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
			if e.RoutineID != id && previousID == "" {
				// A renamed routine keeps its earlier events byte-for-byte.
				if raw, readErr := os.ReadFile(filepath.Join(dir, "routine.md")); readErr == nil {
					if doc, parseErr := task.ParseDoc(string(raw)); parseErr == nil {
						if alias := doc.Get("legacy_name"); task.ValidRoutineID(alias) {
							previousID = alias
						}
					}
				}
			}
			if e.RoutineID != id && (previousID == "" || e.RoutineID != previousID) {
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
	name, _ := filepath.Rel(s.Root, filepath.Join(routineDir, "runs", e.At.Format("2006-01"), "reviews.jsonl"))
	data, err := os.ReadFile(filepath.Join(s.Root, name))
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
	records, err := disk.OpenRecords(s.Root)
	if err != nil {
		return err
	}
	defer records.Close()
	return records.WriteFile(name, data, 0o644)
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
		for _, receipt := range state.Receipts {
			if receipt.RunID == result.RunID {
				out = receipt
				return nil
			}
		}
		for _, event := range state.History {
			if event.RunID == result.RunID && (result.ProblemKey == "" || event.ProblemKey != "") {
				return nil
			}
		}
		known := map[[2]string]bool{}
		problemKey := result.ProblemKey
		for _, r := range state.Receipts {
			if problemKey == r.ProblemKey && r.ProblemAt.After(result.ProblemSince) {
				problemKey = ""
			}
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
		if result.ProblemKey == "" && (result.Suppress || result.Nothing) {
			e.Type = "suppressed"
		} else if problemKey == "" && len(matters) == 0 && (len(result.Matters) > 0 || result.ProblemKey != "") {
			e.Type = "deduplicated"
			e.ProblemKey = result.ProblemKey
		} else {
			out = Receipt{ProblemKey: problemKey, Unverified: result.Unverified, Sequence: state.LatestSequence + 1, RunID: result.RunID, Summary: result.Summary, Reference: result.Reference, Matters: matters, AcceptedAt: e.At}
			if problemKey != "" {
				out.ProblemAt = result.ProblemAt
			}
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

// Record puts one execution in the receipt history. Older receipts and their
// signed links keep their original number. Another run of the same problem
// is its own record; deduplication belongs to the task, not these records.
func (s *Store) Record(id, outcome string, result Result) (Receipt, error) {
	var out Receipt
	err := s.locked(id, func() error {
		if result.RunID == "" {
			return errors.New("実行IDが必要です")
		}
		state, err := s.read(id)
		if err != nil {
			return err
		}
		for _, receipt := range state.Receipts {
			if receipt.RunID == result.RunID {
				out = receipt
				return nil
			}
		}
		at := s.now()
		out = Receipt{
			Outcome: outcome, Sequence: state.LatestSequence + 1,
			RunID: result.RunID, AcceptedAt: at, Summary: result.Summary,
			Reference: result.Reference, Unverified: result.Unverified,
			Matters: result.Matters, ProblemKey: result.ProblemKey, ProblemAt: result.ProblemAt,
		}
		return s.appendEvent(id, Event{Type: "accepted", RoutineID: id, At: at, RunID: result.RunID, Receipt: &out})
	})
	return out, err
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

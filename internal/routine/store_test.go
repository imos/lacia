package routine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func result(run, key, rev string) Result {
	return Result{RunID: run, Summary: "新着があります", Matters: []Matter{{Key: key, Revision: rev}}}
}
func mustAccept(t *testing.T, s *Store, r Result) Receipt {
	t.Helper()
	got, new, err := s.Accept("S1001", r)
	if err != nil || !new {
		t.Fatalf("Accept: %+v, %v, %v", got, new, err)
	}
	return got
}

func TestDedupAndBatch(t *testing.T) {
	s := NewStore(t.TempDir())
	r := result("run1", "issue1", "v1")
	r.Matters = append(r.Matters, Matter{Key: "issue2", Revision: "v1"}, r.Matters[0])
	got := mustAccept(t, s, r)
	if got.Sequence != 1 || len(got.Matters) != 2 {
		t.Fatalf("receipt: %+v", got)
	}
	got, new, err := s.Accept("S1001", r)
	if err != nil || new || got.Sequence != 1 {
		t.Fatalf("retry: %+v %v %v", got, new, err)
	}
	got, new, err = s.Accept("S1001", result("run2", "issue1", "v1"))
	if err != nil || new || got.Sequence != 0 {
		t.Fatalf("known revision: %+v %v %v", got, new, err)
	}
	mustAccept(t, s, result("run3", "issue1", "v2"))
	state, err := NewStore(s.Root).Snapshot("S1001")
	if err != nil || len(state.Receipts) != 2 || state.LatestSequence != 2 {
		t.Fatalf("restore: %+v %v", state, err)
	}
	_, new, err = s.Accept("S1001", Result{RunID: "run4", Nothing: true})
	if err != nil || new {
		t.Fatal(new, err)
	}
	_, new, err = s.Accept("S1001", Result{RunID: "run4", Summary: "changed retry"})
	if err != nil || new {
		t.Fatal(new, err)
	}
}

func TestCompletionBoundaryRestoreAndNotification(t *testing.T) {
	s := NewStore(t.TempDir())
	date := time.Date(2026, 11, 30, 23, 50, 0, 0, time.FixedZone("JST", 9*3600))
	s.Now = func() time.Time { return date }
	first := mustAccept(t, s, result("run1", "a", "1"))
	shown := first.Sequence
	date = date.Add(time.Hour)
	second := mustAccept(t, s, result("late-run", "b", "1"))
	state, err := s.Complete("S1001", Completion{OperationID: "click1", Actor: "owner", Through: shown})
	if err != nil || len(state.Pending) != 1 || state.Pending[0].Sequence != second.Sequence {
		t.Fatalf("new arrival must remain: %+v %v", state, err)
	}
	// A repeated button submission must not silently complete a larger boundary.
	state, err = s.Complete("S1001", Completion{OperationID: "click1", Actor: "owner", Through: second.Sequence})
	if err != nil || len(state.Pending) != 1 {
		t.Fatal(state, err)
	}
	if err = s.MarkNotified("S1001", second.Sequence); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkNotified("S1001", second.Sequence); err != nil {
		t.Fatal(err)
	}
	state, err = NewStore(s.Root).Snapshot("S1001")
	if err != nil || !state.Receipts[0].Reviewed || state.Receipts[1].Reviewed || !state.Receipts[1].Notified || len(state.History) != 4 {
		t.Fatalf("restore: %+v %v", state, err)
	}
	state, err = s.Complete("S1001", Completion{OperationID: "click2", Actor: "owner", Sequences: []uint64{2}})
	if err != nil || len(state.Pending) != 0 {
		t.Fatal(state, err)
	}
}

func TestClockMovesBackwards(t *testing.T) {
	s := NewStore(t.TempDir())
	date := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return date }
	mustAccept(t, s, result("1", "a", "1"))
	date = date.Add(-time.Hour)
	mustAccept(t, s, result("2", "b", "1"))
	state, err := s.Complete("S1001", Completion{OperationID: "c", Actor: "owner", Through: 1})
	if err != nil || len(state.Pending) != 1 || state.Pending[0].Sequence != 2 {
		t.Fatal(state, err)
	}
}

func TestConcurrentInstances(t *testing.T) {
	root := t.TempDir()
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Go(func() { _, _, err := NewStore(root).Accept("S1001", result("same-run", "a", "1")); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(root)
	state, err := s.Snapshot("S1001")
	if err != nil || len(state.History) != 1 {
		t.Fatal(state, err)
	}
	for i := 0; i < n; i++ {
		wg.Go(func() {
			key := fmt.Sprintf("distinct-run-%d", i)
			_, _, err := NewStore(root).Accept("S1001", result(key, key, "1"))
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	state, err = s.Snapshot("S1001")
	if err != nil || len(state.Receipts) != n+1 {
		t.Fatal(len(state.Receipts), err)
	}
	for i, r := range state.Receipts {
		if r.Sequence != uint64(i+1) {
			t.Fatalf("sequence: %+v", r)
		}
	}
}

func TestFailedWriteCanRetry(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	if err := os.WriteFile(filepath.Join(root, "routines"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Accept("S1001", result("1", "a", "1")); err == nil {
		t.Fatal("write should fail")
	}
	if err := os.Remove(filepath.Join(root, "routines")); err != nil {
		t.Fatal(err)
	}
	got := mustAccept(t, s, result("1", "a", "1"))
	if got.Sequence != 1 {
		t.Fatal(got)
	}
	// Rebuild using only the Git-managed files, with no local cached state.
	copyRoot := t.TempDir()
	paths, _ := filepath.Glob(filepath.Join(root, "routines", "S1001", "runs", "*", "reviews.jsonl"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		dest := filepath.Join(copyRoot, rel)
		os.MkdirAll(filepath.Dir(dest), 0755)
		if err = os.WriteFile(dest, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	state, err := NewStore(copyRoot).Snapshot("S1001")
	if err != nil || len(state.Pending) != 1 {
		t.Fatal(state, err)
	}
}

func TestInvalidHistoryFailsClosed(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	mustAccept(t, s, result("1", "a", "1"))
	paths, _ := filepath.Glob(filepath.Join(root, "routines", "S1001", "runs", "*", "reviews.jsonl"))
	f, err := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{partial")
	f.Close()
	if _, err = s.Snapshot("S1001"); err == nil {
		t.Fatal("corrupt history must fail")
	}
	if _, _, err = s.Accept("S1001", result("2", "b", "1")); err == nil {
		t.Fatal("corruption must not be overwritten")
	}
}

func TestValidationAndJSON(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, _, err := s.Accept("../bad", Result{RunID: "1"}); err == nil {
		t.Fatal("bad ID")
	}
	if _, _, err := s.Accept("S1001", Result{}); err == nil {
		t.Fatal("missing run")
	}
	if _, _, err := s.Accept("S1001", result("1", "a", "")); err == nil {
		t.Fatal("missing revision")
	}
	mustAccept(t, s, Result{RunID: "1", Summary: "読む結果です"})
	for _, c := range []Completion{{OperationID: "x", Actor: "owner", Through: 2}, {OperationID: "x", Actor: "owner", Sequences: []uint64{0}}, {OperationID: "x", Actor: "owner", Through: 1, Sequences: []uint64{1}}} {
		if _, err := s.Complete("S1001", c); err == nil {
			t.Fatal("bad completion", c)
		}
	}
	if err := s.MarkNotified("S1001", 2); err == nil {
		t.Fatal("missing receipt")
	}
	data, err := json.Marshal(Result{RunID: "1", Nothing: true, Unverified: []string{"未確認です"}})
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err = json.Unmarshal(data, &r); err != nil || !r.Nothing || len(r.Unverified) != 1 {
		t.Fatal(r, err)
	}
}

func TestRecordKeepsEveryFailureAndOldReceiptLinks(t *testing.T) {
	s := NewStore(t.TempDir())
	old := mustAccept(t, s, result("old", "issue", "1"))
	if _, err := s.Complete("S1001", Completion{OperationID: "legacy", Actor: "owner", Through: old.Sequence}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, outcome string }{{"first", "failed"}, {"second", "failed"}, {"skipped", "skipped"}, {"empty", "nothing"}} {
		input := Result{RunID: tc.id, Summary: "記録です。", ProblemKey: "same"}
		first, err := s.Record("S1001", tc.outcome, input)
		if err != nil || first.Outcome != tc.outcome {
			t.Fatal(first, err)
		}
		again, err := s.Record("S1001", tc.outcome, input)
		if err != nil || again.Sequence != first.Sequence {
			t.Fatal(again, err)
		}
	}
	got, err := NewStore(s.Root).Snapshot("S1001")
	if err != nil || len(got.Receipts) != 5 || !got.Receipts[0].Reviewed || got.Receipts[0].Sequence != old.Sequence {
		t.Fatal(got, err)
	}
}

func TestRenamedRoutineKeepsReceiptsAndSequence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".lacia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"format":2,"areas":["work"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	routines := filepath.Join(root, "areas", "work", "routines")
	previous := filepath.Join(routines, "S1016")
	current := filepath.Join(routines, "S0002")
	if err := os.MkdirAll(previous, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "routine.md"), []byte("---\nid: S1016\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(root)
	if receipt, created, err := s.Accept("S1016", result("old-run", "issue", "1")); err != nil || !created || receipt.Sequence != 1 {
		t.Fatalf("original receipt: %+v, %v, %v", receipt, created, err)
	}
	if err := s.MarkNotified("S1016", 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(previous, current); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "routine.md"), []byte("---\nid: S0002\nlegacy_name: S1016\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if receipt, created, err := s.Accept("S0002", result("old-run", "issue", "1")); err != nil || created || receipt.Sequence != 1 {
		t.Fatalf("old receipt after the move: %+v, %v, %v", receipt, created, err)
	}
	if receipt, created, err := s.Accept("S0002", result("new-run", "issue", "2")); err != nil || !created || receipt.Sequence != 2 {
		t.Fatalf("new receipt after the move: %+v, %v, %v", receipt, created, err)
	}
	snapshot, err := s.Snapshot("S0002")
	if err != nil || len(snapshot.History) != 3 || snapshot.History[0].RoutineID != "S1016" || snapshot.History[2].RoutineID != "S0002" {
		t.Fatalf("history after the move: %+v, %v", snapshot.History, err)
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func newRoutineActionRuntime(t *testing.T, keepsTasks bool) routineActionRuntime {
	t.Helper()
	root, state := t.TempDir(), t.TempDir()
	for _, dir := range []string{".lacia", "areas/work/routines/S1013", "areas/work/routines/S1014"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := json.Marshal(map[string]any{"format": 2, "tasks": keepsTasks, "areas": []string{"work"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lacia", "config.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"S1013", "S1014"} {
		text := "---\nid: " + id + "\ntitle: 定期タスクです\nevery: 30m\n---\n\n内容です。\n"
		if err := os.WriteFile(filepath.Join(root, "areas/work/routines", id, "routine.md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return routineActionRuntime{state: testDir(t, state), tasks: store, receipts: routine.NewStore(root)}
}

func (a routineActionRuntime) testMode(t *testing.T, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.state.Path(), "routine-actions-mode"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRoutineActionPrepareAndMoveExistingHandoff(t *testing.T) {
	a := newRoutineActionRuntime(t, true)
	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	linked, err := a.tasks.Create(task.NewTask{Title: "既存の対応", Area: "work", Status: task.Review})
	if err != nil {
		t.Fatal(err)
	}
	matter := routine.Matter{Key: "one", Revision: "old", Summary: "既存の依頼を確かめます。"}
	if _, _, err := a.receipts.Accept("S1013", routine.Result{RunID: "old", Matters: []routine.Matter{matter}}); err != nil {
		t.Fatal(err)
	}
	a.testMode(t, "prepare")
	plan := []routineBaselineItem{{ID: "S1013", At: at, Actions: []task.RoutineAction{{
		Key: "handoff:one", Revision: "old", Text: "通常タスクに引き継ぎました。", LinkedTask: linked.ID, Reference: "/tasks/" + linked.ID,
	}}}}
	plan = append(plan, routineBaselineItem{ID: "S1014", At: at, Silent: true, Actions: []task.RoutineAction{{
		Key: "matter:already-delivered", Revision: "1", Text: "すでに伝えた操作をお願いします。", Owner: true,
	}}})
	raw, _ := json.Marshal(plan)
	if err := os.WriteFile(filepath.Join(a.state.Path(), "routine-actions-baseline.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if a.enabled() {
		t.Fatal("preparation must keep the old view")
	}
	if err := a.applyBaseline(); err != nil {
		t.Fatal(err)
	}
	if err := a.applyBaseline(); err != nil {
		t.Fatal(err)
	}
	migrated, err := a.tasks.Get("S1014")
	if err != nil || migrated.Status != task.Review || len(migrated.RoutineActions()) != 1 {
		t.Fatalf("migrated review = %+v, %v", migrated, err)
	}
	if sequence, _, _ := migrated.RoutineActionNotice(); sequence != 0 {
		t.Fatal("an already delivered request would be sent again")
	}
	old, err := a.receipts.Snapshot("S1013")
	if err != nil || len(old.Pending) != 1 || old.Pending[0].RunID != "old" {
		t.Fatalf("old queue during preparation = %+v, %v", old, err)
	}
	newRun := schedule.Run{ID: "next", At: at.Add(time.Minute), Outcome: schedule.RunDone, Matters: []routine.Matter{matter}}
	matter.Revision = "new"
	newRun.Matters[0] = matter
	a.testMode(t, "tasks")
	if err := a.observe("S1013", newRun); err != nil {
		t.Fatal(err)
	}
	if err := a.observe("S1013", newRun); err != nil {
		t.Fatal(err)
	}
	current, err := a.tasks.Get("S1013")
	if err != nil || current.Status != task.Waiting || len(current.RoutineActions()) != 1 || !strings.Contains(current.Body, "/tasks/"+linked.ID) {
		t.Fatalf("continued link = %+v, %v", current, err)
	}
	reopened, err := a.receipts.Snapshot("S1013")
	if err != nil || len(reopened.Receipts) != 2 || reopened.Receipts[0].RunID != "old" || reopened.Receipts[1].RunID != newRun.ID {
		t.Fatalf("receipts = %+v, %v", reopened, err)
	}
}

func TestRoutineActionRecordWithoutTaskSupport(t *testing.T) {
	a := newRoutineActionRuntime(t, false)
	a.testMode(t, "tasks")
	run := schedule.Run{ID: "failure", At: time.Now(), Outcome: schedule.RunFailed, FailureKind: "execution", Summary: "接続できませんでした。"}
	if err := a.observe("S1014", run); err != nil {
		t.Fatal(err)
	}
	receipt, err := a.receipts.Snapshot("S1014")
	if err != nil || len(receipt.Receipts) != 1 || receipt.Receipts[0].Outcome != schedule.RunFailed || len(a.tasks.List()) != 0 {
		t.Fatalf("history, no tasks: %+v, %v", receipt, err)
	}
	if _, err := a.tasks.Get("S1014"); err == nil {
		t.Fatal("routine task should not exist")
	}
}

func TestRoutineMatterWithoutSummaryUsesConfiguredName(t *testing.T) {
	previous := task.OwnerName
	t.Cleanup(func() { task.OwnerName = previous })
	task.OwnerName = "佐藤さん"
	run := schedule.Run{Outcome: schedule.RunDone, Matters: []routine.Matter{{Key: "mail/1", Revision: "a"}}}
	got := runActions(run, "").Actions
	if len(got) != 1 || !strings.HasPrefix(got[0].Text, "佐藤さんに、") {
		t.Fatalf("fallback for matter without a summary: %+v", got)
	}
}

func TestRoutineActionInstructionNamesTheRoutineTask(t *testing.T) {
	a := newRoutineActionRuntime(t, true)
	job := schedule.Job{ID: "S1013", Name: "S1013", Output: "matters"}
	for _, mode := range []string{"", "prepare"} {
		a.testMode(t, mode)
		if got := a.instruction(job); got != "" || a.collects(job) {
			t.Fatalf("mode %q: %q", mode, got)
		}
	}
	a.testMode(t, "tasks")
	// The routine has no task.md, and the text must still say where matters go.
	got := a.instruction(job)
	for _, want := range []string{"結果の JSON の matters に書いてください", "S1013 の task.md の「実行から分かった対応」にまとめます", "通常のタスクを新しく作るのは",
		"この定期タスクの「すること」で、どのタスクを作るか指定されているときだけ", "同じ用件をすでに追っている通常のタスクへ引き継ぐとき", "handoffs", "resolved"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	job.MattersTask = "S1014"
	got = a.instruction(job)
	if !strings.Contains(got, "定期タスク S1014 が用件の引き継ぎ先です") || strings.Contains(got, "lacia task show S1014") {
		t.Errorf("another routine without a task yet: %s", got)
	}
	if _, err := a.tasks.MergeRoutineActions("S1014", task.RoutineObservation{At: time.Now(), Shared: true, Actions: []task.RoutineAction{{Key: "matter:x", Revision: "1", Text: "確かめます。", Owner: true}}}); err != nil {
		t.Fatal(err)
	}
	if got = a.instruction(job); !strings.Contains(got, "`lacia task show S1014`") {
		t.Errorf("another routine with a task: %s", got)
	}
	weekly := schedule.Job{ID: "S1008", Name: "S1008", Output: "task"}
	if got := a.instruction(weekly); a.collects(weekly) || strings.Contains(got, "通常のタスクを新しく作るのは") || !strings.Contains(got, "同じ用件をすでに追っているタスクがあれば、そちらへ引き継いでください") {
		t.Errorf("a routine whose output is a task: %s", got)
	}
	if got := newRoutineActionRuntime(t, false); got.instruction(job) != "" {
		t.Error("a repository without tasks got the text")
	}
}

func TestRoutineActionMattersCollectInAnotherRoutine(t *testing.T) {
	a := newRoutineActionRuntime(t, true)
	root := a.tasks.Root()
	definition := "---\nid: S1013\ntitle: 夕方の確認です\ndaily: \"17:00\"\nmatters_task: S1014\n---\n\n内容です。\n"
	if err := os.WriteFile(filepath.Join(root, "areas/work/routines/S1013/routine.md"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	a.testMode(t, "tasks")
	morning := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	evening := morning.Add(8 * time.Hour)
	one := routine.Matter{Key: "mail/1", Revision: "a", Summary: "校正の依頼に返事をしていただきます。"}
	two := routine.Matter{Key: "mail/2", Revision: "a", Summary: "説明の日程を選んでいただきます。"}
	observe := func(id string, run schedule.Run) task.Task {
		t.Helper()
		if err := a.observe(id, run); err != nil {
			t.Fatal(err)
		}
		got, err := a.tasks.Get("S1014")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	keys := func(got task.Task) string {
		var out []string
		for _, item := range got.RoutineActions() {
			out = append(out, item.Key)
		}
		return strings.Join(out, ",")
	}

	got := observe("S1013", schedule.Run{ID: "evening-1", At: evening, Outcome: schedule.RunDone, Matters: []routine.Matter{one, two}})
	if got.Status != task.Review || keys(got) != "matter:mail/1,matter:mail/2" {
		t.Fatalf("collected in the other routine: %s %s", got.Status, keys(got))
	}
	// The item links back to the run of the routine that found it.
	if !strings.Contains(got.Body, "校正の依頼に返事をしていただきます。") || !strings.Contains(got.Body, "/routines/S1013?month=2026-11#") {
		t.Fatalf("body: %s", got.Body)
	}
	if sequence, text, count := got.RoutineActionNotice(); sequence != 1 || count != 2 || text != one.Summary {
		t.Fatalf("notice: %d %q %d", sequence, text, count)
	}
	if got.RoutineActionSummary() != one.Summary {
		t.Fatalf("home summary: %q", got.RoutineActionSummary())
	}
	if _, err := a.tasks.Get("S1013"); err == nil {
		t.Fatal("the routine that names another one got its own task")
	}
	if receipts, err := a.receipts.Snapshot("S1013"); err != nil || len(receipts.Receipts) != 1 || len(receipts.Receipts[0].Matters) != 2 {
		t.Fatalf("the receipt stays with the routine that ran: %+v %v", receipts, err)
	}

	// The morning routine's own result arrives late. Sharing must not have
	// moved its run clock, and the same matter stays one item.
	late := observe("S1014", schedule.Run{ID: "morning-1", At: morning, Outcome: schedule.RunFailed, FailureKind: "execution", Summary: "接続できませんでした。", Matters: []routine.Matter{one}})
	if keys(late) != "matter:mail/1,matter:mail/2,run:execution" {
		t.Fatalf("the routine's own late run: %s", keys(late))
	}
	if sequence, _, _ := late.RoutineActionNotice(); sequence != 1 {
		t.Fatalf("the same matter was announced again: %d", sequence)
	}

	// A failure of the evening routine is about that routine.
	failed := observe("S1013", schedule.Run{ID: "evening-2", At: evening.Add(24 * time.Hour), Outcome: schedule.RunFailed, FailureKind: "execution", Summary: "読めませんでした。"})
	if keys(failed) != "matter:mail/1,matter:mail/2,run:execution" {
		t.Fatalf("the other routine's task changed: %s", keys(failed))
	}
	own, err := a.tasks.Get("S1013")
	if err != nil || len(own.RoutineActions()) != 1 || own.RoutineActions()[0].Key != "run:execution" || own.Status != task.Waiting {
		t.Fatalf("the failure stays with the routine that ran: %+v %v", own, err)
	}

	// An older result of the evening routine cannot close a newer item.
	stale := task.RoutineObservation{At: morning.Add(-time.Hour), Shared: true, Resolved: []string{"matter:mail/2"}}
	if _, err := a.tasks.MergeRoutineActions("S1014", stale); err != nil {
		t.Fatal(err)
	}
	// The evening run closes one matter and then the other.
	got = observe("S1013", schedule.Run{ID: "evening-3", At: evening.Add(48 * time.Hour), Outcome: schedule.RunDone, Resolved: []string{"mail/1"}})
	if got.Status != task.Review || keys(got) != "matter:mail/2,run:execution" {
		t.Fatalf("one resolved: %s %s", got.Status, keys(got))
	}
	got = observe("S1013", schedule.Run{ID: "evening-4", At: evening.Add(72 * time.Hour), Outcome: schedule.RunNothing, Resolved: []string{"mail/2"}})
	if got.Status != task.Waiting || keys(got) != "run:execution" {
		t.Fatalf("all resolved: %s %s", got.Status, keys(got))
	}
	if own, err := a.tasks.Get("S1013"); err != nil || len(own.RoutineActions()) != 0 {
		t.Fatalf("the routine's own failure cleared by its success: %+v %v", own, err)
	}

	// A handoff from the evening run is followed in the collecting task too.
	linked, err := a.tasks.Create(task.NewTask{Title: "既存の対応", Area: "work", Status: task.Review})
	if err != nil {
		t.Fatal(err)
	}
	got = observe("S1013", schedule.Run{ID: "evening-5", At: evening.Add(96 * time.Hour), Outcome: schedule.RunDone,
		Handoffs: []routine.Handoff{{Matter: routine.Matter{Key: "mail/3", Revision: "a", Summary: "既存のタスクで追います。"}, TaskID: linked.ID}}})
	if keys(got) != "handoff:mail/3,run:execution" || got.Status != task.Waiting || !strings.Contains(got.Body, "/tasks/"+linked.ID) {
		t.Fatalf("handoff: %s %s", keys(got), got.Body)
	}

	// Without the field, a routine keeps its matters in its own task.
	plain := observe("S1014", schedule.Run{ID: "morning-2", At: evening.Add(100 * time.Hour), Outcome: schedule.RunDone, Matters: []routine.Matter{two}})
	if keys(plain) != "matter:mail/2,handoff:mail/3" || plain.Status != task.Review {
		t.Fatalf("own matters: %s %s", keys(plain), plain.Status)
	}
}

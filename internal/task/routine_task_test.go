package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRoutineStore makes a repository of format 2 with the routine S1011 in
// the work area, which has no task yet.
func newRoutineStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"format": 2, "areas": ["work", "personal"]}`), 0o644)
	dir := filepath.Join(root, "areas", "work", "routines", "S1011")
	os.MkdirAll(filepath.Join(dir, "runs", "2026-11"), 0o755)
	os.WriteFile(filepath.Join(dir, "routine.md"), []byte("---\nid: S1011\ntitle: \"クラスタ操作コマンドの認証を確かめる\"\ndaily: \"09:00\"\ncheck_scopes: cluster:cluster-a/auth, cluster:cluster-b/auth\n---\n\n## めざすこと\n\n確かめます。\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "runs", "2026-11", "runs.jsonl"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "procedure.md"), []byte("# 手順\n"), 0o644)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC) } // 11:00 JST
	return s
}

const routineBody = "## ひとことで\n\nクラスタ操作コマンドの認証を毎朝確かめる定期タスクです。いまは3つのクラスタとも確かめられています。\n\n## 次にすること\n\n次の実行で確かめます。オーナーさんにお願いすることはありません。\n"

func TestRoutineTaskIsCreatedByTheFirstWrite(t *testing.T) {
	s := newRoutineStore(t)
	if _, err := s.Get("S1011"); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "lacia task write S1011 task.md") {
		t.Fatalf("before the first write: %v", err)
	}
	if _, err := s.WriteDoc("S1011", NotesFile, "# S1011 の補足\n", "local-claude"); err == nil || !strings.Contains(err.Error(), "先に lacia task write S1011 task.md") {
		t.Fatalf("notes before the task: %v", err)
	}
	if _, err := s.WriteDoc("S1011", TaskFile, "---\nid: S1011\n---\n\n"+routineBody, "local-claude"); !errors.Is(err, ErrShape) {
		t.Fatalf("front matter was taken: %v", err)
	}
	if _, err := s.WriteDoc("S1011", TaskFile, "## ひとことで\n\n次にすることがありません。\n", "local-claude"); !errors.Is(err, ErrShape) {
		t.Fatalf("a body without the next step was taken: %v", err)
	}
	path := filepath.Join(s.Root(), "areas", "work", "routines", "S1011", TaskFile)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused write left task.md: %v", err)
	}

	got, err := s.WriteDoc("s1011", TaskFile, routineBody, "local-claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "S1011" || !got.Routine || got.Status != Waiting || got.Title != "クラスタ操作コマンドの認証を確かめる" || got.Dir != "areas/work/routines/S1011" || got.Area != "work" {
		t.Fatalf("created = %+v", got)
	}
	data, _ := os.ReadFile(path)
	if want := "---\nid: S1011\nstatus: waiting\ncreated: 2026-11-27\n---\n\n" + routineBody; string(data) != want {
		t.Fatalf("task.md =\n%s\nwant\n%s", data, want)
	}
	lines, _ := s.HistoryLines(got, 0)
	if len(lines) != 2 || !strings.Contains(lines[0], "定期タスクのタスクを作りました。状態は「外部待ち」です。") || !strings.Contains(lines[1], "task.md を書き換えました") {
		t.Fatalf("history = %q", lines)
	}

	// The task is found by ID and keeps out of the task lists.
	if again, err := s.Get("S1011"); err != nil || !again.Routine {
		t.Fatalf("get = %+v, %v", again, err)
	}
	for _, listed := range s.List() {
		if listed.ID == "S1011" {
			t.Fatal("a routine's task is in the task list")
		}
	}

	// Later writes replace the body and keep the front matter.
	body := strings.Replace(routineBody, "確かめられています", "cluster-bだけ確かめられていません", 1)
	if _, err := s.WriteDoc("S1011", TaskFile, body, "local-claude"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.HasPrefix(string(data), "---\nid: S1011\nstatus: waiting\ncreated: 2026-11-27\n---\n") || !strings.Contains(string(data), "cluster-bだけ") {
		t.Fatalf("rewritten task.md =\n%s", data)
	}
	if _, err := s.WriteDoc("S1011", NotesFile, "# S1011 の補足\n\n## ここまでできたら完了\n\n定期タスクなので、完了しません。\n", "local-claude"); err != nil {
		t.Fatalf("notes: %v", err)
	}
	if _, err := s.PutFile("S1011", ArtifactsDir, "plan.md", strings.NewReader("# 計画\n"), "local-claude"); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if err := s.Log("S1011", Event{Type: EventNote, Text: "受付3を受けて本文を直しました。"}); err != nil {
		t.Fatalf("note: %v", err)
	}

	// Another store reads the same files the same way.
	reopened, err := Open(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	if problems := reopened.Problems(); len(problems) > 0 {
		t.Fatalf("problems = %+v", problems)
	}
	if again, err := reopened.Get("S1011"); err != nil || !again.Routine || again.Title != "クラスタ操作コマンドの認証を確かめる" {
		t.Fatalf("reopened = %+v, %v", again, err)
	}
	if len(reopened.List()) != 0 {
		t.Fatalf("list = %+v", reopened.List())
	}
}

func TestRoutineTaskHasOnlyThreeStatusesAndNoFields(t *testing.T) {
	s := newRoutineStore(t)
	if _, err := s.WriteDoc("S1011", TaskFile, routineBody, "local-claude"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{Todo, Closing, Done, Cancelled} {
		if _, _, err := s.SetStatus("S1011", status, "Web", ""); !errors.Is(err, ErrRoutineStatus) {
			t.Errorf("%s: err = %v", status, err)
		}
	}
	for _, status := range []Status{Review, Doing, Waiting} {
		if got, changed, err := s.SetStatus("S1011", status, "Web", ""); err != nil || !changed || got.Status != status {
			t.Errorf("%s: %+v, %v, %v", status, got, changed, err)
		}
	}
	if got, changed, err := s.Claim("S1011", Owner{Label: "local-codex", Session: "abc"}, "local-codex", ""); err != nil || !changed || got.Status != Doing || got.Owner != "local-codex" {
		t.Fatalf("claim = %+v, %v, %v", got, changed, err)
	}
	if got, _, err := s.Stop("S1011", "同じ書き込みが続きました。"); err != nil || got.Status != Review {
		t.Fatalf("stop = %+v, %v", got, err)
	}
	for _, key := range []string{"title", "due", "parent", "after", "after_run", "resume_on", "review_at", "on_ready"} {
		if _, _, err := s.SetField("S1011", key, "", "Web"); !errors.Is(err, ErrRoutineField) {
			t.Errorf("%s: err = %v", key, err)
		}
	}

	// A routine's task is not something a task may wait for or belong to.
	other, err := s.Create(NewTask{Area: "work", Title: "通常のタスク"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetField(other.ID, "after", "S1011", "Web"); err == nil || !strings.Contains(err.Error(), "after_run") {
		t.Errorf("after a routine: %v", err)
	}
	if _, _, err := s.SetField(other.ID, "parent", "S1011", "Web"); err == nil || !strings.Contains(err.Error(), "定期タスク") {
		t.Errorf("parent a routine: %v", err)
	}
	if _, err := s.Create(NewTask{Area: "work", Title: "子のタスク", Parent: "S1011"}); err == nil || !strings.Contains(err.Error(), "定期タスク") {
		t.Errorf("child of a routine: %v", err)
	}
	if woken := s.WakeWithRuns(s.now(), nil); len(woken) != 0 {
		t.Errorf("woken = %+v", woken)
	}
}

func TestRoutineTaskWrittenDirectlyIsFoundAndChecked(t *testing.T) {
	s := newRoutineStore(t)
	dir := filepath.Join(s.Root(), "areas", "work", "routines", "S1011")
	os.WriteFile(filepath.Join(dir, TaskFile), []byte("---\nid: S1011\nstatus: review\ncreated: 2026-11-27\n---\n\n"+strings.Replace(routineBody, "オーナーさんにお願いすることはありません。", "オーナーさんに、cluster-bの認証をやり直していただきます。", 1)), 0o644)
	if err := s.Refresh("S1011"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, err := s.Get("S1011")
	if err != nil || got.Status != Review || !got.Routine {
		t.Fatalf("got = %+v, %v", got, err)
	}
	// The definition, procedure and runs next to task.md are not stray files.
	for _, w := range Check(got, s.Files(got)) {
		if strings.Contains(w.Message, "タスクのディレクトリーに置くファイルではありません") {
			t.Errorf("warning = %s", w)
		}
	}
	if got.MissingWaitingCondition(false) {
		t.Error("a routine's task needs no wait condition")
	}

	// A status a routine's task cannot have makes the file unreadable,
	// and the problem is shown instead of the task being dropped silently.
	os.WriteFile(filepath.Join(dir, TaskFile), []byte("---\nid: S1011\nstatus: done\ncreated: 2026-11-27\n---\n\n"+routineBody), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if problems := s.Problems(); len(problems) != 1 || !strings.Contains(problems[0].Err, "定期タスクのタスクでは使えません") {
		t.Fatalf("problems = %+v", problems)
	}
}

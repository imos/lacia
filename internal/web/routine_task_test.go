package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/task"
)

// setupAreaRoutine makes a repository of format 2 with two routines in the
// work area: S1011, which may get a task, and S1012, which never does.
func setupAreaRoutine(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lacia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"format":2,"areas":["work"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for id, title := range map[string]string{"S1011": "クラスタ操作コマンドの認証を確かめる", "S1012": "共有の予定を確かめる"} {
		dir := filepath.Join(root, "areas", "work", "routines", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		def := "---\nid: " + id + "\ntitle: " + title + "\ndaily: \"09:00\"\n---\n\n## めざすこと\n\n" + title + "ための定期タスクです。\n\n## すること\n\n手順に従います。\n"
		if err := os.WriteFile(filepath.Join(dir, "routine.md"), []byte(def), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, Routines: routine.NewStore(root)}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return s, h
}

const routineTaskBody = "## ひとことで\n\nクラスタ操作コマンドの認証を毎朝確かめる定期タスクです。3つのクラスタとも、11月26日から確かめられていません。\n\n## 次にすること\n\nオーナーさんに、cluster-bの認証をやり直していただきます。済んだら、次の実行でわたしが確かめます。\n\n## いまの資料\n\n- [認証の手順](artifacts/steps.md)\n- [受付3](/routines/S1011/receipts/3)\n"

func TestRoutineTaskThroughTheAPI(t *testing.T) {
	s, h := setupAreaRoutine(t)
	claude := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}
	if code, out := putDoc(h, claude, "/api/tasks/S1011/doc?name=notes.md", "# S1011 の補足\n"); code != 400 || !strings.Contains(out["error"].(string), "先に lacia task write") {
		t.Fatalf("notes first: %d %v", code, out)
	}
	code, out := putDoc(h, claude, "/api/tasks/S1011/doc?name=task.md", routineTaskBody)
	if code != 200 {
		t.Fatalf("first write: %d %v", code, out)
	}
	got, err := s.Store.Get("S1011")
	if err != nil || !got.Routine || got.Status != task.Waiting || got.Body != routineTaskBody {
		t.Fatalf("task = %+v, %v", got, err)
	}
	if code, out := putDoc(h, claude, "/api/tasks/S1099/doc?name=task.md", routineTaskBody); code != 404 {
		t.Fatalf("a routine that does not exist: %d %v", code, out)
	}

	if rec := post(h, "/tasks/S1011/status", "", map[string][]string{"status": {"done"}}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "定期タスクのタスクの状態は") {
		t.Fatalf("done from the page: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(h, "/tasks/S1011/status", "", map[string][]string{"status": {"review"}}); rec.Code != 303 {
		t.Fatalf("review from the page: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := s.Store.Get("S1011"); got.Status != task.Review {
		t.Fatalf("status = %s", got.Status)
	}

	// The task page of a routine is the routine's page.
	code, _, header := get(t, h, "/tasks/S1011")
	if code != http.StatusFound || header.Get("Location") != "/routines/S1011#task" {
		t.Fatalf("task page: %d %v", code, header)
	}
	// The task lists leave it out.
	_, list, _ := get(t, h, "/api/tasks")
	if strings.Contains(list, "S1011") {
		t.Fatalf("api list: %s", list)
	}
	_, detail, _ := get(t, h, "/api/tasks/S1011?detail=1")
	if !strings.Contains(detail, `"Routine":true`) || !strings.Contains(detail, "クラスタ操作コマンドの認証を確かめる") {
		t.Fatalf("detail: %s", detail)
	}
}

// sectionOrder returns where each marker first appears in the page, so a
// test can say which part comes before which.
func sectionOrder(t *testing.T, body string, markers ...string) []int {
	t.Helper()
	var at []int
	for _, m := range markers {
		i := strings.Index(body, m)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", m, body)
		}
		at = append(at, i)
	}
	return at
}

func ascending(at []int) bool {
	for i := 1; i < len(at); i++ {
		if at[i-1] >= at[i] {
			return false
		}
	}
	return true
}

func TestRoutinePageWithoutATaskStartsWithReceipts(t *testing.T) {
	s, h := setupAreaRoutine(t)
	acceptRoutine(t, s, "S1012", "朝")
	code, body, _ := get(t, h, "/routines/S1012")
	if code != 200 {
		t.Fatalf("page %d: %s", code, body)
	}
	if strings.Contains(body, `id="task"`) || strings.Contains(body, "タスク 待機") {
		t.Fatalf("a routine without task.md shows a task part:\n%s", body)
	}
	at := sectionOrder(t, body, "<h1>", "次は", `id="receipts"`, "朝の結果です。", `id="definition"`, "共有の予定を確かめるための定期タスクです。", `id="records"`, "すべての範囲を確かめた記録はまだありません。", "実行の履歴")
	if !ascending(at) {
		t.Fatalf("order = %v:\n%s", at, body)
	}
	// The definition's headings are set apart from the page's own.
	if !strings.Contains(body, `<div class="markdown routine-prompt"><h2 id="heading">めざすこと</h2>`) {
		t.Errorf("definition:\n%s", body)
	}
}

func TestRoutinePageShowsItsTaskFirst(t *testing.T) {
	s, h := setupAreaRoutine(t)
	acceptRoutine(t, s, "S1011", "朝")
	claude := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}
	if code, out := putDoc(h, claude, "/api/tasks/S1011/doc?name=task.md", routineTaskBody); code != 200 {
		t.Fatalf("write: %d %v", code, out)
	}
	if _, err := s.Store.PutFile("S1011", task.ArtifactsDir, "steps.md", strings.NewReader("# 認証の手順\n\n手順です。\n"), "local-claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.WriteDoc("S1011", task.NotesFile, "# S1011 の補足\n\n## ここまでできたら完了\n\n定期タスクなので、完了しません。\n", "local-claude"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Store.SetStatus("S1011", task.Review, "local-claude", ""); err != nil {
		t.Fatal(err)
	}

	code, body, _ := get(t, h, "/routines/S1011")
	if code != 200 {
		t.Fatalf("page %d: %s", code, body)
	}
	at := sectionOrder(t, body, "<h1>", "タスク 確認待ち", `id="task"`, "cluster-bの認証をやり直していただきます", `id="receipts"`, "朝の結果です。", `id="definition"`, "クラスタ操作コマンドの認証を確かめるための定期タスクです。", `id="records"`, "実行の履歴", "タスクと定義の変更の履歴", "定期タスクのタスクを作りました。")
	if !ascending(at) {
		t.Fatalf("order = %v:\n%s", at, body)
	}
	for _, want := range []string{
		`href="/routines/S1011/files/artifacts/steps.md">認証の手順</a>`,         // the list of artifacts
		`<a href="/routines/S1011/files/artifacts/steps.md">認証の手順</a></li>`, // the link in the body
		`<a href="/routines/S1011/receipts/3">受付3</a>`,
		`<summary>補足</summary>`,
		`<button name="status" value="waiting" class="primary">待機に戻す</button>`,
		`<button name="status" value="doing" >進行中にする</button>`,
		`action="/routines/S1011/status"`,
		`href="/tasks/S1011/log"`,
		"タスクの状態は変わりません。",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}

	// The status buttons keep to the three statuses and come back here.
	rec := post(h, "/routines/S1011/status", "", map[string][]string{"status": {"waiting"}})
	if rec.Code != 303 || !strings.HasSuffix(rec.Header().Get("Location"), "#task") || !strings.Contains(rec.Header().Get("Location"), "flash=") {
		t.Fatalf("status: %d %v", rec.Code, rec.Header())
	}
	if got, _ := s.Store.Get("S1011"); got.Status != task.Waiting {
		t.Fatalf("status = %s", got.Status)
	}
	if rec := post(h, "/routines/S1011/status", "", map[string][]string{"status": {"done"}}); rec.Code != 400 {
		t.Fatalf("done: %d", rec.Code)
	}
	if rec := post(h, "/routines/S1012/status", "", map[string][]string{"status": {"review"}}); rec.Code != 404 {
		t.Fatalf("a routine without a task: %d", rec.Code)
	}
	_, body, _ = get(t, h, "/routines/S1011")
	if !strings.Contains(body, "タスク 待機") || !strings.Contains(body, `value="review" >確認待ちにする</button>`) {
		t.Fatalf("waiting page:\n%s", body)
	}

	// Completing the receipts leaves the task alone.
	token := viewToken(t, h, "S1011")
	if rec := post(h, "/routines/S1011/complete", "", map[string][]string{"view": {token}}); rec.Code != 303 {
		t.Fatalf("complete: %d", rec.Code)
	}
	if got, _ := s.Store.Get("S1011"); got.Status != task.Waiting {
		t.Fatalf("status after completing receipts = %s", got.Status)
	}

	// The task's files open under the routine, and nothing outside it does.
	code, file, _ := get(t, h, "/routines/S1011/files/artifacts/steps.md")
	if code != 200 || !strings.Contains(file, "手順です。") || !strings.Contains(file, `href="/tasks/S1011"`) {
		t.Fatalf("artifact %d:\n%s", code, file)
	}
	for _, outside := range []string{"/routines/S1011/files/../S1012/routine.md", "/routines/S1011/files/%2e%2e/S1012/routine.md", "/routines/S1099/files/task.md"} {
		if code, _, _ := get(t, h, outside); code != 404 && code != 301 && code != 307 && code != 308 {
			t.Errorf("%s: %d", outside, code)
		}
	}
}

func TestRoutineTaskInReviewSharesTheRoutinesRowInTheActionList(t *testing.T) {
	s, h := setupAreaRoutine(t)
	claude := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}
	if code, out := putDoc(h, claude, "/api/tasks/S1011/doc?name=task.md", routineTaskBody); code != 200 {
		t.Fatalf("write: %d %v", code, out)
	}
	// Waiting for the next run is nothing to act on.
	_, home, _ := get(t, h, "/")
	if strings.Contains(home, `href="/routines/S1011"`) {
		t.Fatalf("a waiting routine task is in the action list:\n%s", home)
	}

	if _, _, err := s.Store.SetStatus("S1011", task.Review, "local-claude", ""); err != nil {
		t.Fatal(err)
	}
	_, home, _ = get(t, h, "/")
	if strings.Count(home, `class="id" href="/routines/S1011"`) != 1 || !strings.Contains(home, "オーナーさんに、cluster-bの認証をやり直していただきます。") || !strings.Contains(home, `href="/routines/S1011#task">タスクを開く</a>`) {
		t.Fatalf("review row:\n%s", home)
	}

	// With unconfirmed receipts as well, it is still one row with both.
	acceptRoutine(t, s, "S1011", "朝")
	_, home, _ = get(t, h, "/")
	if strings.Count(home, `class="id" href="/routines/S1011"`) != 1 || !strings.Contains(home, "未確認の結果が1回分あります。") || !strings.Contains(home, "cluster-bの認証をやり直していただきます") {
		t.Fatalf("review row with receipts:\n%s", home)
	}

	// The receipt page leads back to the task.
	_, receipt, _ := get(t, h, "/routines/S1011/receipts/1")
	if !strings.Contains(receipt, `<a href="/routines/S1011#task">この定期タスクの「タスク」欄</a>（確認待ち）`) {
		t.Fatalf("receipt page:\n%s", receipt)
	}
	_, other, _ := get(t, h, "/routines/S1012")
	if strings.Contains(other, "この定期タスクの「タスク」欄</a>") {
		t.Fatalf("a routine without a task links to one:\n%s", other)
	}
}

package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

const closingNext = "表示を直して、本番に反映しました。結果のページで見ていただけます。"

// closingSetup adds a task that only waits for its finished work to be
// confirmed (T1003) to the fixture, next to the one in review (T1002).
func closingSetup(t *testing.T) (*task.Store, http.Handler) {
	t.Helper()
	store, h := setup(t)
	finished, err := store.Create(task.NewTask{Area: "work", Title: "終わったタスク", Status: task.Closing})
	if err != nil || finished.ID != "T1003" {
		t.Fatalf("create: %+v, %v", finished, err)
	}
	path := filepath.Join(store.Root(), finished.Dir, "task.md")
	data, _ := os.ReadFile(path)
	body := strings.Replace(string(data), "## 次にすること\n\n"+task.Placeholder, "## 次にすること\n\n"+closingNext+"\n\n直したいところがあれば、依頼に書いてください。", 1)
	if body == string(data) {
		t.Fatalf("the new task has no placeholder to replace:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	return store, h
}

func TestHomeListsClosingBelowReviewWithAButtonToClose(t *testing.T) {
	_, h := closingSetup(t)
	code, home, _ := get(t, h, "/")
	if code != 200 {
		t.Fatalf("home: %d", code)
	}
	review := strings.Index(home, `<section class="list review">`)
	closing := strings.Index(home, `<section class="list closing">`)
	ongoing := strings.Index(home, `<section class="list ongoing">`)
	if review < 0 || closing < review || ongoing < closing {
		t.Fatalf("sections out of order (review %d, closing %d, ongoing %d):\n%s", review, closing, ongoing, home)
	}
	section := home[closing:ongoing]
	for _, want := range []string{
		"完了確認待ち <span class=\"count\">1</span>",
		"作業は終わっています。成果を見て、よければ完了にしてください。",
		"終わったタスク",
		`<p class="next">` + closingNext + `</p>`,
		`<form method="post" action="/tasks/T1003/status" class="start"><input type="hidden" name="back" value="home"><input type="hidden" name="status" value="done"><button class="primary">完了にする</button></form>`,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the closing section lacks %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "T1002") || strings.Contains(section, "直したいところ") {
		t.Errorf("the closing section shows another task or more than the first paragraph:\n%s", section)
	}
	// The task in review looks as before: no button to close it from the row.
	asked := home[review:closing]
	if !strings.Contains(asked, "確認してほしいタスク") || !strings.Contains(asked, "佐藤さんが結果を確認します。") || strings.Contains(asked, "完了にする") || strings.Contains(asked, "T1003") {
		t.Errorf("the review section changed:\n%s", asked)
	}
	if strings.Contains(home, "取り消す") {
		t.Errorf("nothing was closed, yet the page offers to take it back")
	}
}

func TestClosingFromTheRowCanBeTakenBack(t *testing.T) {
	store, h := closingSetup(t)
	rec := post(h, "/tasks/T1003/status", "http://example.com", url.Values{"status": {"done"}, "back": {"home"}})
	location := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(location, "/?flash=") || !strings.HasSuffix(location, "&undo=T1003") {
		t.Fatalf("closing from the row: %d %s", rec.Code, location)
	}
	if got, _ := store.Get("T1003"); got.Status != task.Done || got.Done == "" {
		t.Fatalf("after closing: %+v", got)
	}
	_, home, _ := get(t, h, location)
	undo := `<form method="post" action="/tasks/T1003/status" class="undo"><input type="hidden" name="back" value="home"><input type="hidden" name="status" value="closing"><button>取り消す</button>`
	if !strings.Contains(home, "T1003 を「完了」にしました。") || !strings.Contains(home, undo) || strings.Contains(home, `<section class="list closing">`) {
		t.Fatalf("the page after closing lacks the notice or the way back:\n%s", home)
	}

	rec = post(h, "/tasks/T1003/status", "http://example.com", url.Values{"status": {"closing"}, "back": {"home"}})
	location = rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(location, "/?flash=") || strings.Contains(location, "undo=") {
		t.Fatalf("taking it back: %d %s", rec.Code, location)
	}
	if got, _ := store.Get("T1003"); got.Status != task.Closing || got.Done != "" {
		t.Fatalf("after taking it back: %+v", got)
	}
	_, home, _ = get(t, h, location)
	if !strings.Contains(home, "T1003 を「完了確認待ち」にしました。") || !strings.Contains(home, `<section class="list closing">`) || strings.Contains(home, "取り消す") {
		t.Errorf("the page after taking it back:\n%s", home)
	}
}

func TestOnlyClosingFromTheRowOffersToTakeItBack(t *testing.T) {
	store, h := closingSetup(t)
	// From the task page: the page itself has the buttons.
	rec := post(h, "/tasks/T1003/status", "http://example.com", url.Values{"status": {"done"}})
	if location := rec.Header().Get("Location"); !strings.HasPrefix(location, "/tasks/T1003?flash=") || strings.Contains(location, "undo=") {
		t.Errorf("closing from the task page: %s", location)
	}
	// A task in review closed with back=home, as the form allows.
	rec = post(h, "/tasks/T1002/status", "http://example.com", url.Values{"status": {"done"}, "back": {"home"}})
	if location := rec.Header().Get("Location"); !strings.HasPrefix(location, "/?flash=") || strings.Contains(location, "undo=") {
		t.Errorf("closing a task in review: %s", location)
	}
	// A hand-made address does not offer to move a task that is not closed.
	store.SetStatus("T1002", task.Review, "テスト", "")
	for _, target := range []string{"/?undo=T1002", "/?undo=T9999", "/?undo="} {
		if _, home, _ := get(t, h, target); strings.Contains(home, "取り消す") {
			t.Errorf("%s offers to take back a task that is not closed", target)
		}
	}
}

func TestTaskPageOfAClosingTask(t *testing.T) {
	_, h := closingSetup(t)
	code, page, _ := get(t, h, "/tasks/T1003")
	if code != 200 {
		t.Fatalf("task page: %d", code)
	}
	for _, want := range []string{
		`<span class="status closing">完了確認待ち</span>`,
		"完了確認待ちになった日時 ",
		`<button name="status" value="done" class="primary">完了にする</button>`,
		`<button name="status" value="doing" >進行中へ戻す</button>`,
		`<button name="status" value="cancelled" >取りやめる</button>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page of a closing task lacks %q", want)
		}
	}
	if strings.Contains(page, "確認する方の名前") {
		t.Errorf("a closing task was asked to name the reviewer")
	}
	// The page of a task in review keeps its wording.
	_, asked, _ := get(t, h, "/tasks/T1002")
	if !strings.Contains(asked, "確認待ちになった日時 ") || !strings.Contains(asked, `<span class="status review">確認待ち</span>`) || strings.Contains(asked, "完了確認待ち") {
		t.Errorf("the page of a task in review changed")
	}
	// The list of all tasks can be narrowed to the new status.
	_, list, _ := get(t, h, "/tasks?status=closing")
	if !strings.Contains(list, `<option value="closing" selected>完了確認待ち</option>`) || !strings.Contains(list, "終わったタスク") || strings.Contains(list, "確認してほしいタスク") {
		t.Errorf("the list narrowed to closing:\n%s", list)
	}
}

func TestARequestReopensAClosingTask(t *testing.T) {
	store, _ := closingSetup(t)
	queue, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	h, err := (&Server{Store: store, Requests: queue}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(`{"id":"closing-fix","task":"T1003","text":"見出しの色を直してください。"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := store.Get("T1003")
	if got.Status != task.Doing {
		t.Fatalf("a request about a closing task left it %s", got.Status)
	}
	log, _ := readTestHistory(t, filepath.Join(store.Root(), got.Dir))
	if !strings.Contains(string(log), "状態を「完了確認待ち」から「進行中」に変えました。") {
		t.Errorf("log:\n%s", log)
	}

	// A request that only fixes the record keeps the status.
	store.SetStatus("T1003", task.Closing, "テスト", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(`{"id":"closing-keep","task":"T1003","text":"補足を保存してください。","status_policy":"keep"}`)))
	if got, _ := store.Get("T1003"); rec.Code != http.StatusCreated || got.Status != task.Closing {
		t.Errorf("a record-only request: %d, status %s", rec.Code, got.Status)
	}
}

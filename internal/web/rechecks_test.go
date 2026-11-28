package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/todo"
)

// soon is a time the store, which runs on the real clock, takes as a first
// round that has yet to come.
func soon(store *task.Store) (time.Time, string) {
	next := time.Now().In(store.Location()).Add(2 * time.Minute).Truncate(time.Minute)
	return next, next.Format("2006-01-02 15:04")
}

func TestARecheckIsRegisteredAskedAndRecordedThroughTheAPI(t *testing.T) {
	store, s, handler, _ := setupWaits(t)
	queue, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	s.Requests = queue
	const id = "T1001"
	first, when := soon(store)
	api := func(body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/tasks/"+id, strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := api(`{"recheck":{"every":"1d"}}`); code != http.StatusBadRequest || !strings.Contains(body, "--until") {
		t.Fatalf("a registration with no deadline: %d %s", code, body)
	}
	reply := as(t, handler, caller.Caller{}, "POST", "/api/tasks/"+id, `{"recheck":{"every":"1d","until":"2999-01-01","next":"`+when+`"}}`)
	if reply["changed"] != true {
		t.Fatalf("register: %v", reply)
	}
	got, _ := store.Get(id)
	if got.Status != task.Waiting || !got.Rechecks() || got.ResumeOn != when {
		t.Fatalf("registered: %+v", got)
	}
	label := "毎日 " + first.Format("15:04") + " に確認（2999-01-01 まで）"
	_, page, _ := get(t, handler, "/tasks/"+id)
	for _, want := range []string{label, got.RecheckState(), `action="/tasks/T1001/recheck"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the task page lacks %q", want)
		}
	}
	if strings.Contains(page, "。。") {
		t.Error("the recheck message has a doubled period")
	}
	if strings.Contains(page, "再開条件が未登録") || strings.Contains(page, task.WaitingConditionWarning) {
		t.Error("the page says the task waits with no condition")
	}
	if _, home, _ := get(t, handler, "/"); !strings.Contains(home, label) {
		t.Error("the home page lacks the rounds")
	}
	if list := todo.Render(store, time.Now()); !strings.Contains(list, label) || !strings.Contains(list, got.RecheckState()) {
		t.Errorf("TODO.md lacks the rounds:\n%s", list)
	}

	// Before the round, nothing is asked.
	if s.Recheck(first.Add(-time.Second)) || len(queue.Recent(0, id, true)) != 0 {
		t.Fatal("asked before the round")
	}
	if !s.Recheck(first) {
		t.Fatal("the round was not noticed")
	}
	asked := queue.Recent(0, id, true)
	if len(asked) != 1 {
		t.Fatalf("requests = %+v", asked)
	}
	look := asked[0]
	if look.Kind != request.System || !look.Side || look.TaskID != id || look.Status != request.Queued || look.StatusPolicy != "" || !strings.HasPrefix(look.ID, "recheck-T1001-") {
		t.Fatalf("the look is not a side request of the system: %+v", look)
	}
	prompt := request.Prompt(look)
	for _, want := range []string{"対象のタスク: T1001", "確かめる時刻（" + when + "）", "対象を読み取って結果を記録してください", "外部への送信、投稿、書き込み、設定の変更はしません",
		"lacia task rechecked T1001 same", "lacia task rechecked T1001 changed", "lacia task rechecked T1001 unknown", "lacia task field T1001 resume_on", "確認は毎日で、期限は 2999-01-01 です"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the look is not told %q:\n%s", want, prompt)
		}
	}
	got, _ = store.Get(id)
	following := first.AddDate(0, 0, 1).Format("2006-01-02 15:04")
	if got.Status != task.Waiting || got.Owner != "" || got.ResumeOn != following || got.RecheckAsked != when {
		t.Fatalf("the next round was not written before asking: %+v", got)
	}
	// One round is asked once, also after a restart between the two writes.
	if s.Recheck(first.Add(time.Minute)) || len(queue.Recent(0, id, true)) != 1 {
		t.Fatal("the same round was asked twice")
	}
	if !s.recheckPending(id, when) || s.recheckPending(id, following) {
		t.Fatal("the queue does not say which look is still to come")
	}

	reply = as(t, handler, caller.Caller{}, "POST", "/api/tasks/"+id, `{"rechecked":"same","text":"Issue に新しいコメントはありませんでした。"}`)
	got, _ = store.Get(id)
	if reply["changed"] != true || got.Status != task.Waiting || got.RecheckAsked != "" || got.RecheckLast == "" {
		t.Fatalf("same: %v %+v", reply, got)
	}
	if _, page, _ = get(t, handler, "/tasks/"+id); !strings.Contains(page, got.RecheckState()) || !strings.Contains(page, first.AddDate(0, 0, 1).Format("01/02 15:04")) {
		t.Error("the page does not show the last look")
	}
	if code, body := api(`{"rechecked":"maybe","text":"分かりません。"}`); code != http.StatusBadRequest || !strings.Contains(body, "same（変化なし）") {
		t.Fatalf("a result that does not exist: %d %s", code, body)
	}
	reply = as(t, handler, caller.Caller{}, "POST", "/api/tasks/"+id, `{"rechecked":"changed","text":"修正の依頼が届きました。"}`)
	got, _ = store.Get(id)
	if reply["changed"] != true || got.Status != task.Review || got.Rechecks() || got.ResumeOn != "" {
		t.Fatalf("changed: %v %+v", reply, got)
	}
	if log := logOf(t, store, id); !strings.Contains(log, "確認の結果は「変化あり」です。修正の依頼が届きました。") {
		t.Fatalf("history: %s", log)
	}
}

func TestARoundWithNobodyToAskGoesToReview(t *testing.T) {
	store, s, _, _ := setupWaits(t)
	const id = "T1001"
	first, when := soon(store)
	if _, _, err := store.SetRecheck(id, "1d", "2999-01-01", when, "test"); err != nil {
		t.Fatal(err)
	}
	// This server takes no requests.
	if !s.Recheck(first) {
		t.Fatal("the round was not noticed")
	}
	got, _ := store.Get(id)
	if got.Status != task.Review || got.Rechecks() || got.ResumeOn != "" {
		t.Fatalf("a round nobody could look at left the task waiting: %+v", got)
	}
	if log := logOf(t, store, id); !strings.Contains(log, "確認を別の会話に頼めませんでした") || !strings.Contains(log, "依頼を受け付けていません") {
		t.Fatalf("history: %s", log)
	}
}

func TestTheTaskPageRegistersARecheck(t *testing.T) {
	store, _, handler, _ := setupWaits(t)
	const id = "T1001"
	rec := post(handler, "/tasks/"+id+"/recheck", "", url.Values{"every": {"3h"}, "until": {"2999-01-01"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("register from the page: %d %s", rec.Code, rec.Body)
	}
	got, _ := store.Get(id)
	if got.Status != task.Waiting || got.RecheckEvery != "3h" || got.RecheckUntil != "2999-01-01" || got.ResumeOn == "" {
		t.Fatalf("registered from the page: %+v", got)
	}
	_, page, _ := get(t, handler, "/tasks/"+id)
	if !strings.Contains(page, `<option value="3h" selected>3時間ごと</option>`) || !strings.Contains(page, "3時間ごとに確認（2999-01-01 まで）") {
		t.Error("the page does not show what was registered")
	}
	if rec := post(handler, "/tasks/"+id+"/recheck", "", url.Values{"every": {"10m"}, "until": {"2999-01-01"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an interval nobody can read: %d", rec.Code)
	}
	// Leaving the wait from the page ends the rounds.
	if rec := post(handler, "/tasks/"+id+"/status", "", url.Values{"status": {"doing"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("leave: %d", rec.Code)
	}
	if got, _ := store.Get(id); got.Rechecks() || got.ResumeOn != "" {
		t.Fatalf("the rounds outlived the wait: %+v", got)
	}
}

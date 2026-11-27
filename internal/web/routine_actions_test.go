package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/notify"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func TestRoutineActionHomeShowsTasksOnly(t *testing.T) {
	s, h := setupAreaRoutine(t)
	normal, err := s.Store.Create(task.NewTask{Area: "work", Title: "通常の確認事項", Status: task.Review})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"S1011", "S1012"} {
		acceptRoutine(t, s, id, "点検")
	}
	if _, err := s.Store.WriteDoc("S1011", task.TaskFile, routineTaskBody, "test"); err != nil {
		t.Fatal(err)
	}
	jobs := s.routineJobs()
	jobs = append(jobs, schedule.Job{Name: "legacy", Title: "旧方式の予約", Err: "定義に問題があります"})
	s.Schedules = func() ([]schedule.Job, map[string]string) { return jobs, nil }
	enabled := false
	s.RoutineActionsEnabled = func() bool { return enabled }
	_, home, _ := get(t, h, "/")
	if !strings.Contains(home, "未確認の結果が1回分") || !strings.Contains(home, "旧方式の予約") {
		t.Fatalf("rollout disabled: %s", home)
	}
	_, oldTroubles, _ := get(t, h, "/api/schedule-troubles")
	if !strings.Contains(oldTroubles, "旧方式の予約") {
		t.Fatalf("legacy alerts: %s", oldTroubles)
	}

	enabled = true
	_, home, _ = get(t, h, "/")
	for _, absent := range []string{"/routines/S1011", "/routines/S1012", "未確認の結果", "旧方式の予約"} {
		if strings.Contains(home, absent) {
			t.Errorf("waiting task or record shows on the home page: %s", absent)
		}
	}
	if !strings.Contains(home, "/tasks/"+normal.ID) {
		t.Fatal("the normal review task is missing")
	}
	if code, body, _ := get(t, h, "/api/schedule-troubles"); code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("legacy alerts still expose troubles: %d %s", code, body)
	}
	if _, _, err := s.Store.SetStatus("S1011", task.Review, "test", ""); err != nil {
		t.Fatal(err)
	}
	_, home, _ = get(t, h, "/")
	if strings.Count(home, `class="id" href="/routines/S1011"`) != 1 ||
		!strings.Contains(home, `href="/routines/S1011#task"`) ||
		!strings.Contains(home, "認証をやり直していただきます") ||
		strings.Contains(home, "未確認の結果") ||
		strings.Contains(home, "/routines/S1011/receipts/1") {
		t.Fatalf("new review row: %s", home)
	}
	_, list, _ := get(t, h, "/routines")
	if !strings.Contains(list, `href="/routines/S1011#task">確認待ち`) || strings.Contains(list, "未確認") {
		t.Fatalf("routine list: %s", list)
	}
}

func TestRoutineActionReceiptsKeepLegacyLinksAndHistory(t *testing.T) {
	s, h := setupRoutine(t)
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, task.JST)
	s.Routines.Now = func() time.Time { return now }
	acceptRoutine(t, s, "S1001", "legacy")
	oldView := viewToken(t, h, "S1001")
	if _, err := s.Routines.Complete("S1001", routine.Completion{OperationID: "old", Actor: "以前の担当", Through: 1}); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{schedule.RunNothing, schedule.RunFailed, schedule.RunMissed, schedule.RunSkipped} {
		if _, err := s.Routines.Record("S1001", outcome, routine.Result{RunID: outcome, Summary: outcome + " summary"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	acceptRoutine(t, s, "S1001", "実行結果の区分が残っていない回")
	oldRun := schedule.Run{
		ID: "occurrence", Request: "legacy", At: time.Date(2026, 11, 30, 9, 0, 0, 0, task.JST),
		Outcome: schedule.RunPartial, Checks: []routine.Check{{Scope: "cluster", Status: "unverified", Reason: "通信エラー"}},
	}
	runPath := filepath.Join(s.Store.Root(), "routines", "S1001", "runs", "2026-11", "runs.jsonl")
	if err := os.MkdirAll(filepath.Dir(runPath), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(oldRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runPath, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	s.RoutineActions = true
	_, body, _ := get(t, h, "/routines/S1001")
	if !ascending(sectionOrder(t, body, `id="receipt-6"`, `id="receipt-5"`, `id="receipt-4"`, `id="receipt-3"`, `id="receipt-2"`, `id="receipt-1"`)) {
		t.Fatalf("receipts are not newest first: %s", body)
	}
	for _, want := range []string{
		"何もありませんでした", "失敗しました", "実行されませんでした", "停止中のため見送りました",
		"一部を確認できませんでした", `href="/routines/S1001?month=2026-11#` + oldRun.Anchor(),
		"結果の区分は記録されていません", "実行結果の区分が残っていない回",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("receipt list lacks %q", want)
		}
	}
	if strings.Contains(body, "未確認") || strings.Contains(body, "/complete") {
		t.Fatalf("legacy review controls remain: %s", body)
	}
	code, _, header := get(t, h, "/routines/github/receipts/1")
	if code != http.StatusPermanentRedirect || header.Get("Location") != "/routines/S1001/receipts/1" {
		t.Fatalf("old receipt URL: %d %v", code, header)
	}
	_, body, _ = get(t, h, "/routines/S1001/receipts/1")
	for _, want := range []string{"以前の担当", "過去の確認の履歴", "通信エラー", "この回の実行履歴を開く"} {
		if !strings.Contains(body, want) {
			t.Errorf("receipt detail lacks %q", want)
		}
	}
	if strings.Contains(body, "確認済みです") || strings.Contains(body, "/complete") {
		t.Fatalf("legacy review state remains: %s", body)
	}

	before, err := s.Routines.Snapshot("S1001")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/routines/S1001/complete":             "/routines/S1001?flash=",
		"/routines/github/receipts/2/complete": "/routines/S1001/receipts/2?flash=",
	} {
		rec := post(h, path, "", url.Values{"view": {oldView}})
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, want) {
			t.Fatalf("old form POST: %d %s", rec.Code, loc)
		}
		if code, body, _ := get(t, h, loc); code != 200 || !strings.Contains(body, "受付を確認済みにする操作は要りません") {
			t.Fatalf("old form destination: %d %s", code, body)
		}
	}
	after, err := s.Routines.Snapshot("S1001")
	if err != nil || len(after.History) != len(before.History) || len(after.Pending) != len(before.Pending) {
		t.Fatalf("old form POST changed receipt history: %+v, %v", after, err)
	}
}

func TestRoutineActionNoticeAcknowledgementIsSeparateFromReceipt(t *testing.T) {
	s, h := setupAreaRoutine(t)
	jobs := s.routineJobs()
	for i := range jobs {
		if jobs[i].ID == "S1012" {
			jobs[i].ReplyTo = "discord:test"
		}
	}
	s.Schedules = func() ([]schedule.Job, map[string]string) { return jobs, nil }
	s.RoutineActions = true
	for _, id := range []string{"S1011", "S1012"} {
		acceptRoutine(t, s, id, "receipt")
		if _, err := s.Store.MergeRoutineActions(id, task.RoutineObservation{
			At: time.Now(), Actions: []task.RoutineAction{{Key: "auth", Revision: "1", Text: "手元で認証を取り直してください。", Owner: true}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	notices := func() []notify.RoutineNotice {
		t.Helper()
		code, body, _ := get(t, h, "/api/routine-notices?action=1")
		var out []notify.RoutineNotice
		if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("notices: %d %s", code, body)
		}
		return out
	}
	if code, body, _ := get(t, h, "/api/routine-notices"); code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("legacy agent sees task notices: %d %s", code, body)
	}
	initial := notices()
	if len(initial) != 1 || !initial[0].Action || initial[0].ReceiptPage || initial[0].RoutineID != "S1011" ||
		initial[0].Summary != "手元で認証を取り直してください。" || initial[0].Sequence != 1 || initial[0].Count != 1 {
		t.Fatalf("new notices: %+v", initial)
	}
	_, home, _ := get(t, h, "/")
	if !strings.Contains(home, initial[0].Summary) {
		t.Fatal("managed routine review row lacks the current action")
	}
	const ack = "/api/routine-notices/S1011/1"
	if rec := post(h, ack, "", nil); rec.Code != http.StatusNoContent || !slices.Equal(notices(), initial) {
		t.Fatalf("legacy acknowledgement affected task notice: %d", rec.Code)
	}
	if rec := post(h, ack+"?action=1", "", nil); rec.Code != http.StatusNoContent || len(notices()) != 0 {
		t.Fatalf("task action acknowledgement: %d %s", rec.Code, rec.Body.String())
	}
	state, err := s.Routines.Snapshot("S1011")
	if err != nil || len(state.Receipts) != 1 || state.Receipts[0].Notified {
		t.Fatalf("acknowledgement modified receipt state: %+v %v", state, err)
	}
}

func TestRoutineActionRecordsWithoutTasks(t *testing.T) {
	s, _ := setupRoutine(t)
	root := s.Store.Root()
	if err := os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format":1,"areas":["work"],"tasks":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = store
	s.RoutineActions = true
	jobs := s.routineJobs()
	s.Schedules = func() ([]schedule.Job, map[string]string) { return jobs, nil }
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Routines.Record("S1001", schedule.RunFailed, routine.Result{
		RunID: "failure", Summary: "実行時に接続できませんでした。",
	}); err != nil {
		t.Fatal(err)
	}
	_, home, _ := get(t, h, "/")
	if strings.Contains(home, "要対応") || strings.Contains(home, "/routines/S1001") {
		t.Fatalf("receipt appears in action list without tasks: %s", home)
	}
	_, list, _ := get(t, h, "/routines")
	if !strings.Contains(list, `href="/routines/S1001"`) || strings.Contains(list, "<th>タスク</th>") {
		t.Fatalf("routine list without tasks: %s", list)
	}
	_, receipt, _ := get(t, h, "/routines/S1001/receipts/1")
	if !strings.Contains(receipt, "実行時に接続できませんでした。") || strings.Contains(receipt, "#task") {
		t.Fatalf("routine receipt without tasks: %s", receipt)
	}
	if code, body, _ := get(t, h, "/api/routine-notices?action=1"); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("task action notices without tasks: %d %s", code, body)
	}
}

func TestRunReportAcceptsOnlyValidResolvedKeys(t *testing.T) {
	if !validRunReport(schedule.Job{}, routine.Result{Resolved: []string{"issue:1", "issue:2"}}) {
		t.Fatal("report with explicit resolutions is invalid")
	}
	for _, keys := range [][]string{
		{" "}, {"issue:1", "issue:1"}, {strings.Repeat("a", 1025)}, make([]string, 501),
	} {
		if validRunReport(schedule.Job{}, routine.Result{Resolved: keys}) {
			t.Fatalf("accepted invalid resolution keys: %v", keys)
		}
	}
}

func TestRoutineMattersCollectedInAnotherRoutineShowThere(t *testing.T) {
	s, h := setupAreaRoutine(t)
	root := s.Store.Root()
	definition := "---\nid: S1011\ntitle: 夕方の確認です\ndaily: \"17:00\"\nmatters_task: S1012\n---\n\n## すること\n\n手順に従います。\n"
	if err := os.WriteFile(filepath.Join(root, "areas", "work", "routines", "S1011", "routine.md"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs := s.routineJobs()
	s.Schedules = func() ([]schedule.Job, map[string]string) { return jobs, nil }
	s.RoutineActions = true
	acceptRoutine(t, s, "S1011", "夕方の受付")
	if _, err := s.Store.MergeRoutineActions("S1012", task.RoutineObservation{At: time.Now(), Shared: true, Actions: []task.RoutineAction{{
		Key: "matter:mail/1", Revision: "a", Text: "校正の依頼に返事をしていただきます。", Reference: "/routines/S1011/receipts/1", Owner: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	_, home, _ := get(t, h, "/")
	if strings.Count(home, `class="id" href="/routines/S1012"`) != 1 || !strings.Contains(home, "校正の依頼に返事をしていただきます。") || strings.Contains(home, `class="id" href="/routines/S1011"`) {
		t.Fatalf("home: %s", home)
	}
	code, body, _ := get(t, h, "/api/routine-notices?action=1")
	var notices []notify.RoutineNotice
	if code != http.StatusOK || json.Unmarshal([]byte(body), &notices) != nil || len(notices) != 1 || notices[0].RoutineID != "S1012" || !notices[0].Action {
		t.Fatalf("notices: %d %s", code, body)
	}
	if _, err := s.Store.WriteDoc("S1011", "task.md", routineTaskBody, "test"); err != nil {
		t.Fatal(err)
	}
	const link = `<a href="/routines/S1012#task">S1012 の「タスク」欄</a>`
	for _, page := range []string{"/routines/S1011/receipts/1", "/routines/S1011"} {
		if _, text, _ := get(t, h, page); !strings.Contains(text, link) {
			t.Errorf("%s does not link to the collecting task: %s", page, text)
		}
	}
	if _, text, _ := get(t, h, "/routines/S1011/receipts/1"); strings.Contains(text, `href="/routines/S1011#task"`) {
		t.Error("the receipt also directs the reader to the source routine's task")
	}
	_, target, _ := get(t, h, "/routines/S1012")
	if strings.Contains(target, "に集めています") || !strings.Contains(target, `id="task"`) || !strings.Contains(target, `href="/routines/S1011/receipts/1"`) {
		t.Fatalf("the collecting routine: %s", target)
	}
	s.RoutineActions = false
	if _, text, _ := get(t, h, "/routines/S1011"); strings.Contains(text, "に集めています") {
		t.Fatal("the hint shows where matters are not collected")
	}
}

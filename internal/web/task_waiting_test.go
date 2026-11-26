package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

func TestWaitingGuidanceMatchesPageAndAPI(t *testing.T) {
	store, s, handler, _ := setupWaits(t)
	id := "T1001"
	path := "/tasks/" + id
	set := func(value string) map[string]any {
		t.Helper()
		return as(t, handler, caller.Caller{}, "POST", "/api"+path, `{"status":"`+value+`"}`)
	}
	check := func(warn bool) {
		t.Helper()
		code, body, _ := get(t, handler, path)
		if code != http.StatusOK || strings.Contains(body, task.WaitingConditionWarning) != warn {
			t.Fatalf("detail did not match expected warning %v (status %d)", warn, code)
		}
		if warn && !strings.Contains(body, `<details class="schedule" open>`) {
			t.Fatal("condition settings are hidden after entering waiting without a condition")
		}
		code, body, _ = get(t, handler, "/api/tasks/waiting-warnings")
		var warnings map[string]string
		if err := json.Unmarshal([]byte(body), &warnings); err != nil || code != http.StatusOK || (warnings[id] == task.WaitingConditionWarning) != warn {
			t.Fatalf("lint API did not match expected warning %v: %d %s", warn, code, body)
		}
	}
	rec := post(handler, path+"/status", "", url.Values{"status": {"waiting"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the Web save must succeed without a condition: %d", rec.Code)
	}
	saved, err := store.Get(id)
	if err != nil || saved.Status != task.Waiting || saved.Owner != "" {
		t.Fatalf("Web did not save ordinary waiting status: %+v %v", saved, err)
	}
	check(true)
	for _, condition := range []struct{ field, value string }{
		{"resume_on", "2026-11-20"}, {"after", "T1002"}, {"after_run", "S1001"},
	} {
		set("doing")
		if _, _, err := store.SetField(id, condition.field, condition.value, "test"); err != nil {
			t.Fatal(err)
		}
		if reply := set("waiting"); reply["changed"] != true || reply["waiting_warning"] != nil {
			t.Fatalf("waiting with %s: %v", condition.field, reply)
		}
		check(false)
		if _, _, err := store.SetField(id, condition.field, "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	watch := addWait(t, handler, macClaude, "")
	check(false)
	as(t, handler, caller.Caller{}, "POST", "/api/waits/"+watch, `{"seen":"running"}`)
	check(false)
	if _, _, err := s.Waits.Update(watch, func(w *wait.Wait) bool { w.State = wait.Cancelled; return true }); err != nil {
		t.Fatal(err)
	}
	check(true)
	handoverRecord, _, err := s.Handovers.Add(handover.Handover{TaskID: id, Next: "local-codex", Then: "結果を確かめます。"})
	if err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, _, err := s.Handovers.Update(handoverRecord.ID, func(h *handover.Handover) bool { h.State = handover.Acked; return true }); err != nil {
		t.Fatal(err)
	}
	check(true)
	queued, err := request.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Requests = queued
	item, _, err := queued.Add(request.Request{TaskID: id, Text: "続けてください。"})
	if err != nil {
		t.Fatal(err)
	}
	check(false)
	queued.Resolve(item.ID, request.Running, "", "", false)
	check(false)
	queued.Resolve(item.ID, request.Done, "終わりました。", "", false)
	check(true)
	set("doing")
	if reply := set("waiting"); reply["changed"] != true || reply["waiting_warning"] != task.WaitingConditionWarning {
		t.Fatalf("unregistered API save: %v", reply)
	}
	set("review")
	check(false)
}

func homeTaskSection(body, key string) string {
	_, rest, _ := strings.Cut(body, `<section class="list `+key+`">`)
	section, _, _ := strings.Cut(rest, "</section>")
	return section
}

func TestTaskListsShowWaitingConditionsAndWatchProgress(t *testing.T) {
	store, s, h, now := setupWaits(t)
	add := func(title string) task.Task {
		t.Helper()
		v, err := store.Create(task.NewTask{Area: "work", Title: title, Status: task.Waiting})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	field := func(v task.Task, key, value string) {
		t.Helper()
		if _, _, err := store.SetField(v.ID, key, value, ""); err != nil {
			t.Fatal(err)
		}
	}
	timed := add("日時を待つ作業")
	field(timed, "resume_on", "2026-11-19 05:00")
	field(timed, "on_ready", "start local-codex")
	dependent := add("別のタスクを待つ作業")
	field(dependent, "after", "T1002")
	multiple := add("複数のタスクを待つ作業")
	field(multiple, "after", "T1001、T1002")
	combined := add("日時と別のタスクを待つ作業")
	field(combined, "resume_on", "2026-11-19 05:00")
	field(combined, "review_at", "2026-11-19 06:00")
	field(combined, "after", "T1002")
	field(combined, "on_ready", "start")
	unregistered := add("条件が記録されていない作業")
	if _, _, err := store.SetField(unregistered.ID, "on_ready", "start", ""); err != nil {
		t.Fatal(err)
	}
	plain := add("外からの返事を待つ作業")
	if _, _, err := s.Handovers.Add(handover.Handover{
		TaskID: plain.ID, Next: "local-codex", Then: "続きに進みます。",
	}); err != nil {
		t.Fatal(err)
	}
	id := addWait(t, h, macClaude, `,"next":"codex"`)
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"pending"}`)
	field(task.Task{ID: "T1001"}, "review_at", "2026-11-19 04:00")
	field(task.Task{ID: "T1001"}, "on_ready", "start")
	code, body, _ := get(t, h, "/")
	if later := homeTaskSection(body, "later"); code != http.StatusOK || !strings.Contains(later, "親のタスク") ||
		!strings.Contains(later, "2026-11-19 05:00以降再開") ||
		!strings.Contains(later, `2026-11-19 06:00以降かつ<a href="/tasks/T1002">T1002</a>終了後再開`) ||
		!strings.Contains(later, `<a href="/tasks/T1001">T1001</a>、<a href="/tasks/T1002">T1002</a>終了後要対応`) ||
		!strings.Contains(later, `class="tag resume">再開条件が未登録</span>`) ||
		!strings.Contains(later, `href="/tasks/T1002">T1002</a>終了後要対応`) ||
		!strings.Contains(later, "local-codex への申し送り。宛先の会話に届くのを待っています。") ||
		!strings.Contains(later, "score-1。開始を待っています。確認の期限は 2026-11-19 06:00") {
		t.Fatalf("waiting tasks are missing their conditions: %s", later)
	}
	if strings.Contains(homeTaskSection(body, "ongoing"), "親のタスク") {
		t.Fatal("the pending job appeared as ongoing")
	}
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"running"}`)
	_, body, _ = get(t, h, "/")
	if ongoing := homeTaskSection(body, "ongoing"); !strings.Contains(ongoing, "親のタスク") ||
		!strings.Contains(ongoing, "score-1。実行中です") ||
		!strings.Contains(ongoing, "次の確認日時 2026-11-19 04:00以降再開") {
		t.Fatalf("running job or next check missing: %s", ongoing)
	}
	_, list, _ := get(t, h, "/tasks?status=waiting")
	if !strings.Contains(list, "日時を待つ作業") || strings.Contains(list, "親のタスク") ||
		!strings.Contains(list, "2026-11-19 05:00以降再開") {
		t.Fatalf("waiting filter differs from the home page: %s", list)
	}
	got, err := store.Get("T1001")
	if err != nil || got.Status != task.Waiting || got.ReviewAt != "2026-11-19 04:00" || got.OnReady != "start" {
		t.Fatalf("list/observation changed the saved status or schedule: %+v, %v", got, err)
	}
	woken := store.Wake(now.Add(4 * time.Hour))
	for _, v := range woken {
		if v.ID == timed.ID && v.Status == task.Waiting && v.ReadyTarget() == "local-codex" {
			return
		}
	}
	t.Fatalf("the scheduled task did not wake with its original destination: %+v", woken)
}

func TestHomeShowsAQueuedTaskButKeepsWorkAlreadyInProgress(t *testing.T) {
	store, _ := setup(t)
	queue, err := request.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addRequest := func(id, policy string) {
		t.Helper()
		if _, _, err := queue.Add(request.Request{TaskID: id, StatusPolicy: policy, Text: "確認してください。"}); err != nil {
			t.Fatal(err)
		}
	}
	addRequest("T1001", "")
	addRequest("T1001", "")
	keep, err := store.Create(task.NewTask{Area: "work", Title: "記録だけを頼んだ作業", Status: task.Doing})
	if err != nil {
		t.Fatal(err)
	}
	addRequest(keep.ID, request.KeepStatus)
	owned, err := store.Create(task.NewTask{Area: "work", Title: "会話が担当する作業", Status: task.Doing})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Claim(owned.ID, task.Owner{Label: "local-codex", Session: "session"}, "", ""); err != nil {
		t.Fatal(err)
	}
	addRequest(owned.ID, "")
	h, err := (&Server{Store: store, Requests: queue}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := get(t, h, "/")
	if later := homeTaskSection(body, "later"); !strings.Contains(later, "親のタスク") ||
		strings.Count(later, "会話の順番待ち") != 1 || strings.Contains(later, keep.Title) || strings.Contains(later, owned.Title) {
		t.Fatalf("wrong jobs in the waiting list: %s", later)
	}

	ctx, cancel := context.WithCancel(context.Background())
	started, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		queue.Work(ctx, false, func(ctx context.Context, _ string) (string, string, error) {
			close(started)
			<-ctx.Done()
			return "", "", ctx.Err()
		}, func() bool { return ctx.Err() != nil })
	}()
	defer func() { cancel(); <-stopped }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the request did not start")
	}
	_, body, _ = get(t, h, "/")
	if ongoing := homeTaskSection(body, "ongoing"); !strings.Contains(ongoing, "親のタスク") ||
		!strings.Contains(ongoing, keep.Title) || !strings.Contains(ongoing, owned.Title) {
		t.Fatalf("work already running or assigned is missing from the ongoing list: %s", ongoing)
	}
	if later := homeTaskSection(body, "later"); strings.Contains(later, "親のタスク") {
		t.Fatalf("a second queued request hid the running request: %s", later)
	}
}

func TestTaskListSuppliesStructuredConditionAndKeepsTaskBody(t *testing.T) {
	store, s, h, now := setupWaits(t)
	newTask := func(title string, status task.Status) task.Task {
		t.Helper()
		item, err := store.Create(task.NewTask{Area: "work", Title: title, Status: status,
			Body: task.Body(store.Format(), "概要です。", "PRの結果を本文に書きます。")})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	addHandover := func(item task.Task, state string) handover.Handover {
		t.Helper()
		h, _, err := s.Handovers.Add(handover.Handover{TaskID: item.ID, Next: "local-codex", Then: "後で確認します。"})
		if err != nil {
			t.Fatal(err)
		}
		h, _, err = s.Handovers.Update(h.ID, func(v *handover.Handover) bool {
			v.State = state
			v.Told = *now
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	plain := newTask("本文しかない待機", task.Waiting)
	late := newTask("受領が遅れたタスク", task.Waiting)
	addHandover(late, handover.ToldDelivered)
	expired := newTask("期限を過ぎたタスク", task.Waiting)
	addHandover(expired, handover.Skipped)

	for _, example := range []struct {
		id, want string
	}{
		{plain.ID, `class="tag resume">再開条件が未登録</span>`},
		{late.ID, "受け取りが遅れています。再割り当てが必要です。"},
		{expired.ID, "期限が過ぎました。再割り当てが必要です。"},
	} {
		_, detail, _ := get(t, h, "/tasks/"+example.id)
		if !strings.Contains(detail, example.want) || !strings.Contains(detail, "<p>PRの結果を本文に書きます。</p>") {
			t.Errorf("detail for %s does not show %q and its body", example.id, example.want)
		}
	}
	_, list, _ := get(t, h, "/tasks?status=waiting")
	if !strings.Contains(list, `class="next">PRの結果を本文に書きます。</p>`) ||
		!strings.Contains(list, `class="tag resume">再開条件が未登録</span>`) {
		t.Fatal("list did not preserve separate condition and suggested work")
	}
}

func TestTaskListRequestsFinishedAlert(t *testing.T) {
	store, _ := setup(t)
	queue, err := request.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	add := func(title, owner string) task.Task {
		t.Helper()
		item, err := store.Create(task.NewTask{Area: "work", Title: title, Status: task.Doing})
		if err != nil {
			t.Fatal(err)
		}
		if owner != "" {
			if _, _, err := store.Claim(item.ID, task.Owner{Label: owner, Session: "session"}, "", ""); err != nil {
				t.Fatal(err)
			}
		}
		return item
	}
	save := func(item task.Task, policy, status string) {
		t.Helper()
		r, _, err := queue.Add(request.Request{TaskID: item.ID, StatusPolicy: policy, Text: "確認してください。"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := queue.Resolve(r.ID, status, "", "", false); !ok {
			t.Fatal("could not save request status")
		}
	}
	finished := add("受付用タスク", "")
	save(finished, "", request.Done)
	running := add("次の依頼が実行中", "")
	save(running, "", request.Done)
	save(running, "", request.Running)
	queued := add("次の依頼が順番待ち", "")
	save(queued, "", request.Done)
	save(queued, "", request.Queued)
	owned := add("担当者が付いたタスク", "local-codex")
	save(owned, "", request.Done)
	keep := add("記録だけの依頼", "")
	save(keep, request.KeepStatus, request.Done)
	h, err := (&Server{Store: store, Requests: queue}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []task.Task{finished, running, queued, owned, keep} {
		_, detail, _ := get(t, h, "/tasks/"+item.ID)
		got := strings.Contains(detail, "受け付けた依頼は終了しました。タスクの状態確認が必要です。")
		if got != (item.ID == finished.ID) {
			t.Errorf("wrong alert for %s: %t", item.Title, got)
		}
	}
	if got, err := store.Get(finished.ID); err != nil || got.Status != task.Doing {
		t.Fatal("displaying the alert changed the task status")
	}
}

package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

func setupWaits(t *testing.T) (*task.Store, *Server, http.Handler, *time.Time) {
	t.Helper()
	store, _ := setup(t)
	waits, err := wait.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, task.JST)
	waits.Now = func() time.Time { return now }
	handovers, err := handover.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handovers.Now = func() time.Time { return now }
	s := &Server{Store: store, Version: "test", Waits: waits, Handovers: handovers, Now: func() time.Time { return now }}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return store, s, h, &now
}

func logOf(t *testing.T, store *task.Store, id string) string {
	t.Helper()
	got, _ := store.Get(id)
	data, _ := readTestHistory(t, filepath.Join(store.Root(), got.Dir))
	return string(data)
}

func status(h http.Handler, who caller.Caller, target, body string) int {
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	if who.Place != "" {
		req.Header.Set(caller.Header, who.Encode())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

var (
	macClaude = caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}
	macCodex  = caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Codex, PID: 30, Session: "thread-9"}}}
)

func addWait(t *testing.T, h http.Handler, who caller.Caller, extra string) string {
	t.Helper()
	out := as(t, h, who, "POST", "/api/waits", `{"task":"T1001","kind":"job","context":"cluster-a","namespace":"ns","name":"score-1","by":"5h","then":"結果を回収して、表を作ります。"`+extra+`}`)
	item, _ := out["wait"].(map[string]any)
	id, _ := item["id"].(string)
	if id == "" || out["added"] != true {
		t.Fatalf("add = %v", out)
	}
	return id
}

func TestAWaitSetsTheTaskAsideAndTheContinuationTakesItBack(t *testing.T) {
	store, s, h, now := setupWaits(t)
	// Claude registers, and leaves the next step to Codex.
	id := addWait(t, h, macClaude, `,"next":"codex"`)
	got, _ := store.Get("T1001")
	if got.Status != task.Waiting {
		t.Fatalf("status after add = %s", got.Status)
	}
	if log := logOf(t, store, "T1001"); !strings.Contains(log, "待ち "+id+" を登録しました") || !strings.Contains(log, "期限は 2026-11-19 06:00") || !strings.Contains(log, "Codex の側のレイシアに続きを頼みます") {
		t.Fatalf("log = %s", log)
	}
	// The task's page shows it.
	if _, body, _ := get(t, h, "/tasks/T1001"); !strings.Contains(body, "いま待っているもの") || !strings.Contains(body, "score-1") {
		t.Fatal("the page does not show the wait")
	}

	// The watcher's reports: still running, then done.
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"running"}`)
	if code := status(h, caller.Caller{}, "/api/waits/"+id, `{"start":"mac"}`); code != http.StatusConflict {
		t.Fatalf("start before the end = %d", code)
	}
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"succeeded","detail":"output/table.csv"}`)
	if got, _ = store.Get("T1001"); got.Status != task.Waiting {
		t.Fatalf("status right after the end = %s; the continuation has its grace period", got.Status)
	}
	item, _ := s.Waits.Get(id)
	message, err := s.Handovers.Get(item.HandoverID)
	want := "待っていたのは cluster-a の ns にある Job score-1 です。正常に終わりました。\noutput/table.csv\n\n結果を回収して、表を作ります。"
	if err != nil || !item.Closed() || message.Then != want {
		t.Fatalf("wait = %+v, handover = %+v, error = %v", item, message, err)
	}

	// Only one runner gets to start the continuation.
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"start":"mac"}`)
	if code := status(h, caller.Caller{}, "/api/waits/"+id, `{"start":"mac"}`); code != http.StatusConflict {
		t.Fatalf("second start = %d", code)
	}
	// The new conversation says it has it, and owns the task from then on.
	as(t, h, macCodex, "POST", "/api/waits/"+id, `{"ack":true}`)
	if got, _ = store.Get("T1001"); got.Status != task.Doing || got.Owner != "local-codex" {
		t.Fatalf("after ack = %s, owner %q", got.Status, got.Owner)
	}
	// A wait that is over leaves the top of the page; the history has it.
	if _, body, _ := get(t, h, "/tasks/T1001"); strings.Contains(body, "いま待っているもの") {
		t.Fatal("the page still shows the wait that is over")
	}
	if _, body, _ := get(t, h, "/tasks/T1001/log"); !strings.Contains(body, "待ち "+id+" を登録しました") {
		t.Fatal("the full history lost the wait")
	}
	// Nothing more happens, however long it takes.
	*now = now.Add(3 * time.Hour)
	s.TickWaits()
	if got, _ = store.Get("T1001"); got.Status != task.Doing {
		t.Fatalf("after the tick = %s", got.Status)
	}
	if _, body, _ := get(t, h, "/api/waits"); strings.TrimSpace(body) != "[]" {
		t.Fatalf("still listed: %s", body)
	}
	if _, body, _ := get(t, h, "/api/waits?all=1&task=T1001"); !strings.Contains(body, id) {
		t.Fatalf("not in the full list: %s", body)
	}
}

func TestWaitWithoutContinuationAndDelayedHandoverHaveDifferentStatuses(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		drive func(t *testing.T, h http.Handler, id string, now *time.Time)
		want  string
		state task.Status
	}{
		{"the deadline passes", `,"next":"codex"`, func(t *testing.T, h http.Handler, id string, now *time.Time) {
			*now = now.Add(5*time.Hour + time.Minute)
		}, "までに、終了を確かめられませんでした", task.Review},
		{"the job disappears", `,"next":"codex"`, func(t *testing.T, h http.Handler, id string, now *time.Time) {
			as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"gone"}`)
		}, "対象が見つからなくなりました", task.Review},
		{"nobody was to continue", `,"next":"none"`, func(t *testing.T, h http.Handler, id string, now *time.Time) {
			as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"failed","detail":"BackoffLimitExceeded"}`)
		}, "処理に失敗しました。\nBackoffLimitExceeded\n終わったらすること\n結果を回収して、表を作ります。", task.Review},
		{"the local device never starts the continuation", `,"next":"claude"`, func(t *testing.T, h http.Handler, id string, now *time.Time) {
			as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"succeeded"}`)
			*now = now.Add(handover.Grace + time.Minute)
		}, "宛先の会話へまだ届けられていません", task.Waiting},
		{"the continuation dies without a word", `,"next":"claude"`, func(t *testing.T, h http.Handler, id string, now *time.Time) {
			as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"succeeded"}`)
			as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"start":"mac"}`)
			as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"end":1,"tail":"command not found"}`)
		}, "受け取りを知らせないまま終わりました", task.Waiting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, s, h, now := setupWaits(t)
			id := addWait(t, h, macClaude, c.extra)
			c.drive(t, h, id, now)
			s.TickWaits()
			got, _ := store.Get("T1001")
			if got.Status != c.state || (got.ReadySince != "") != (c.state == task.Review) {
				t.Fatalf("status = %s, ready_since = %q", got.Status, got.ReadySince)
			}
			if log := logOf(t, store, "T1001"); !strings.Contains(log, c.want) {
				t.Fatalf("log = %s", log)
			}
			item, _ := s.Waits.Get(id)
			if item.HandoverID != "" {
				message, err := s.Handovers.Get(item.HandoverID)
				if err != nil || !strings.Contains(message.Then, "結果を回収して、表を作ります。") {
					t.Fatalf("handover = %+v, error = %v", message, err)
				}
			} else if log := logOf(t, store, "T1001"); !strings.Contains(log, "結果を回収して、表を作ります。") {
				t.Fatalf("log = %s", log)
			}
			// Each issue is recorded only once.
			before := logOf(t, store, "T1001")
			*now = now.Add(time.Hour)
			s.TickWaits()
			if logOf(t, store, "T1001") != before {
				t.Fatal("the issue was recorded twice")
			}
		})
	}
}

func TestAnOfflineDeviceStillContinuesUnlessTheTaskMovedOn(t *testing.T) {
	store, s, h, now := setupWaits(t)
	id := addWait(t, h, macCodex, "") // next defaults to the registering side
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"succeeded"}`)
	*now = now.Add(2 * time.Hour)
	s.TickWaits()
	if got, _ := store.Get("T1001"); got.Status != task.Waiting {
		t.Fatalf("status = %s", got.Status)
	}
	// The device reconnects: the wait is still offered, and may start.
	listed := s.Handovers.List(func(item handover.Handover) bool { return !item.Closed() })
	if len(listed) != 1 || !listed[0].Runnable() || listed[0].Next != wait.NextCodex {
		t.Fatalf("listed = %+v", listed)
	}
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"start":"mac"}`)
	as(t, h, macCodex, "POST", "/api/waits/"+id, `{"ack":true}`)
	if got, _ := store.Get("T1001"); got.Status != task.Doing {
		t.Fatalf("after the late start = %s", got.Status)
	}

	// While somebody else is working on the task, the continuation waits
	// its turn, and the log says how to take the result over.
	store, s, h, now = setupWaits(t)
	id = addWait(t, h, macCodex, "")
	as(t, h, macClaude, "POST", "/api/tasks/T1001", `{"status":"doing"}`)
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"succeeded"}`)
	if code := status(h, caller.Caller{}, "/api/waits/"+id, `{"start":"mac"}`); code != http.StatusConflict {
		t.Fatalf("start while doing = %d", code)
	}
	*now = now.Add(2 * time.Hour)
	s.TickWaits()
	if got, _ := store.Get("T1001"); got.Status != task.Doing || got.Owner != "local-claude" {
		t.Fatalf("the task was taken away: %s, %q", got.Status, got.Owner)
	}
	if log := logOf(t, store, "T1001"); !strings.Contains(log, "lacia wait ack H"+strings.TrimPrefix(id, "W")) {
		t.Fatalf("log = %s", log)
	}
	if item, _ := s.Handovers.Get("H" + strings.TrimPrefix(id, "W")); item.Closed() || !item.Runnable() {
		t.Fatalf("wait = %+v", item)
	}
	// Its turn comes when the task is set down.
	as(t, h, macClaude, "POST", "/api/tasks/T1001", `{"status":"waiting"}`)
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"start":"mac"}`)

	// A closed task is left alone for good.
	store, s, h, _ = setupWaits(t)
	id = addWait(t, h, macCodex, "")
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+id, `{"seen":"succeeded"}`)
	as(t, h, macClaude, "POST", "/api/tasks/T1001", `{"status":"done"}`)
	if code := status(h, caller.Caller{}, "/api/waits/"+id, `{"start":"mac"}`); code != http.StatusConflict {
		t.Fatalf("start on a closed task = %d", code)
	}
	if item, _ := s.Handovers.Get("H" + strings.TrimPrefix(id, "W")); !item.Closed() || item.Skipped == "" {
		t.Fatalf("wait = %+v", item)
	}
	_ = store
}

func TestWaitsAreRefusedForWhatCannotBeWatched(t *testing.T) {
	_, _, h, _ := setupWaits(t)
	for _, body := range []string{
		`{"task":"T9999","kind":"job","context":"cluster-a","namespace":"ns","name":"a","by":"5h","next":"none"}`,
		`{"task":"T1001","kind":"job","context":"cluster-a","namespace":"ns","name":"a","next":"none"}`,
		`{"task":"T1001","kind":"job","context":"cluster-a","namespace":"ns","name":"a","by":"5h","next":"codex"}`,
		`{"task":"T1001","kind":"job","context":"cluster-a","namespace":"ns","name":"a b","by":"5h","next":"none"}`,
		`{"task":"T1001","kind":"job","context":"cluster-a","namespace":"ns","name":"a","by":"5h","next":"owner"}`,
	} {
		if code := status(h, caller.Caller{}, "/api/waits", body); code < 400 {
			t.Errorf("accepted (%d): %s", code, body)
		}
	}
}

func TestARequestToTheOtherSideIsDeliveredAndTakenOnce(t *testing.T) {
	store, s, h, now := setupWaits(t)
	// Claude is working on the task and asks Codex to take it over.
	as(t, h, macClaude, "POST", "/api/tasks/T1001", `{"status":"doing"}`)
	out := as(t, h, macClaude, "POST", "/api/handovers", `{"task":"T1001","then":"コピーを復旧してください。手順は local/T1001/brief.md にあります。"}`)
	item, _ := out["handover"].(map[string]any)
	id, _ := item["id"].(string)
	if item["next"] != "codex" || item["state"] != "queued" || id == "" {
		t.Fatalf("tell = %v", out)
	}
	// Asking does not change whose turn it is.
	if got, _ := store.Get("T1001"); got.Status != task.Doing || got.Owner != "local-claude" {
		t.Fatalf("after tell = %s, %q", got.Status, got.Owner)
	}
	if log := logOf(t, store, "T1001"); !strings.Contains(log, "local-claude から Codex の側のレイシア への申し送り "+id+" を預かりました") {
		t.Fatalf("log = %s", log)
	}
	// It can be delivered although the sender is still at the task.
	as(t, h, caller.Caller{}, "POST", "/api/handovers/"+id, `{"start":"mac"}`)
	as(t, h, caller.Caller{}, "POST", "/api/handovers/"+id, `{"via":"mac で動いている Codex の会話 local-codex（個人用）へ届けました"}`)
	if log := logOf(t, store, "T1001"); !strings.Contains(log, "申し送り "+id+" の届け方を記録しました。\nmac で動いている Codex の会話 local-codex（個人用）へ届けました") {
		t.Fatalf("log = %s", log)
	}
	// The receiver takes the task; a second conversation is told to stop.
	as(t, h, macCodex, "POST", "/api/handovers/"+id, `{"ack":true}`)
	if got, _ := store.Get("T1001"); got.Status != task.Doing || got.Owner != "local-codex" {
		t.Fatalf("after ack = %s, %q", got.Status, got.Owner)
	}
	as(t, h, macCodex, "POST", "/api/handovers/"+id, `{"ack":true}`) // saying it twice is fine
	other := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Codex, PID: 31, Session: "thread-10"}}}
	req := httptest.NewRequest("POST", "/api/handovers/"+id, strings.NewReader(`{"ack":true}`))
	req.Header.Set(caller.Header, other.Encode())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "すでに local-codex が受け取りました") {
		t.Fatalf("second ack = %d %s", rec.Code, rec.Body)
	}

	// A delayed request leaves its details in the waiting task’s history.
	store, s, h, now = setupWaits(t)
	waitWithoutCondition(t, store, "T1001")
	out = as(t, h, macCodex, "POST", "/api/handovers", `{"task":"T1001","then":"図を確かめてください。"}`)
	item, _ = out["handover"].(map[string]any)
	id, _ = item["id"].(string)
	if item["next"] != "claude" {
		t.Fatalf("from codex, next = %v", item["next"])
	}
	as(t, h, caller.Caller{}, "POST", "/api/handovers/"+id, `{"start":"mac"}`)
	as(t, h, caller.Caller{}, "POST", "/api/handovers/"+id, `{"via":"MacでClaudeの会話を新しく始めました。"}`)
	*now = now.Add(handover.Grace + time.Minute)
	s.TickWaits()
	got, _ := store.Get("T1001")
	if log := logOf(t, store, "T1001"); got.Status != task.Waiting || !strings.Contains(log, "受け取りの知らせが、まだ届いていません。\n届け方の記録\nMacでClaudeの会話を新しく始めました。\n申し送りの内容\n図を確かめてください。") {
		t.Fatalf("status = %s, log = %s", got.Status, log)
	}
	// A request needs somewhere to go and something to say.
	for _, body := range []string{`{"task":"T1001","then":"宛先がありません。"}`, `{"task":"T1001","next":"codex","then":" "}`, `{"task":"T1001","next":"none","then":"x"}`} {
		if code := status(h, caller.Caller{}, "/api/handovers", body); code < 400 {
			t.Errorf("accepted (%d): %s", code, body)
		}
	}
}

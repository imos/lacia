package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/task"
)

// as sends a JSON request the way the lacia command does from inside the
// given conversation.
func as(t *testing.T, h http.Handler, who caller.Caller, method, target, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if who.Place != "" {
		req.Header.Set(caller.Header, who.Encode())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		t.Fatalf("%s %s: %d %s", method, target, rec.Code, rec.Body)
	}
	out := map[string]any{}
	if strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestTheConversationThatStartsATaskOwnsIt(t *testing.T) {
	store, h := setup(t)
	codex := caller.Caller{Place: "work", Host: "example-pod", Agents: []caller.Agent{{Kind: caller.Codex, PID: 10, Session: "thread-1"}}}
	// Claude Code, started by a Codex on the local device.
	claude := caller.Caller{Place: "local", Host: "mac", Dir: "/Users/u/git/lacia", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}, {Kind: caller.Codex, PID: 5, Session: "thread-2"}}}

	as(t, h, codex, "POST", "/api/tasks/T1002", `{"status":"doing"}`)
	got, _ := store.Get("T1002")
	if got.Status != task.Doing || got.Owner != "work-codex" || got.OwnerSession != codex.Key() {
		t.Fatalf("after start = %+v", got)
	}

	// Another conversation writing to it is told who is working, and
	// setting "doing" again does not take the task away.
	out := as(t, h, claude, "POST", "/api/tasks/T1002", `{"log":"横から書きました。"}`)
	if warning, _ := out["warning"].(string); !strings.Contains(warning, "いま work-codex が進めています") {
		t.Fatalf("warning = %q", warning)
	}
	out = as(t, h, claude, "POST", "/api/tasks/T1002", `{"status":"doing"}`)
	if got, _ = store.Get("T1002"); out["changed"] != false || out["warning"] == "" || got.Owner != "work-codex" {
		t.Fatalf("out = %v, owner = %q", out, got.Owner)
	}
	if out = as(t, h, codex, "POST", "/api/tasks/T1002", `{"log":"自分で書きました。"}`); out["warning"] != "" {
		t.Fatalf("the owner was warned: %v", out)
	}

	// Claiming takes it over, and the log names both sides.
	out = as(t, h, claude, "POST", "/api/tasks/T1002", `{"claim":true}`)
	if got, _ = store.Get("T1002"); out["changed"] != true || got.Owner != "local-claude" {
		t.Fatalf("out = %v, owner = %q", out, got.Owner)
	}
	log, _ := readTestHistory(t, filepath.Join(store.Root(), got.Dir))
	for _, want := range []string{"担当は work-codex です。（work-codex）", "横から書きました。（local-claude、local-codex から）", "担当を work-codex から local-claude に替えました。（local-claude、local-codex から）"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}

	// The pages and the list of conversations show the owner.
	if _, body, _ := get(t, h, "/tasks/T1002"); !strings.Contains(body, "担当 local-claude") || !strings.Contains(body, "local-codex から呼ばれています") {
		t.Error("the task page lacks the owner")
	}
	if _, body, _ := get(t, h, "/tasks?status=doing"); !strings.Contains(body, `<span class="task-owner">担当 local-claude</span>`) {
		t.Error("the list lacks the owner")
	}
	code, body, _ := get(t, h, "/who")
	if code != 200 || !strings.Contains(body, "work-codex") || !strings.Contains(body, `<a href="/tasks/T1002">T1002</a>`) {
		t.Errorf("who page (%d) lacks the conversations", code)
	}
	_, body, _ = get(t, h, "/api/sessions")
	var rows []SessionRow
	if err := json.Unmarshal([]byte(body), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("sessions = %s (%v)", body, err)
	}

	// A new task created as "doing" belongs to its creator; one that leaves
	// "doing" belongs to nobody.
	created := as(t, h, claude, "POST", "/api/tasks", `{"area":"work","title":"新しい仕事","status":"doing"}`)
	if created["Owner"] != "local-claude" {
		t.Fatalf("created = %v", created)
	}
	as(t, h, claude, "POST", "/api/tasks/T1002", `{"status":"review"}`)
	if got, _ = store.Get("T1002"); got.Owner != "" {
		t.Fatalf("owner after review = %q", got.Owner)
	}

	// Without an agent, the command works as before and owns nothing.
	as(t, h, caller.Caller{}, "POST", "/api/tasks/T1002", `{"status":"doing"}`)
	if got, _ = store.Get("T1002"); got.Status != task.Doing || got.Owner != "" {
		t.Fatalf("plain command = %+v", got)
	}
	if log, _ = readTestHistory(t, filepath.Join(store.Root(), got.Dir)); !strings.Contains(string(log), "「進行中」に変えました。（コマンド）") {
		t.Errorf("log lacks the plain actor:\n%s", log)
	}
}

// Before a restart, the server says who would be cut off: a conversation
// that ran a command a moment ago. Asking does not make the asker one.
func TestBusyListsTheConversationsHeardJustNow(t *testing.T) {
	_, h := setup(t)
	if out := as(t, h, caller.Caller{}, "GET", "/api/busy", ""); len(out["sessions"].([]any)) != 0 || len(out["requests"].([]any)) != 0 {
		t.Fatalf("a quiet server = %v", out)
	}
	codex := caller.Caller{Place: "work", Host: "example-pod", Agents: []caller.Agent{{Kind: caller.Codex, PID: 10, Session: "thread-1"}}}
	as(t, h, codex, "POST", "/api/tasks/T1002", `{"log":"作業の記録です。"}`)
	other := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}
	out := as(t, h, other, "GET", "/api/busy?within=5m", "")
	sessions := out["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["label"] != "work-codex" {
		t.Fatalf("after a command = %v", out)
	}
	if out = as(t, h, other, "GET", "/api/busy?within=1ns", ""); len(out["sessions"].([]any)) != 0 {
		t.Fatalf("outside the window = %v", out)
	}
}

// A restart for when the work is done is kept by the server itself. One
// that has nowhere to keep it says so.
func TestRestartRequestIsKeptByTheServer(t *testing.T) {
	_, h := setup(t)
	req := httptest.NewRequest("POST", "/api/restart", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "予約できません") {
		t.Fatalf("without a state directory = %d %s", rec.Code, rec.Body)
	}
}

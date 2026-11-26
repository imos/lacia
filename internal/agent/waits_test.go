package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/imos/lacia/internal/testrecords"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
	"github.com/imos/lacia/internal/web"
)

// kubectl answers for a fake cluster.
type kubectl struct {
	out, errOut string
	code        int
	err         error
	calls       [][]string
}

func (k *kubectl) exec(_ context.Context, _ string, args ...string) (string, string, int, error) {
	k.calls = append(k.calls, args)
	return k.out, k.errOut, k.code, k.err
}

func TestLookReadsTheStateOfAJob(t *testing.T) {
	job := wait.Wait{Kind: wait.Job, Context: "cluster-a", Namespace: "ns", Name: "score-1"}
	cases := []struct {
		name string
		k    kubectl
		seen string
		in   string // expected in the detail
	}{
		{"complete", kubectl{out: `{"status":{"succeeded":1,"conditions":[{"type":"SuccessCriteriaMet","status":"True"},{"type":"Complete","status":"True"}]}}`}, wait.Succeeded, ""},
		{"failed", kubectl{out: `{"status":{"failed":3,"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"Job has reached the specified backoff limit"}]}}`}, wait.Failed, "BackoffLimitExceeded"},
		{"running", kubectl{out: `{"status":{"active":1}}`}, wait.Running, ""},
		{"retrying", kubectl{out: `{"status":{"active":1,"failed":2}}`}, wait.Running, "失敗した Pod は2個"},
		{"held by the queue", kubectl{out: `{"spec":{"suspend":true},"status":{"conditions":[{"type":"Suspended","status":"True"}]}}`}, wait.Pending, "Suspended"},
		{"just created", kubectl{out: `{"status":{}}`}, wait.Pending, ""},
		{"deleted", kubectl{code: 1, errOut: `Error from server (NotFound): jobs.batch "score-1" not found`}, wait.Gone, ""},
		{"credentials expired", kubectl{code: 1, errOut: "error: You must be logged in to the server (Unauthorized)\nmore"}, wait.Unseen, "Unauthorized"},
		{"kubectl missing", kubectl{err: errors.New("exec: no such file")}, wait.Unseen, "no such file"},
		{"garbage", kubectl{out: "<html>"}, wait.Unseen, ""},
	}
	for _, c := range cases {
		w := &Watcher{Kubectl: "kubectl", Exec: c.k.exec}
		seen, detail := w.Look(context.Background(), job)
		if seen != c.seen || !strings.Contains(detail, c.in) {
			t.Errorf("%s: seen %q, detail %q", c.name, seen, detail)
		}
	}
	k := kubectl{out: `{"status":{}}`}
	(&Watcher{Kubectl: "kubectl", Exec: k.exec}).Look(context.Background(), job)
	if got := strings.Join(k.calls[0], " "); got != "--context cluster-a -n ns --request-timeout=20s get job score-1 -o json" {
		t.Fatalf("kubectl was called with: %s", got)
	}
}

func TestLookReadsTheFileThatMarksTheEnd(t *testing.T) {
	file := wait.Wait{Kind: wait.File, Context: "cluster-a", Namespace: "ns", Name: "deploy/workspace", Path: "/root/run 1/DONE"}
	cases := []struct {
		name string
		k    kubectl
		seen string
	}{
		{"not yet", kubectl{code: 7}, wait.Running},
		{"done", kubectl{out: "ok 600 lines\nsecond line"}, wait.Succeeded},
		{"empty file", kubectl{}, wait.Succeeded},
		{"failed", kubectl{out: "FAILED: out of memory"}, wait.Failed},
		{"pod not found", kubectl{code: 1, errOut: "Error from server (NotFound): pods not found"}, wait.Unseen},
	}
	for _, c := range cases {
		w := &Watcher{Kubectl: "kubectl", Exec: c.k.exec}
		if seen, _ := w.Look(context.Background(), file); seen != c.seen {
			t.Errorf("%s: seen %q", c.name, seen)
		}
		// The path is an argument of the script, never part of it.
		args := c.k.calls[0]
		if args[len(args)-1] != "/root/run 1/DONE" || strings.Contains(args[len(args)-3], "run 1") {
			t.Fatalf("%s: args = %q", c.name, args)
		}
	}
}

// server runs the real handlers over a task repository with one task.
func server(t *testing.T) (*task.Store, *wait.Store, *handover.Store, *httptest.Server) {
	t.Helper()
	store, err := task.Open(testrecords.Init(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(task.NewTask{Area: "work", Title: "計算を待つタスク", Status: task.Waiting}); err != nil {
		t.Fatal(err)
	}
	waits, err := wait.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handovers, err := handover.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := (&web.Server{Store: store, Version: "test", Waits: waits, Handovers: handovers}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return store, waits, handovers, srv
}

func TestTheWatcherReportsTheEndAndStartsTheContinuation(t *testing.T) {
	store, waits, handovers, srv := server(t)
	id := store.List()[0].ID
	item, _, err := waits.Add(wait.Wait{TaskID: id, Kind: wait.Job, Context: "cluster-a", Namespace: "ns", Name: "score-1", By: time.Now().Add(time.Hour), Next: wait.NextClaude, Then: "結果を回収して、表を作ります。", From: "local-codex"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	k := &kubectl{out: `{"status":{"active":1}}`}
	w := &Watcher{Kubectl: "kubectl", Base: func() string { return srv.URL }, Client: &http.Client{Timeout: 5 * time.Second}, Host: "mac", Dir: dir, LogDir: filepath.Join(dir, "logs"),
		Every: time.Nanosecond, Exec: k.exec,
		// The stand-in for a new conversation keeps what it was told.
		Runners: map[string][]string{wait.NextClaude: {"sh", "-c", `cat > "{dir}/prompt.txt"; echo "$LACIA_WAIT in $PWD"; exit 3`}}}
	ctx := context.Background()

	w.Tick(ctx)
	if got, _ := waits.Get(item.ID); got.State != wait.Waiting || got.Seen != wait.Running {
		t.Fatalf("after the first look: %+v", got)
	}
	posts := len(k.calls)
	w.Tick(ctx) // nothing new to say
	if got, _ := waits.Get(item.ID); len(k.calls) != posts+1 || got.Seen != wait.Running {
		t.Fatalf("after the second look: %+v", got)
	}

	k.out = `{"status":{"succeeded":1,"conditions":[{"type":"Complete","status":"True"}]}}`
	w.Tick(ctx) // sees the end
	if got, _ := waits.Get(item.ID); got.State != wait.Finished {
		t.Fatalf("after the end: %+v", got)
	}
	w.Tick(ctx) // starts the continuation
	w.Tick(ctx) // and does not start it twice
	deadline := time.Now().Add(10 * time.Second)
	hid := "H" + strings.TrimPrefix(item.ID, "W")
	var got handover.Handover
	for got, _ = handovers.Get(hid); got.Ended.IsZero() && time.Now().Before(deadline); got, _ = handovers.Get(hid) {
		time.Sleep(20 * time.Millisecond)
	}
	if got.Runner != "mac" || got.Ended.IsZero() || got.ExitCode != 3 {
		t.Fatalf("after the continuation: %+v", got)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	for _, want := range []string{"lacia wait ack " + hid, "lacia task show " + id, "結果を回収して、表を作ります。", "正常に終わりました", "local-codex", "オーナーさんの新しい許可ではありません"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
	real, _ := filepath.EvalSymlinks(dir)
	if out, _ := os.ReadFile(filepath.Join(dir, "logs", hid+".log")); !strings.Contains(string(out), item.ID+" in "+real) && !strings.Contains(string(out), item.ID+" in "+dir) {
		t.Fatalf("log of the continuation = %q", out)
	}
	data, _ := readTestHistory(t, filepath.Join(store.Root(), store.List()[0].Dir))
	for _, want := range []string{"申し送り " + hid + " の届け方を記録しました。\nmac で Claude の新しい会話を起動します", "終了コード 3 で終わりました"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the task's log lacks %q:\n%s", want, data)
		}
	}
	w.Stop()
}

func TestAKindWithoutARunnerIsLeftToTheServer(t *testing.T) {
	store, waits, handovers, srv := server(t)
	item, _, _ := waits.Add(wait.Wait{TaskID: store.List()[0].ID, Kind: wait.Job, Context: "cluster-a", Namespace: "ns", Name: "score-1", By: time.Now().Add(time.Hour), Next: wait.NextClaude, Then: "続き。", From: "local-codex"})
	waits.Observe(item.ID, wait.Succeeded, "")
	handovers.Add(handover.Handover{WaitID: item.ID, TaskID: item.TaskID, Next: item.Next, Then: item.Then, From: item.From, FromKey: item.FromKey})
	w := &Watcher{Kubectl: "kubectl", Base: func() string { return srv.URL }, Client: &http.Client{Timeout: 5 * time.Second}, Runners: map[string][]string{wait.NextClaude: nil}}
	w.Tick(context.Background())
	if got, _ := handovers.Get("H" + strings.TrimPrefix(item.ID, "W")); !got.Started.IsZero() {
		t.Fatalf("started without a runner: %+v", got)
	}
	w.Stop()
}

// heard makes the server hear from a Codex conversation on the local device, the
// way any lacia command run inside it does.
func heard(t *testing.T, srv *httptest.Server, taskID, dir, thread, side string, pid int) string {
	t.Helper()
	return heardFrom(t, srv, taskID, dir, caller.Agent{Kind: caller.Codex, PID: pid, Session: thread, Home: homes[side]})
}

// heardFrom is heard for a conversation whose processes are spelled out.
func heardFrom(t *testing.T, srv *httptest.Server, taskID, dir string, agent caller.Agent) string {
	t.Helper()
	who := caller.Caller{Place: "local", Host: "mac", Dir: dir, Agents: []caller.Agent{agent}}
	req, _ := http.NewRequest("POST", srv.URL+"/api/tasks/"+taskID, strings.NewReader(`{"log":"作業の記録です。"}`))
	req.Header.Set(caller.Header, who.Encode())
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("heard: %v %v", resp, err)
	}
	resp.Body.Close()
	time.Sleep(2 * time.Millisecond) // keep the order of "last heard" distinct
	return who.Key()
}

// homes are the settings directories of the two logins in these tests.
var homes = map[string]string{"work": "/Users/u/.codex-work", "personal": "/Users/u/.codex"}

// codexes answers for the two Codex installations: each knows only the
// threads listed for it.
type codexes struct {
	threads map[string][]string // installation → thread IDs it holds
	calls   []string
}

func (c *codexes) exec(_ context.Context, name string, args ...string) (string, string, int, error) {
	// The command of a login here is `sh -c <script> <name>`; what the
	// watcher appends starts at "queue".
	at := 0
	for at < len(args) && args[at] != "queue" {
		at++
	}
	name, args = args[at-1], args[at:]
	c.calls = append(c.calls, name+" "+strings.Join(args[:3], " "))
	for _, thread := range c.threads[name] {
		if args[2] == thread {
			return "Queued message m-1 for thread " + thread + ".", "", 0, nil
		}
	}
	return "", "Error: failed to queue session message: no rollout found for thread id " + args[2], 1, nil
}

func TestTheContinuationGoesToTheCodexConversationThatIsRunning(t *testing.T) {
	// Each login starts a script that leaves a file where a new
	// conversation would start; the arguments Codex would get are ignored.
	sides := func(dir string) []Login {
		login := func(name, label, home string) Login {
			return Login{Name: name, Label: label, Home: home, Command: []string{"sh", "-c", `cat > "` + dir + `/fresh-` + name + `.txt"`, "codex-" + name}}
		}
		return []Login{login("work", "仕事用", "/Users/u/.codex-work"), login("personal", "個人用", "/Users/u/.codex")}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, srv *httptest.Server, id, dir string) (fromKey string)
		alive map[int]bool
		holds map[string][]string
		calls []string // the queue commands tried, in order
		via   string   // expected in the task's log
		fresh string   // the installation a new conversation was started with, if any
	}{
		{"the conversation says it is the personal one", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			heard(t, srv, id, dir, "thread-p", "personal", 11)
			return ""
		}, map[int]bool{11: true}, map[string][]string{"codex-personal": {"thread-p"}},
			[]string{"codex-personal queue --thread thread-p"}, "Codex の会話 local-codex（個人用）へ届けました", ""},
		{"an older conversation does not say which it is", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			heard(t, srv, id, dir, "thread-p", "", 11)
			return ""
		}, map[int]bool{11: true}, map[string][]string{"codex-personal": {"thread-p"}},
			[]string{"codex-work queue --thread thread-p", "codex-personal queue --thread thread-p"}, "（個人用）へ届けました", ""},
		{"the one that registered is preferred to the others", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			key := heard(t, srv, id, dir, "thread-w", "work", 12)
			heard(t, srv, id, dir, "thread-p", "personal", 11)
			return key
		}, map[int]bool{11: true, 12: true}, map[string][]string{"codex-personal": {"thread-p"}, "codex-work": {"thread-w"}},
			[]string{"codex-work queue --thread thread-w"}, "（仕事用）へ届けました", ""},
		{"the app-server was replaced, the loop above it is still there", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			heardFrom(t, srv, id, dir, caller.Agent{Kind: caller.Codex, PID: 21, Started: 7, Session: "thread-p", Home: homes["personal"], Above: []caller.Proc{{PID: 20, Started: 5}}})
			return ""
		}, map[int]bool{20: true}, map[string][]string{"codex-personal": {"thread-p"}},
			[]string{"codex-personal queue --thread thread-p"}, "Codex の会話 local-codex（個人用）へ届けました", ""},
		{"the most recently active conversation is preferred", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			// Shared process age does not tell which conversation is active.
			heardFrom(t, srv, id, dir, caller.Agent{Kind: caller.Codex, PID: 31, Started: 100, Session: "thread-old", Home: homes["personal"]})
			heardFrom(t, srv, id, dir, caller.Agent{Kind: caller.Codex, PID: 32, Started: 100, Session: "thread-new", Home: homes["work"]})
			return ""
		}, map[int]bool{31: true, 32: true}, map[string][]string{"codex-personal": {"thread-old"}, "codex-work": {"thread-new"}},
			[]string{"codex-work queue --thread thread-new"}, "（仕事用）へ届けました", ""},
		{"a conversation elsewhere is not this one's business", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			heard(t, srv, id, "/Users/u/git/other", "thread-x", "work", 13)
			return ""
		}, map[int]bool{13: true}, nil, nil, "Codex（仕事用）の新しい会話を起動します", "work"},
		{"nothing is running: a new one, of the kind used last", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			heard(t, srv, id, dir, "thread-p", "personal", 11)
			return ""
		}, map[int]bool{}, nil, nil, "Codex（個人用）の新しい会話を起動します", "personal"},
		{"the running one cannot be reached: a new one of its kind", func(t *testing.T, srv *httptest.Server, id, dir string) string {
			heard(t, srv, id, dir, "thread-p", "personal", 11)
			return ""
		}, map[int]bool{11: true}, map[string][]string{},
			[]string{"codex-personal queue --thread thread-p"}, "Codex（個人用）の新しい会話を起動します", "personal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, waits, handovers, srv := server(t)
			id, dir := store.List()[0].ID, t.TempDir()
			fromKey := c.setup(t, srv, id, dir)
			item, _, err := waits.Add(wait.Wait{TaskID: id, Kind: wait.Job, Context: "cluster-a", Namespace: "ns", Name: "score-1", By: time.Now().Add(time.Hour), Next: wait.NextCodex, Then: "結果を回収します。", From: "local-claude", FromKey: fromKey})
			if err != nil {
				t.Fatal(err)
			}
			waits.Observe(item.ID, wait.Succeeded, "")
			handovers.Add(handover.Handover{WaitID: item.ID, TaskID: item.TaskID, Next: item.Next, Then: item.Then, From: item.From, FromKey: item.FromKey})
			cx := &codexes{threads: c.holds}
			w := &Watcher{Kubectl: "kubectl", Base: func() string { return srv.URL }, Client: &http.Client{Timeout: 5 * time.Second}, Host: "mac", Dir: dir,
				Logins: sides(dir), Exec: cx.exec, Alive: func(pid int) bool { return c.alive[pid] }}
			w.Tick(context.Background())
			w.Tick(context.Background()) // never delivered twice
			if strings.Join(cx.calls, "; ") != strings.Join(c.calls, "; ") {
				t.Fatalf("queue commands = %q", cx.calls)
			}
			deadline := time.Now().Add(10 * time.Second)
			got, _ := handovers.Get("H" + strings.TrimPrefix(item.ID, "W"))
			for c.fresh != "" && got.Ended.IsZero() && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
				got, _ = handovers.Get("H" + strings.TrimPrefix(item.ID, "W"))
			}
			if got.Started.IsZero() || !strings.Contains(got.Via, c.via) {
				t.Fatalf("wait = %+v", got)
			}
			data, _ := readTestHistory(t, filepath.Join(store.Root(), store.List()[0].Dir))
			if !strings.Contains(string(data), c.via) {
				t.Fatalf("the task's log lacks %q:\n%s", c.via, data)
			}
			for _, side := range []string{"work", "personal"} {
				_, err := os.Stat(filepath.Join(dir, "fresh-"+side+".txt"))
				if started := err == nil; started != (side == c.fresh) {
					t.Fatalf("a new %s conversation: started = %v", side, started)
				}
			}
			w.Stop()
		})
	}
}

func TestTheMessageForARunningConversationIsShort(t *testing.T) {
	long := strings.Repeat("長い依頼の文章です。", 400)
	for _, item := range []handover.Handover{
		{ID: "H123456789012345678901234", WaitID: "W12345678", TaskID: "T1066", Then: long, From: "local-claude 2"},
		{ID: "H123456789012345678901234", TaskID: "T1066", Then: long, From: "local-claude 2"},
	} {
		message := QueueMessage(item)
		// The tool for sending to a thread takes 1,000 bytes at most.
		if len(message) > 1000 || !strings.Contains(message, "lacia wait show "+item.ID) || !strings.Contains(message, "lacia wait ack "+item.ID) || strings.Contains(message, "長い依頼") {
			t.Fatalf("%d bytes: %s", len(message), message)
		}
	}
}

func TestHandoversAreDeliveredWhenWatchingIsDisabled(t *testing.T) {
	var delivered atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/waits":
			http.NotFound(out, r)
		case "GET /api/handovers":
			io.WriteString(out, `[{"id":"H123","task_id":"T1","next":"claude","state":"queued","then":"依頼です。"}]`)
		case "PATCH /api/handovers/H123":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["start"] == "mac" {
				delivered.Store(true)
			}
			io.WriteString(out, `{}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	defer srv.Close()
	w := &Watcher{Base: func() string { return srv.URL }, Client: srv.Client(), Host: "mac", Dir: t.TempDir(), Runners: map[string][]string{"claude": {"sh", "-c", "cat >/dev/null"}}}
	w.Tick(context.Background())
	if !delivered.Load() {
		t.Fatal("handover was not started after waits returned 404")
	}
	w.Stop()
	// Let the runner report its end before the test server closes.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		active := len(w.running) > 0
		w.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("handover runner did not finish")
}

func readTestHistory(t *testing.T, dir string) ([]byte, error) {
	t.Helper()
	lines, err := task.ReadHistory(dir, 1, 0)
	return []byte(strings.Join(lines, "\n")), err
}

func TestDormantConversationWithLiveSharedProcessIsNotQueued(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			// The server retains an inactive conversation when it still owns a task.
			rows := []map[string]any{{"key": "old", "label": "old", "active": active, "last": time.Now().Add(-24 * time.Hour), "caller": map[string]any{"place": "local", "host": "mac", "dir": "/tasks", "agents": []map[string]any{{"kind": "codex", "pid": 11, "session": "thread-old", "codex_home": "/personal"}}}}}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(rows) }))
			defer srv.Close()
			cx := &codexes{threads: map[string][]string{"codex-personal": {"thread-old"}}}
			w := &Watcher{Base: func() string { return srv.URL }, Client: srv.Client(), Host: "mac", Dir: "/tasks", Logins: []Login{{Name: "personal", Home: "/personal", Command: []string{"sh", "-c", "true", "codex-personal"}}}, Alive: func(int) bool { return true }, Exec: cx.exec}
			_, delivered := w.queueToCodex(context.Background(), handover.Handover{FromKey: "old"})
			if (delivered != "") != active || (len(cx.calls) > 0) != active {
				t.Fatalf("active=%v, delivered=%q, calls=%v", active, delivered, cx.calls)
			}
		})
	}
}

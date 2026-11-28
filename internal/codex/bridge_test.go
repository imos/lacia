package codex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/imos/lacia/internal/rules"
)

func TestRunForBindsChildrenBeforeStartResponse(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lacia-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		write := func(v any) {
			data, _ := json.Marshal(v)
			if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
				t.Error(err)
			}
		}
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
				Params struct {
					Input []struct {
						Type string `json:"type"`
						Path string `json:"path"`
						Text string `json:"text"`
					} `json:"input"`
				} `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil || req.ID == nil {
				continue
			}
			result := any(map[string]any{})
			switch req.Method {
			case "thread/resume":
				result = map[string]any{"thread": map[string]any{"id": "root-thread"}}
			case "turn/start":
				if len(req.Params.Input) != 2 || req.Params.Input[0].Type != "text" || req.Params.Input[0].Text != "依頼です" || req.Params.Input[1].Type != "localImage" || req.Params.Input[1].Path != "/synthetic/label.png" {
					t.Errorf("incorrect multimodal input: %+v", req.Params.Input)
				}
				for _, kind := range []string{"started", "completed"} {
					write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "root-thread", "turnId": "turn-a", "item": map[string]any{"type": "subAgentActivity", "agentThreadId": "child-a", "kind": kind}}})
				}
				write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "root-thread", "turnId": "turn-a", "item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": "調査を進めます。"}}})
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "root-thread", "turn": map[string]any{"id": "turn-a", "status": "completed"}}})
				result = map[string]any{"turn": map[string]any{"id": "turn-a"}}
			}
			write(map[string]any{"id": *req.ID, "result": result})
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	if err := os.WriteFile(filepath.Join(dir, "thread.json"), []byte(`{"thread_id":"root-thread"}`), 0600); err != nil {
		t.Fatal(err)
	}
	b := New(Config{State: testState(t, dir)})
	defer b.Close()
	var mu sync.Mutex
	var got []Child
	b.OnChild = func(c Child) { mu.Lock(); got = append(got, c); mu.Unlock() }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := b.RunForImages(ctx, "依頼です", "request-a", []string{"/synthetic/label.png"})
	if err != nil || res.Reply != "調査を進めます。" {
		t.Fatalf("result %+v error %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[1].RequestID != "request-a" || got[1].OriginTurnID != "turn-a" {
		t.Fatalf("racing notifications: %+v", got)
	}
}

func TestChildCompletionArrivesAfterParentAndNextTurn(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lacia-late-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "codex.sock"))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		write := func(v any) {
			data, _ := json.Marshal(v)
			if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
				t.Error(err)
			}
		}
		child := func(kind string) {
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "root", "turnId": "first", "item": map[string]any{"type": "subAgentActivity", "agentThreadId": "worker", "kind": kind}}})
		}
		turns := 0
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
				Params struct {
					ThreadID string `json:"threadId"`
				} `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil || req.ID == nil {
				continue
			}
			result := any(map[string]any{})
			switch req.Method {
			case "thread/resume":
				result = map[string]any{"thread": map[string]any{"id": req.Params.ThreadID}}
			case "thread/turns/list":
				result = map[string]any{"data": []activityTurn{
					{ID: "missed-finish", Status: "completed"},
					{ID: "still-running", Status: "inProgress"},
				}}
			case "turn/start":
				turns++
				id := "first"
				if turns == 1 {
					child("started")
				} else {
					id = "second"
				}
				result = map[string]any{"turn": map[string]any{"id": id}}
				write(map[string]any{"id": *req.ID, "result": result})
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "root", "turn": map[string]any{"id": id, "status": "completed"}}})
				if turns == 2 {
					select {
					case <-release:
						child("completed")
					case <-r.Context().Done():
						return
					}
				}
				continue
			}
			write(map[string]any{"id": *req.ID, "result": result})
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	if err := os.WriteFile(filepath.Join(dir, "thread.json"), []byte(`{"thread_id":"root"}`), 0600); err != nil {
		t.Fatal(err)
	}
	b := New(Config{State: testState(t, dir)})
	defer b.Close()
	completed := make(chan Child, 1)
	b.OnChild = func(c Child) {
		if c.Finished() {
			completed <- c
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, id := range []string{"original-request", "next-request"} {
		if _, err := b.RunFor(ctx, "依頼です", id); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	select {
	case c := <-completed:
		if c.RequestID != "original-request" || c.OriginTurnID != "first" {
			t.Fatalf("late completion changed origin: %+v", c)
		}
	case <-ctx.Done():
		t.Fatal("completion subscription ended with parent turn")
	}
	b.Close()

	reopened := New(Config{State: testState(t, dir)})
	reopened.turns["missed-finish"] = &turn{}
	defer reopened.Close()
	if err := reopened.connect(ctx); err != nil || reopened.ThreadID() != "root" {
		t.Fatalf("replaced server did not resume the window: thread=%q err=%v", reopened.ThreadID(), err)
	}
	if reopened.turns["missed-finish"] != nil || reopened.turns["still-running"] == nil {
		t.Fatal("reconnection did not distinguish finished and still-running turns")
	}
}

func TestConversationRefreshesRuleVersionBetweenTurns(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lacia-rules-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "codex.sock"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := make(chan string, 5)
	starts := make(chan string, 3)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		write := func(value any) {
			data, _ := json.Marshal(value)
			if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
				t.Error(err)
			}
		}
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var request struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
				Params struct {
					Instructions string `json:"developerInstructions"`
					Input        []struct{ Text string }
				}
			}
			if json.Unmarshal(data, &request) != nil || request.ID == nil {
				continue
			}
			result := any(map[string]any{})
			switch request.Method {
			case "thread/start", "thread/resume":
				starts <- request.Params.Instructions
				result = map[string]any{"thread": map[string]any{"id": "thread"}}
			case "turn/start":
				inputs <- request.Params.Input[0].Text
				id := strconv.Itoa(*request.ID)
				result = map[string]any{"turn": map[string]any{"id": id}}
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": id, "status": "completed"}}})
			}
			write(map[string]any{"id": *request.ID, "result": result})
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current := "one"
	config := Config{State: testState(t, dir), CurrentInstructions: func(context.Context) (string, rules.Revision) {
		return current, rules.Revision{"core": current}
	}}
	b := New(config)
	defer func() { b.Close() }()
	for i, message := range []string{"最初です。", "更新後です。", "続きです。", "再接続しました。"} {
		if i == 1 {
			current = "two"
		}
		if i == 3 {
			b.Close()
			b = New(config)
		}
		if _, err := b.Run(ctx, message); err != nil {
			t.Fatal(err)
		}
		got := <-inputs
		if i == 1 {
			if !strings.Contains(got, "決まりの版が変わりました") || !strings.HasSuffix(got, message) {
				t.Fatalf("update not delivered: %q", got)
			}
		} else if got != message {
			t.Fatalf("unexpected notice: %q", got)
		}
	}
	if first, resumed := <-starts, <-starts; first != "one" || resumed != "two" {
		t.Fatalf("wrong startup instructions: %q, %q", first, resumed)
	}
}

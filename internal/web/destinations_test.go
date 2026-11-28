package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

func setDestinationTestServer(s *Server) {
	s.Place = "work"
	s.ResolveDestination = func(raw string) (caller.Destination, error) {
		dest, err := caller.ParseDestination(raw)
		if err != nil {
			return dest, err
		}
		if dest.Place == "" {
			dest.Place = "work"
		}
		if dest.Place != "local" && dest.Place != "work" {
			return dest, fmt.Errorf("不明な場所 %s", dest.Place)
		}
		if dest.Kind == "" {
			dest.Kind = caller.Codex
		}
		return dest, nil
	}
	s.AuthorizeHandover = func(place, token string) bool { return place == "local" && token == "fixture-key" }
}

func TestAddressedHandoverAndWait(t *testing.T) {
	store, s, h, _ := setupWaits(t)
	setDestinationTestServer(s)
	tell := func(to string) map[string]any {
		t.Helper()
		out := as(t, h, macClaude, "POST", "/api/handovers",
			fmt.Sprintf(`{"task":"T1001","next":%q,"then":"結果を確認してください。"}`, to))
		return out["handover"].(map[string]any)
	}
	remote := tell("local")
	if remote["next"] != "local-codex" {
		t.Fatalf("remote: %v", remote)
	}
	local := tell("work-claude")
	localID := local["id"].(string)
	id := remote["id"].(string)
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if code := call("GET", "/api/handovers?place=local", "", "").Code; code != http.StatusUnauthorized {
		t.Fatalf("unregistered poll: %d", code)
	}
	list := call("GET", "/api/handovers?place=local", "", "fixture-key")
	var items []handover.Handover
	if err := json.Unmarshal(list.Body.Bytes(), &items); list.Code != http.StatusOK || err != nil || len(items) != 1 || items[0].ID != id {
		t.Fatalf("worker sees work addressed elsewhere: %s", list.Body.String())
	}
	if code := call("PATCH", "/api/handovers/"+id, `{"start":"mac","place":"local"}`, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("unregistered start: %d", code)
	}
	if code := call("PATCH", "/api/handovers/"+localID, `{"start":"mac","place":"local"}`, "fixture-key").Code; code != http.StatusConflict {
		t.Fatalf("started for another place: %d", code)
	}
	if code := call("PATCH", "/api/handovers/"+id, `{"start":"mac","place":"local"}`, "fixture-key").Code; code != http.StatusOK {
		t.Fatalf("registered start: %d", code)
	}
	if code := status(h, macClaude, "/api/handovers/"+id, `{"ack":true}`); code != http.StatusConflict {
		t.Fatalf("ack from the wrong conversation kind: %d", code)
	}
	as(t, h, macCodex, "POST", "/api/handovers/"+id, `{"ack":true}`)
	if got, _ := store.Get("T1001"); got.Owner != "local-codex" {
		t.Fatalf("task owner = %q", got.Owner)
	}
	if code := status(h, macCodex, "/api/handovers", `{"task":"T1001","next":"unregistered-codex","then":"続けてください。"}`); code != http.StatusBadRequest {
		t.Fatalf("unregistered recipient %d", code)
	}

	waitID := addWait(t, h, macClaude, "")
	as(t, h, caller.Caller{}, "POST", "/api/waits/"+waitID, `{"seen":"succeeded"}`)
	wait, _ := s.Waits.Get(waitID)
	next, err := s.Handovers.Get(wait.HandoverID)
	if err != nil || next.Next != "local-claude" {
		t.Fatalf("wait did not resume at the original place and kind: %+v, %v", next, err)
	}
}

func TestServerHandoverAndDatedTaskUseTheirAddressedAgent(t *testing.T) {
	store, s, h, now := setupWaits(t)
	setDestinationTestServer(s)
	queue, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	s.Requests = queue
	out := as(t, h, macCodex, "POST", "/api/handovers", `{"task":"T1001","next":"work-claude","then":"中身を確認してください。"}`)
	id := out["handover"].(map[string]any)["id"].(string)
	s.TickWaits()
	s.TickWaits()
	if items := queue.Recent(10, "T1001", true); len(items) != 1 || items[0].Agent != caller.Claude ||
		!strings.Contains(items[0].Text, "lacia wait ack "+id) {
		t.Fatalf("queued server handovers = %+v", items)
	}
	serverClaude := caller.Caller{Place: "work", Host: "server", Agents: []caller.Agent{{Kind: caller.Claude, Session: "claude-work"}}}
	as(t, h, serverClaude, "POST", "/api/handovers/"+id, `{"ack":true}`)

	for _, test := range []struct {
		to     string
		remote bool
	}{{"local-codex", true}, {"work-claude", false}} {
		created, err := store.Create(task.NewTask{Area: "work", Title: "日時で始めるタスク"})
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range map[string]string{"review_at": "2026-11-18 09:00", "on_ready": "start " + test.to} {
			if _, _, err := store.SetField(created.ID, key, value, ""); err != nil {
				t.Fatal(err)
			}
		}
		woken := store.Wake(*now)
		if len(woken) != 1 || !woken[0].StartsWhenReady() {
			t.Fatalf("woken = %+v", woken)
		}
		s.StartReady(woken[0])
		if test.remote {
			items := s.handoversOf(created.ID)
			if len(items) != 1 || items[0].Next != test.to {
				t.Fatalf("remote on_ready = %v", items)
			}
		} else {
			items := queue.Recent(5, created.ID, true)
			if len(items) != 1 || items[0].Agent != caller.Claude {
				t.Fatalf("server on_ready = %v", items)
			}
		}
	}
}

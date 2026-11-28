package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/imos/lacia/internal/effect/disk"
)

func TestChildRoutesRaceInterruptionRestartAndReuse(t *testing.T) {
	dir := t.TempDir()
	b := New(Config{State: testState(t, dir)})
	var got []Child
	b.OnChild = func(c Child) { got = append(got, c) }
	b.awaitingTurn(true)
	b.routeChild(Child{ThreadID: "child-a", TurnID: "turn-a", Kind: "started"})
	b.routeChild(Child{ThreadID: "child-a", TurnID: "turn-a", Kind: "completed"})
	if len(got) != 0 {
		t.Fatal("notifications escaped before the originating turn was bound")
	}
	if err := b.bindTurn("turn-a", "request-a"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].RequestID != "request-a" {
		t.Fatalf("got %+v", got)
	}
	if err := b.bindTurn("turn-b", "request-b"); err != nil {
		t.Fatal(err)
	}
	b.routeChild(Child{ThreadID: "child-a", TurnID: "turn-b", Kind: "completed"})
	if got[2].RequestID != "request-a" || got[2].OriginTurnID != "turn-a" {
		t.Fatalf("intervening request changed origin: %+v", got[2])
	}
	// The routes are private durable metadata, not conversation bodies.
	st, err := os.Stat(filepath.Join(dir, "child-routes.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v %v", st, err)
	}
	restarted := New(Config{State: testState(t, dir)})
	restarted.OnChild = b.OnChild
	restarted.routeChild(Child{ThreadID: "child-a", TurnID: "turn-b", Kind: "shutdown"})
	if got[3].RequestID != "request-a" || got[3].OriginTurnID != "turn-a" {
		t.Fatalf("restart lost route: %+v", got[3])
	}
	// An explicit new execution of the same child has its own completion key.
	restarted.routeChild(Child{ThreadID: "child-a", TurnID: "turn-b", Kind: "started"})
	restarted.routeChild(Child{ThreadID: "child-a", TurnID: "turn-b", Kind: "completed"})
	if got[5].RequestID != "request-b" || got[5].OriginTurnID != "turn-b" {
		t.Fatalf("reused child: %+v", got[5])
	}
}

func TestUnknownAndCorruptRoutesNeverUseLatestRequest(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "corrupt"}[corrupt], func(t *testing.T) {
			dir := t.TempDir()
			b := New(Config{State: testState(t, dir)})
			if corrupt {
				if err := os.WriteFile(filepath.Join(dir, "child-routes.json"), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := b.bindTurn("turn-latest", "request-latest"); err != nil {
				t.Fatal(err)
			}
			var got Child
			b.OnChild = func(c Child) { got = c }
			b.routeChild(Child{ThreadID: "unknown-child", TurnID: "old-turn", Kind: "completed"})
			if got.RequestID != "" || got.ThreadID == "" {
				t.Fatalf("unknown route: %+v", got)
			}
		})
	}
}

func TestRecoveryDoesNotFinishReusedRunningChild(t *testing.T) {
	b := New(Config{State: testState(t, t.TempDir())})
	if err := b.bindTurn("turn-a", "request-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.bindTurn("turn-b", "request-b"); err != nil {
		t.Fatal(err)
	}
	item := func(kind string) activityItem {
		return activityItem{Type: "subAgentActivity", Kind: kind, AgentThreadID: "child-a"}
	}
	var got []Child
	b.OnChild = func(c Child) { got = append(got, c) }
	b.replayChildren([]activityTurn{{ID: "turn-a", Items: []activityItem{item("started"), item("completed")}}, {ID: "turn-b", Items: []activityItem{item("started")}}})
	if len(got) != 0 {
		t.Fatalf("old completion finished a running child: %+v", got)
	}
	b.routeChild(Child{ThreadID: "child-a", TurnID: "turn-b", Kind: "completed"})
	if len(got) != 1 || got[0].RequestID != "request-b" {
		t.Fatalf("wrong restored origin: %+v", got)
	}
	// The latest terminal is replayable with the same execution key.
	b.replayChildren([]activityTurn{{ID: "turn-b", Items: []activityItem{item("started"), item("completed")}}})
	if len(got) != 2 || got[0].OriginTurnID != got[1].OriginTurnID {
		t.Fatalf("recovery key changed: %+v", got)
	}
}

func TestUnboundTerminalNeverUsesCompletionTurnAsOrigin(t *testing.T) {
	b := New(Config{State: testState(t, t.TempDir())})
	if err := b.bindTurn("turn-b", "request-b"); err != nil {
		t.Fatal(err)
	}
	b.OnChild = func(c Child) {
		if c.RequestID != "" {
			t.Fatalf("terminal guessed its origin: %+v", c)
		}
	}
	b.routeChild(Child{ThreadID: "old-child", TurnID: "turn-b", Kind: "completed"})
}

func TestRecoveryFindsAReusedChildWhoseStartNotificationWasMissed(t *testing.T) {
	b := New(Config{State: testState(t, t.TempDir())})
	if err := b.bindTurn("turn-a", "request-a"); err != nil {
		t.Fatal(err)
	}
	b.routeChild(Child{ThreadID: "child-a", TurnID: "turn-a", Kind: "started"})
	b.routeChild(Child{ThreadID: "child-a", TurnID: "turn-a", Kind: "completed"})
	if err := b.bindTurn("turn-b", "request-b"); err != nil {
		t.Fatal(err)
	}
	var got Child
	b.OnChild = func(c Child) { got = c }
	item := func(kind string) activityItem {
		return activityItem{Type: "subAgentActivity", Kind: kind, AgentThreadID: "child-a"}
	}
	b.replayChildren([]activityTurn{{ID: "turn-a", Items: []activityItem{item("started"), item("completed")}}, {ID: "turn-b", Items: []activityItem{item("started"), item("completed")}}})
	if got.RequestID != "request-b" || got.OriginTurnID != "turn-b" {
		t.Fatalf("missed reuse was not recovered: %+v", got)
	}
}

// testState opens dir as the conversation's state directory.
func testState(t *testing.T, dir string) *disk.Dir {
	t.Helper()
	state, err := disk.OpenTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	return state
}

package caller

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
)

func table(procs ...process) func(int) (process, error) {
	return func(pid int) (process, error) {
		for _, p := range procs {
			if p.pid == pid {
				return p, nil
			}
		}
		return process{}, errors.New("no such process")
	}
}

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestDetectFindsTheNearestAgent(t *testing.T) {
	// Codex started Claude, and Claude's shell runs the command. Both
	// session variables are set; only the processes tell the order.
	lookup := table(
		process{pid: 50, parent: 40, exe: "/bin/zsh", args: []string{"zsh", "-c", "lacia task note"}},
		process{pid: 40, parent: 30, started: 4, exe: "/Users/u/.local/bin/claude", args: []string{"claude", "-p"}},
		process{pid: 30, parent: 20, exe: "/bin/zsh"},
		process{pid: 20, parent: 10, started: 2, exe: "/opt/codex/codex-aarch64-apple-darwin", args: []string{"codex", "app-server"}},
		// The npm wrapper that started the real binary is the same agent.
		process{pid: 10, parent: 1, started: 1, exe: "/usr/bin/node", args: []string{"node", "/opt/codex/bin/codex.js"}},
	)
	c := detect(50, lookup, env(map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-1", "CODEX_THREAD_ID": "codex-1"}))
	if c.Name() != "local-claude" {
		t.Fatalf("name = %q", c.Name())
	}
	if len(c.Agents) != 2 || c.Agents[0].PID != 40 || c.Agents[0].Session != "claude-1" || c.Agents[1].PID != 20 || c.Agents[1].Session != "codex-1" {
		t.Fatalf("agents = %+v", c.Agents)
	}
	if via := c.Via(); len(via) != 1 || via[0] != "local-codex" {
		t.Fatalf("via = %v", via)
	}

	// The other way round, with the same variables.
	lookup = table(
		process{pid: 50, parent: 40, exe: "/bin/sh"},
		process{pid: 40, parent: 30, exe: "/usr/local/bin/codex", args: []string{"codex", "exec"}},
		process{pid: 30, parent: 20, exe: "/bin/zsh"},
		process{pid: 20, parent: 1, exe: "/x/claude/versions/2.1.311", args: []string{"claude"}},
	)
	c = detect(50, lookup, env(map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-1", "CODEX_THREAD_ID": "codex-1", "LACIA_PROFILE": "work"}))
	if c.Name() != "work-codex" || c.Agents[0].Session != "codex-1" || c.Agents[1].Kind != Claude {
		t.Fatalf("caller = %+v", c)
	}
}

func TestDetectNestedSameKind(t *testing.T) {
	// Claude started another Claude through a shell: two conversations, and
	// the session variable belongs to the nearer one.
	lookup := table(
		process{pid: 40, parent: 30, started: 9, args: []string{"claude", "-p"}},
		process{pid: 30, parent: 20, exe: "/bin/zsh"},
		process{pid: 20, parent: 1, started: 5, args: []string{"claude"}},
	)
	c := detect(40, lookup, env(map[string]string{"CLAUDE_CODE_SESSION_ID": "inner"}))
	if len(c.Agents) != 2 || c.Agents[0].Session != "inner" || c.Agents[1].Session != "" {
		t.Fatalf("agents = %+v", c.Agents)
	}
}

func TestDetectKeepsTheProcessAboveTheNearestOne(t *testing.T) {
	// The Codex app-server that serves the conversation is replaced when
	// it updates, while the conversation goes on. The loop above it is the
	// same agent and is kept, so that the conversation can still be found
	// running.
	lookup := table(
		process{pid: 50, parent: 45, exe: "/bin/zsh", args: []string{"zsh", "-c", "lacia task note"}},
		process{pid: 45, parent: 40, started: 8, exe: "/Users/u/.codex/packages/app-server-daemon/releases/0.171.0/bin/codex", args: []string{"codex", "app-server", "--listen", "unix://"}},
		process{pid: 40, parent: 1, started: 3, exe: "/Users/u/.codex/packages/app-server-daemon/releases/0.171.0/bin/codex", args: []string{"codex", "app-server", "daemon", "pid-update-loop"}},
	)
	c := detect(50, lookup, env(map[string]string{"CODEX_THREAD_ID": "codex-1"}))
	if len(c.Agents) != 1 || c.Agents[0].PID != 45 || c.Agents[0].Session != "codex-1" {
		t.Fatalf("agents = %+v", c.Agents)
	}
	procs := c.Agents[0].Procs()
	if len(procs) != 2 || procs[0] != (Proc{PID: 45, Started: 8}) || procs[1] != (Proc{PID: 40, Started: 3}) {
		t.Fatalf("procs = %+v", procs)
	}
}

func TestDetectFallsBackToTheEnvironment(t *testing.T) {
	c := detect(40, table(), env(map[string]string{"CODEX_THREAD_ID": "codex-1"}))
	if c.Name() != "local-codex" || c.Agents[0].Session != "codex-1" {
		t.Fatalf("caller = %+v", c)
	}
	if c := detect(40, table(process{pid: 40, parent: 1, exe: "/bin/zsh"}), env(nil)); c.Known() || c.Key() != "" {
		t.Fatalf("caller = %+v", c)
	}
}

func TestKeyFollowsTheSession(t *testing.T) {
	a := Caller{Place: "local", Host: "mac", Agents: []Agent{{Kind: Claude, PID: 1, Started: 1, Session: "s"}}}
	b := Caller{Place: "local", Host: "mac", Agents: []Agent{{Kind: Claude, PID: 2, Started: 2, Session: "s"}}}
	if a.Key() != b.Key() {
		t.Fatal("a resumed conversation got a new key")
	}
	b.Agents[0].Session = "other"
	if a.Key() == b.Key() {
		t.Fatal("two conversations share a key")
	}
	if got := Decode(a.Encode()); got.Key() != a.Key() || got.Name() != "local-claude" {
		t.Fatalf("decoded = %+v", got)
	}
	if Decode("???").Known() {
		t.Fatal("garbage decoded to a caller")
	}
}

func TestParseStat(t *testing.T) {
	stat := "4242 (my (odd) name) S 4000 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 200 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0"
	parent, started, ok := parseStat(stat)
	if !ok || parent != 4000 || started != 987654 {
		t.Fatalf("parent=%d started=%d ok=%v", parent, started, ok)
	}
}

func TestRegistryNumbersConcurrentSessions(t *testing.T) {
	dir, err := disk.OpenTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	r, err := OpenRegistry(dir, "sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return now }
	claude := func(session string) Caller {
		return Caller{Place: "local", Host: "mac", Agents: []Agent{{Kind: Claude, Session: session}, {Kind: Codex, Session: "c"}}}
	}
	first, _ := r.Seen(claude("a"))
	second, _ := r.Seen(claude("b"))
	if first.Label() != "local-claude" || second.Label() != "local-claude 2" {
		t.Fatalf("labels = %q, %q", first.Label(), second.Label())
	}
	if second.Actor() != "local-claude 2、local-codex から" {
		t.Fatalf("actor = %q", second.Actor())
	}
	// The number stays with the conversation, and is free again once the
	// first one has been silent for long enough.
	now = now.Add(Active + time.Hour)
	if again, _ := r.Seen(claude("b")); again.Label() != "local-claude 2" {
		t.Fatalf("label changed to %q", again.Label())
	}
	if third, _ := r.Seen(claude("c")); third.Label() != "local-claude" {
		t.Fatalf("third = %q", third.Label())
	}
	if _, ok := r.Seen(Caller{Place: "local"}); ok {
		t.Fatal("a caller without an agent was recorded")
	}
	reopened, err := OpenRegistry(dir, "sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := reopened.Get(second.Key); !ok || s.Label() != "local-claude 2" {
		t.Fatalf("reopened = %+v, %v", s, ok)
	}
}

func TestDetectTellsTheWorkCodexFromThePersonalOne(t *testing.T) {
	lookup := table(
		process{pid: 30, parent: 20, exe: "/bin/zsh"},
		process{pid: 20, parent: 10, exe: "/x/claude/versions/2.1.311", args: []string{"claude"}},
		process{pid: 10, parent: 1, exe: "/opt/codex/bin/codex", args: []string{"codex"}},
	)
	for home, want := range map[string]string{"/Users/u/.codex-work": "/Users/u/.codex-work", "/Users/u/.codex-personal/": "/Users/u/.codex-personal", "": ""} {
		c := detect(30, lookup, env(map[string]string{"CODEX_HOME": home, "CODEX_THREAD_ID": "t-1"}))
		// The setting belongs to the Codex, not to the Claude it started.
		if c.Agents[0].Kind != Claude || c.Agents[0].Home != "" || c.Agents[1].Home != want {
			t.Errorf("CODEX_HOME=%q: %+v", home, c.Agents)
		}
		if back := Decode(c.Encode()); back.Agents[1].Home != want {
			t.Errorf("CODEX_HOME=%q does not survive the header: %+v", home, back.Agents)
		}
	}
	// The login belongs to the agent lacia started with it.
	c := detect(30, lookup, env(map[string]string{"LACIA_LOGIN": "personal", "LACIA_LAUNCH": Codex, "CODEX_THREAD_ID": "t-1"}))
	if c.Agents[0].Login != "" || c.Agents[1].Login != "personal" || !strings.Contains(c.Describe(), "ログイン personal") {
		t.Errorf("login: %+v %s", c.Agents, c.Describe())
	}
}

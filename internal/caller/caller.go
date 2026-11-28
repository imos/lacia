// Package caller works out which conversation ran the lacia command.
//
// Codex and Claude Code start each other, and each passes its environment
// down, so the variables alone cannot say which of them is the one asking.
// The command therefore walks up its own parent processes: the nearest
// agent is the caller, and the agents above it are the ones that called it.
package caller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Header carries the caller to the server on every request.
const Header = "X-Lacia-Caller"

// Kinds of agent the command recognises.
const (
	Claude = "claude"
	Codex  = "codex"
)

// Agent is one agent process among the ancestors of the command.
type Agent struct {
	Kind string `json:"kind"`
	PID  int    `json:"pid,omitempty"`
	// Started tells two processes that reused a PID apart. Its unit differs
	// by system, so it is only ever compared for equality.
	Started int64 `json:"started,omitempty"`
	// Session is the conversation ID the agent gave its children. It stays
	// the same when a conversation is resumed in a new process.
	Session string `json:"session,omitempty"`
	// Home is the directory a Codex keeps its settings in (CODEX_HOME),
	// which tells its login from another's. Empty for Claude, and for a
	// Codex that uses the standard place.
	Home string `json:"codex_home,omitempty"`
	// Login is the name of the login the agent was started with, where it
	// was started with one (lacia codex --login, lacia codex-login).
	Login string `json:"login,omitempty"`
	// Above lists the other processes of the same agent, the ones directly
	// above PID, nearest first: the launcher that started the real binary,
	// or the loop that keeps a Codex app-server running and replaces it
	// when it updates. PID can therefore end while the conversation goes
	// on, so the conversation is running while any of them is.
	Above []Proc `json:"above,omitempty"`
}

type Proc struct {
	PID     int   `json:"pid"`
	Started int64 `json:"started,omitempty"`
}

// Procs lists the processes of the agent, nearest first.
func (a Agent) Procs() []Proc {
	if a.PID == 0 {
		return a.Above
	}
	return append([]Proc{{PID: a.PID, Started: a.Started}}, a.Above...)
}

// Caller is who ran the command.
type Caller struct {
	Place string `json:"place"` // "local" (no server's profile), or the name of the profile, such as "work"
	Host  string `json:"host,omitempty"`
	Dir   string `json:"dir,omitempty"` // working directory of the command
	// Agents lists the agent processes above the command, nearest first.
	Agents []Agent `json:"agents,omitempty"`
}

func (c Caller) Known() bool { return len(c.Agents) > 0 }

func (c Caller) name(a Agent) string { return c.Place + "-" + a.Kind }

// Name is the short name shown in lists, such as "local-claude". It is
// empty when no agent ran the command.
func (c Caller) Name() string {
	if !c.Known() {
		return ""
	}
	return c.name(c.Agents[0])
}

// Via names the agents that called the nearest one, nearest first.
func (c Caller) Via() []string {
	var names []string
	for i, a := range c.Agents {
		if i > 0 {
			names = append(names, c.name(a))
		}
	}
	return names
}

// Key identifies the conversation: by its session ID when there is one, by
// the process otherwise. It is empty when no agent ran the command.
func (c Caller) Key() string {
	if !c.Known() {
		return ""
	}
	a := c.Agents[0]
	id := "session:" + a.Session
	if a.Session == "" {
		id = fmt.Sprintf("process:%d:%d", a.PID, a.Started)
	}
	sum := sha256.Sum256([]byte(c.Host + "\n" + a.Kind + "\n" + id))
	return hex.EncodeToString(sum[:4])
}

func (c Caller) Encode() string {
	data, _ := json.Marshal(c)
	return base64.StdEncoding.EncodeToString(data)
}

// placeName is the name of a profile, as it appears in the name of a
// conversation.
var placeName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Decode reads a header value. Anything unreadable gives an unknown caller.
func Decode(value string) Caller {
	var c Caller
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return Caller{}
	}
	if !placeName.MatchString(c.Place) {
		return Caller{}
	}
	kept := c.Agents[:0]
	for _, a := range c.Agents {
		if a.Kind == Claude || a.Kind == Codex {
			kept = append(kept, a)
		}
	}
	c.Agents = kept
	return c
}

// process is what the walk needs to know about one process.
type process struct {
	pid, parent int
	started     int64
	exe         string
	args        []string
}

// maxDepth bounds the walk in case the parents form a loop.
const maxDepth = 64

func Detect() Caller {
	return detect(os.Getppid(), lookup, os.Getenv)
}

func detect(parent int, lookup func(int) (process, error), getenv func(string) string) Caller {
	c := Caller{Place: "local"}
	// A server names its profile to what it starts (LACIA_PROFILE).
	if profile := getenv("LACIA_PROFILE"); placeName.MatchString(profile) {
		c.Place = profile
	}
	c.Host, _ = os.Hostname()
	c.Dir, _ = os.Getwd()

	// above is the parent of the agent process found last. A process of
	// the same kind that is its direct parent (the npm wrapper around the
	// real binary, the loop that restarts a Codex app-server) is the same
	// agent, so it is not listed a second time; it is kept in Above.
	above := 0
	for pid, depth := parent, 0; pid > 1 && depth < maxDepth; depth++ {
		p, err := lookup(pid)
		if err != nil {
			break
		}
		if kind := classify(p); kind != "" {
			if n := len(c.Agents); n == 0 || c.Agents[n-1].Kind != kind || above != p.pid {
				c.Agents = append(c.Agents, Agent{Kind: kind, PID: p.pid, Started: p.started})
			} else {
				c.Agents[n-1].Above = append(c.Agents[n-1].Above, Proc{PID: p.pid, Started: p.started})
			}
			above = p.parent
		}
		pid = p.parent
	}
	// The session variables are inherited, so each belongs to the nearest
	// agent of its kind.
	sessions := map[string]string{Claude: getenv("CLAUDE_CODE_SESSION_ID"), Codex: getenv("CODEX_THREAD_ID")}
	if len(c.Agents) == 0 {
		// The processes could not be read. The variables still say which
		// agents are involved, though not in which order.
		for _, kind := range []string{Claude, Codex} {
			if sessions[kind] != "" {
				c.Agents = append(c.Agents, Agent{Kind: kind})
			}
		}
	}
	home := getenv("CODEX_HOME")
	if home != "" {
		home = filepath.Clean(home)
	}
	// The login belongs to the agent it was chosen for: the kind lacia
	// started (LACIA_LAUNCH), or Codex.
	login, launched := getenv("LACIA_LOGIN"), getenv("LACIA_LAUNCH")
	if launched == "" {
		launched = Codex
	}
	for i := range c.Agents {
		kind := c.Agents[i].Kind
		c.Agents[i].Session, sessions[kind] = sessions[kind], ""
		if kind == Codex {
			// Inherited like the session, so it belongs to the nearest Codex.
			c.Agents[i].Home, home = home, ""
		}
		if kind == launched {
			c.Agents[i].Login, login = login, ""
		}
	}
	return c
}

// interpreters run a script named by their first argument.
var interpreters = map[string]bool{"node": true, "bun": true, "deno": true}

// classify says which agent a process is, if any. It looks only at the
// program and its arguments, never at the environment of the process.
func classify(p process) string {
	names := []string{p.exe}
	if len(p.args) > 0 {
		names = append(names, p.args[0])
		if interpreters[base(p.args[0])] || interpreters[base(p.exe)] {
			for _, arg := range p.args[1:] {
				if !strings.HasPrefix(arg, "-") {
					names = append(names, arg)
					break
				}
			}
		}
	}
	for _, name := range names {
		switch b := base(name); {
		case b == "claude", strings.Contains(filepath.ToSlash(name), "/claude/versions/"):
			return Claude
		case b == "codex", strings.HasPrefix(b, "codex-"):
			return Codex
		}
	}
	return ""
}

func base(path string) string {
	b := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
	for _, ext := range []string{".js", ".mjs", ".cjs", ".exe"} {
		b = strings.TrimSuffix(b, ext)
	}
	return b
}

// Describe writes the chain for people: "local-claude（プロセス 51647）".
func (c Caller) Describe() string {
	if !c.Known() {
		return "エージェントからの呼び出しではありません（" + runtime.GOOS + "）"
	}
	var parts []string
	for _, a := range c.Agents {
		part := c.name(a)
		var details []string
		if a.PID != 0 {
			details = append(details, fmt.Sprintf("プロセス %d", a.PID))
		}
		if a.Login != "" {
			details = append(details, "ログイン "+a.Login)
		}
		if a.Session != "" {
			details = append(details, "会話 "+a.Session)
		}
		if len(details) > 0 {
			part += "（" + strings.Join(details, "、") + "）"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ← ")
}

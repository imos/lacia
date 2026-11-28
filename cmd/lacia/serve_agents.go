package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/claudecode"
	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/rules"
)

// requestRunners answer the requests of the queue. main takes what people
// ask; side takes the scheduled jobs marked "side", so a calendar fetch never
// makes a request wait. Each side run gets a fresh conversation: these jobs
// carry no memory.
type requestRunners struct {
	main, side request.Runner
	// close ends the conversations that stay open between requests.
	close func()
}

// instructionsLoader gives the instructions of a conversation, as they read
// when it starts.
type instructionsLoader func(text string) func(context.Context) (string, rules.Revision)

// startAgents prepares the conversations that answer requests. A Codex
// conversation starts keeping its connection at once.
func (s *serveRun) startAgents() (requestRunners, error) {
	executable, env, err := s.agentEnv()
	if err != nil {
		return requestRunners{}, err
	}
	instructions, err := s.agentInstructions()
	if err != nil {
		return requestRunners{}, err
	}
	currentInstructions := func(text string) func(context.Context) (string, rules.Revision) {
		return func(ctx context.Context) (string, rules.Revision) {
			current := sessionRules(ctx, s.dir, executable)
			return resolveWindowInstructions(text, s.dir, current), current.Revision
		}
	}
	mainRunners, sideRunners := map[string]request.Runner{}, map[string]request.Runner{}
	var lookupDiscordMail func(context.Context, string, string) (bool, error)
	closeAgents := func() {}
	if *s.c.str("claude") != "" {
		if err := s.addClaude(mainRunners, sideRunners, env, instructions, currentInstructions); err != nil {
			return requestRunners{}, err
		}
	}
	if *s.c.str("codex") != "" {
		if lookupDiscordMail, closeAgents, err = s.addCodex(mainRunners, sideRunners, env, instructions, currentInstructions); err != nil {
			return requestRunners{}, err
		}
	}
	defaultDestination, err := s.routing.Resolve("")
	if err != nil {
		return requestRunners{}, err
	}
	return requestRunners{
		main:  s.family.withMail(routeRunner(mainRunners, defaultDestination.Kind), lookupDiscordMail),
		side:  routeRunner(sideRunners, defaultDestination.Kind),
		close: closeAgents,
	}, nil
}

// agentEnv is what the conversations run with. Codex and its sub-agents run
// the lacia command; the environment points it at this server so their
// changes go through one place.
func (s *serveRun) agentEnv() (executable string, env []string, err error) {
	c := s.c
	_, port, _ := net.SplitHostPort(*c.str("addr"))
	env = []string{"LACIA_API=http://127.0.0.1:" + port, "LACIA_TASKS=" + s.dir, "LACIA_PROFILE=" + s.here}
	if user := *c.str("user"); user != "" {
		// The commands the conversations run use the same name.
		env = append(env, "LACIA_USER="+user)
	}
	// A Codex started under another login (codex-work, codex-personal)
	// runs on the same model.
	if model := *c.str("codex-model"); model != "" {
		env = append(env, "LACIA_CODEX_MODEL="+model)
	}
	if effort := *c.str("codex-effort"); effort != "" {
		env = append(env, "LACIA_CODEX_EFFORT="+effort)
	}
	executable, selfErr := os.Executable()
	if selfErr != nil {
		return executable, env, nil
	}
	// The commands that travel in the binary (shims.go) go beside the
	// state, and in front of PATH with the binary itself.
	shimsDir, err := s.state.MakeSub("bin", 0o755)
	if err == nil {
		err = writeShims(shimsDir, executable)
	}
	var bin string
	if err == nil {
		bin, err = filepath.Abs(shimsDir.Path())
	}
	if err != nil {
		return "", nil, fmt.Errorf("コマンドを置けませんでした: %w", err)
	}
	// LACIA_BIN is the fallback for shells that reset PATH on start.
	env = append(env, "LACIA_BIN="+executable, "PATH="+strings.Join([]string{bin, filepath.Dir(executable), os.Getenv("PATH")}, string(os.PathListSeparator)))
	// A worker started by lacia codex-login takes its Git hooks
	// from here: pre-push refuses every push.
	if os.Getenv("LACIA_WORKER_GIT_HOOKS") == "" {
		env = append(env, "LACIA_WORKER_GIT_HOOKS="+bin)
	}
	// And it may not resume the server's own conversation, whose
	// ID is kept here.
	env = append(env, "LACIA_SERVER_THREAD="+filepath.Join(s.stateDir, "codex", "thread.json"))
	return executable, env, nil
}

func (s *serveRun) agentInstructions() (string, error) {
	instructions := codex.Instructions
	if path := *s.c.str("instructions"); path != "" {
		own, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("--instructions の文書を読めませんでした: %w", err)
		}
		instructions = string(own)
	}
	if path := *s.c.str("instructions-extra"); path != "" {
		extra, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("--instructions-extra の文書を読めませんでした: %w", err)
		}
		instructions = strings.TrimRight(instructions, "\n") + "\n\n" + strings.TrimSpace(string(extra)) + "\n"
	}
	return rules.FillPeer(instructions, s.peerLabel), nil
}

func (s *serveRun) addClaude(mainRunners, sideRunners map[string]request.Runner, env []string, instructions string, currentInstructions instructionsLoader) error {
	c := s.c
	// Only the main conversation keeps its session; the side one, being
	// ephemeral, writes nothing.
	state, err := s.state.MakeSub("claude", 0o700)
	if err != nil {
		return err
	}
	claude := func(state *disk.Dir, text string, ephemeral bool) *claudecode.Runner {
		load := currentInstructions(text)
		return claudecode.New(claudecode.Config{Command: strings.Fields(*c.str("claude")), Workspace: s.dir, State: state,
			Env: env, CurrentInstructions: func(ctx context.Context) (string, rules.Revision) {
				instructions, rev := load(ctx)
				return strings.TrimRight(instructions, "\n") + "\n" + claudecode.Note, rev
			}, Permission: *c.str("claude-permission"), Model: *c.str("claude-model"), Ephemeral: ephemeral})
	}
	withDiscordContext := func(run request.Runner) request.Runner {
		return func(ctx context.Context, text string) (string, string, error) {
			r := request.Current(ctx)
			if len(r.DiscordImages) > 0 {
				text += "\nこの Claude Code の会話には、Discord の添付画像を渡せません。画像は見られなかったと伝え、本文への対応を続けてください。"
			}
			result, err := runDiscordContext(ctx, s.imageBot, r, text, false, func(ctx context.Context, text, _ string, _ []string) (codex.Result, error) {
				reply, turn, err := run(ctx, text)
				return codex.Result{Reply: reply, TurnID: turn}, err
			})
			return result.Reply, result.TurnID, err
		}
	}
	mainRunners[caller.Claude] = withDiscordContext(claude(state, instructions, false).Run)
	sideRunners[caller.Claude] = withDiscordContext(claude(nil, codex.SideInstructions, true).Run)
	return nil
}

// addCodex returns how the main conversation looks up mail for Discord, and
// what closes both conversations.
func (s *serveRun) addCodex(mainRunners, sideRunners map[string]request.Runner, env []string, instructions string, currentInstructions instructionsLoader) (func(context.Context, string, string) (bool, error), func(), error) {
	c, queue := s.c, s.queue
	mainState, err := s.state.MakeSub("codex", 0o700)
	if err != nil {
		return nil, nil, err
	}
	sideState, err := s.state.MakeSub("codex-side", 0o700)
	if err != nil {
		return nil, nil, err
	}
	model, effort, sandbox := *c.str("codex-model"), *c.str("codex-effort"), *c.str("codex-sandbox")
	codexEnv := append(append([]string(nil), env...), rulesNoticeEnvironment(s.dir)...)
	disabledApp := ""
	if s.family.Mail != nil {
		disabledApp = s.family.Mail.Connector
	}
	bridge := codex.New(codex.Config{Command: strings.Fields(*c.str("codex")), Workspace: s.dir, State: mainState, Model: model, Effort: effort, Sandbox: sandbox, Env: codexEnv, DisabledApp: disabledApp, CurrentInstructions: currentInstructions(instructions)})
	// A sub-agent finishing is told to the main conversation as a
	// notice, queued behind anything the user is waiting for.
	bridge.OnChild = func(c codex.Child) { enqueueChild(queue, c) }
	go bridge.Maintain(s.ctx)
	sideBridge := codex.New(codex.Config{Command: strings.Fields(*c.str("codex")), Workspace: s.dir, State: sideState,
		Model: model, Effort: effort, Sandbox: sandbox, Env: codexEnv, Ephemeral: true, Threads: 2, DisabledApp: disabledApp, CurrentInstructions: currentInstructions(codex.SideInstructions)})
	runner := func(b *codex.Bridge, fresh bool) request.Runner {
		return func(ctx context.Context, text string) (string, string, error) {
			res, err := runDiscordTurn(ctx, s.imageBot, request.Current(ctx), text, b.RunForImages)
			if errors.Is(err, codex.ErrDisconnected) {
				err = fmt.Errorf("%w: %v", request.ErrUncertain, err)
			}
			if fresh {
				b.Close()
			}
			return res.Reply, res.TurnID, err
		}
	}
	mainRunners[caller.Codex], sideRunners[caller.Codex] = runner(bridge, false), runner(sideBridge, true)
	return bridge.LookupMail, func() { bridge.Close(); sideBridge.Close() }, nil
}

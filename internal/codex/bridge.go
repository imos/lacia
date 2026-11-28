package codex

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/imos/lacia/internal/chat"
	"github.com/imos/lacia/internal/effect/network"
	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/task"
)

// Instructions is what the conversation is told about its role. The owner's
// name is still to be filled in where rules.OwnerMark stands.
//
//go:embed instructions.md
var Instructions string

// SideInstructions is for the second conversation, which runs scheduled
// jobs in parallel with the main one. Like Instructions, it marks the lines
// for a repository with tasks and for one without (rules.Fit).
const SideInstructions = `あなたはレイシアです。この会話では、窓口とは別に、決まった時刻に行う仕事（予約）を受け持ちます。作業ディレクトリーの AGENTS.md を読み、そこに書かれた決まりと文章の書き方に従ってください。自分についての記録「わたしのこと」（memory/self.md）があれば、あわせて読んでください。

- 今回のメッセージで依頼された仕事だけを進めます。
- 予約の内容は、ふだんの暮らしや仕事をもとに決めています。始める前に、だれが何を必要としているか、ふだんの前提がきょうも成り立つかを確かめてください。依頼に添えられた「いまの状況」や、この仕事で読んでよい予定を参考にします。
- 状況がふだんと違うときは、AGENTS.md が案内する「目的に合わせる」に従って仕事の内容を調整し、変えた点と理由を報告に1行で添えます。作業の許可、送り先、読んでよい情報の範囲は変えません。予約そのものをやめるかどうかは、自分では決めません。
<!-- tasks -->
- タスクの操作には lacia コマンドを使います。lacia が見つからないときは、環境変数 LACIA_BIN に入っているパスで実行してください。
<!-- /tasks -->
<!-- no-tasks -->
- 記録の操作には lacia コマンドを使います。この記録はタスクを持たないので、タスクは作りません。lacia が見つからないときは、環境変数 LACIA_BIN に入っているパスで実行してください。
<!-- /no-tasks -->
- 記録のリポジトリーでは、git commit や git push を実行しないでください。Git への自動保存を使う環境では、サーバーが変更を保存します。ほかのリポジトリーでの操作は、それぞれの決まりと依頼の許可に従います。
- 最後のメッセージが、結果の報告になります。伝えることがなければ、「報告はありません。」とだけ答えてください。伝えることがあるときは、結論から、自然な日本語で書いてください。`

// Config describes the Codex to run.
type Config struct {
	Command     []string // how to start Codex, e.g. ["codex"]
	Workspace   string   // the task repository; Codex works here
	StateDir    string   // where the conversation ID and the socket live
	Model       string   // empty keeps the model of Codex's own settings
	Effort      string   // reasoning effort; empty keeps Codex's own
	Env         []string // extra environment for Codex, as KEY=value
	Sandbox     string   // Codex sandbox mode; empty keeps Codex's default
	Threads     int      // main conversation plus sub-agents
	DisabledApp string   // an app reserved for a server-side tool
	Ephemeral   bool     // do not keep the conversation (for tests)
	// Instructions overrides the embedded text.
	Instructions        string
	CurrentInstructions func(context.Context) (string, rules.Revision)
}

// Child reports that a sub-agent started or stopped.
type Child struct {
	ThreadID     string
	Kind         string // started, completed, interrupted, errored, ...
	Path         string // the agent's name within the conversation, if any
	TurnID       string // the main turn during which this was observed
	RequestID    string // original request, bound when the child is observed
	OriginTurnID string // distinguishes a reused child execution
}

// Finished reports whether the sub-agent will do no more work.
func (c Child) Finished() bool {
	switch c.Kind {
	case "completed", "interrupted", "errored", "failed", "shutdown", "notFound":
		return true
	}
	return false
}

// Result is the outcome of one turn of the main conversation.
type Result struct {
	TurnID string
	Reply  string // the final message, addressed to the user
}

// ErrDisconnected means the connection dropped while a turn was running.
// The turn may or may not have finished, so the caller must not resend.
var ErrDisconnected = errors.New("Codex との接続が切れました。依頼がどこまで進んだかは分かりません")

// Bridge owns the connection and the one main conversation.
type Bridge struct {
	cfg     Config
	OnChild func(Child) // called from the reader; must not block

	setup       sync.Mutex
	mu          sync.Mutex
	client      *client
	threadID    string
	ruleVersion rules.Revision
	turns       map[string]*turn
	process     *exec.Cmd
	routes      childRoutes

	socketOnce sync.Once
	socketPath string
}

type turn struct {
	final  string
	last   string
	status string
	errMsg string
	done   chan struct{}
}

// New prepares a bridge; nothing starts until Run.
func New(cfg Config) *Bridge {
	if cfg.Threads == 0 {
		cfg.Threads = 8
	}
	if cfg.Instructions == "" {
		owner := task.OwnerName
		if cfg.Workspace != "" {
			if record, err := task.LoadConfig(cfg.Workspace); err == nil {
				owner = record.OwnerName()
			}
		}
		cfg.Instructions = chat.WithBehavior(rules.FillPeer(strings.ReplaceAll(Instructions, rules.OwnerMark, owner), ""), owner)
	}
	return &Bridge{cfg: cfg, turns: map[string]*turn{}}
}

func (b *Bridge) socket() string {
	b.socketOnce.Do(func() { b.socketPath = socketPath(b.cfg.StateDir) })
	return b.socketPath
}

// socketPath decides where the app-server listens. Codex refuses a socket
// whose directory belongs to another user. If the state directory is on a
// volume that maps ownership to another uid, use a private directory on
// local disk. Name it after the state directory so a restarted server
// finds the app-server it left running.
func socketPath(stateDir string) string {
	inState := filepath.Join(stateDir, "codex.sock")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return inState
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		return inState
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) == os.Geteuid() {
		return inState
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		abs = stateDir
	}
	sum := sha256.Sum256([]byte(abs))
	dir := filepath.Join(os.TempDir(), "lacia-codex-"+hex.EncodeToString(sum[:6]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return inState
	}
	return filepath.Join(dir, "codex.sock")
}

// ThreadID returns the main conversation's ID once connected.
func (b *Bridge) ThreadID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.threadID
}

// connect makes sure an app-server is running and the conversation is open.
// The app-server is started detached, so it and its sub-agents keep running
// when this process is replaced; the next process reconnects to it.
func (b *Bridge) connect(ctx context.Context) error {
	// Setup has its own lock: the reader takes b.mu for every notification,
	// so holding b.mu across a call would stall the reply to that call.
	b.setup.Lock()
	defer b.setup.Unlock()
	return b.connectLocked(ctx)
}

func (b *Bridge) connectLocked(ctx context.Context) error {
	b.mu.Lock()
	current := b.client
	b.mu.Unlock()
	if current != nil {
		select {
		case <-current.closed:
		default:
			return nil
		}
	}
	if err := os.MkdirAll(b.cfg.StateDir, 0o700); err != nil {
		return err
	}
	if !network.Listening(b.socket()) {
		os.Remove(b.socket())
		args := WithMemoryDefaults(append(append([]string{}, b.cfg.Command[1:]...), "app-server"))
		args = append(args, "--enable", "multi_agent", "--disable", "wake_on_agent_messages", "--listen", "unix://"+b.socket())
		cmd := exec.Command(b.cfg.Command[0], args...)
		cmd.Dir = b.cfg.Workspace
		cmd.Env = append(os.Environ(), b.cfg.Env...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		logFile, err := os.OpenFile(filepath.Join(b.cfg.StateDir, "app-server.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			logFile.Close()
			return fmt.Errorf("Codexを起動できませんでした: %w", err)
		}
		logFile.Close()
		b.process = cmd
		os.WriteFile(filepath.Join(b.cfg.StateDir, "app-server.pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
		go cmd.Wait()
		deadline := time.Now().Add(15 * time.Second)
		for {
			if network.Listening(b.socket()) {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("Codexが起動しませんでした。app-server.log を確かめてください")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	c, err := dial(ctx, b.socket(), b.notify)
	if err != nil {
		return err
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "lacia", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		c.close()
		return err
	}
	if err := c.notifyServer(ctx, "initialized"); err != nil {
		c.close()
		return err
	}
	config := map[string]any{
		"agents.max_threads":              b.cfg.Threads,
		"features.wake_on_agent_messages": false,
	}
	if b.cfg.DisabledApp != "" {
		config["apps"] = map[string]any{b.cfg.DisabledApp: map[string]bool{"enabled": false}}
	}
	var ruleVersion rules.Revision
	instructions := b.cfg.Instructions
	if b.cfg.CurrentInstructions != nil {
		instructions, ruleVersion = b.cfg.CurrentInstructions(ctx)
	}
	params := map[string]any{
		"cwd":                   b.cfg.Workspace,
		"approvalPolicy":        "never",
		"config":                config,
		"developerInstructions": instructions,
	}
	// Without these, Codex uses what its own settings say.
	if b.cfg.Model != "" {
		params["model"] = b.cfg.Model
	}
	if b.cfg.Effort != "" {
		config["model_reasoning_effort"] = b.cfg.Effort
	}
	if b.cfg.Sandbox != "" {
		params["sandbox"] = b.cfg.Sandbox
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	known, err := b.loadThread()
	if err != nil {
		// Like a missing pointer, an unreadable one cannot be resumed.
		// Keep the normal path for starting a new conversation.
		slog.Warn("reading conversation state", "error", err)
		known = threadRecord{}
	}
	resuming := known.ThreadID != "" && !b.cfg.Ephemeral
	if resuming {
		ruleVersion = known.Rules
		params["threadId"] = known.ThreadID
		params["excludeTurns"] = true // the history is not needed here and can be huge
		err = c.call(ctx, "thread/resume", params, &started)
	} else {
		params["ephemeral"] = b.cfg.Ephemeral
		err = c.call(ctx, "thread/start", params, &started)
	}
	if err != nil {
		c.close()
		return err
	}
	if !b.cfg.Ephemeral {
		known.ThreadID, known.Rules = started.Thread.ID, ruleVersion
		if err := b.writeThread(known); err != nil {
			c.close()
			return err
		}
	}
	b.mu.Lock()
	b.client, b.threadID, b.ruleVersion = c, started.Thread.ID, ruleVersion
	b.mu.Unlock()
	slog.Info("codex connected", "thread", started.Thread.ID, "resumed", resuming)
	if resuming {
		if err := b.recoverChildren(ctx, c, started.Thread.ID); err != nil {
			slog.Warn("child history recovery", "error", err)
		}
	}
	return nil
}

func (b *Bridge) turn(id string) *turn {
	t := b.turns[id]
	if t == nil {
		t = &turn{done: make(chan struct{})}
		b.turns[id] = t
	}
	return t
}

func (b *Bridge) notify(method string, raw json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Item struct {
			Type          string `json:"type"`
			Text          string `json:"text"`
			Phase         string `json:"phase"`
			Kind          string `json:"kind"`
			AgentThreadID string `json:"agentThreadId"`
			AgentPath     string `json:"agentPath"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	b.mu.Lock()
	if p.ThreadID != b.threadID {
		b.mu.Unlock()
		return
	}
	var child *Child
	switch method {
	case "item/started", "item/completed":
		switch p.Item.Type {
		case "agentMessage":
			if method == "item/completed" && p.Item.Text != "" {
				t := b.turn(p.TurnID)
				t.last = p.Item.Text
				if p.Item.Phase == "final_answer" || p.Item.Phase == "final" {
					t.final = p.Item.Text
				}
			}
		case "subAgentActivity":
			if p.Item.AgentThreadID != "" {
				child = &Child{ThreadID: p.Item.AgentThreadID, Kind: p.Item.Kind, Path: p.Item.AgentPath, TurnID: p.TurnID}
			}
		}
	case "turn/completed":
		t := b.turn(p.Turn.ID)
		t.status = p.Turn.Status
		if p.Turn.Error != nil {
			t.errMsg = p.Turn.Error.Message
		}
		select {
		case <-t.done:
		default:
			close(t.done)
		}
	}
	b.mu.Unlock()
	if child != nil {
		b.routeChild(*child)
	}
}

// Run sends one message to the main conversation and waits for the turn to
// end. Callers run one at a time; the conversation takes one turn at a time.
func (b *Bridge) Run(ctx context.Context, text string) (Result, error) {
	return b.RunFor(ctx, text, "")
}

// RunFor associates this turn and its children with a trusted request ID.
func (b *Bridge) RunFor(ctx context.Context, text, requestID string) (Result, error) {
	return b.RunForImages(ctx, text, requestID, nil)
}

// RunForImages attaches verified local images to the same user turn.
func (b *Bridge) RunForImages(ctx context.Context, text, requestID string, images []string) (Result, error) {
	if err := b.connect(ctx); err != nil {
		return Result{}, err
	}
	b.mu.Lock()
	c, threadID, previous := b.client, b.threadID, b.ruleVersion
	b.mu.Unlock()
	var changed rules.Revision
	if !b.cfg.Ephemeral && b.cfg.CurrentInstructions != nil {
		_, current := b.cfg.CurrentInstructions(ctx)
		if note := current.Notice(previous); note != "" {
			text = note + "\n\n" + text
			changed = current
		}
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	input := []map[string]any{{"type": "text", "text": text}}
	for _, path := range images {
		input = append(input, map[string]any{"type": "localImage", "path": path})
	}
	b.awaitingTurn(true)
	err := c.call(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"input":    input,
	}, &started)
	if err != nil {
		b.awaitingTurn(false)
		return Result{}, err
	}
	if err := b.bindTurn(started.Turn.ID, requestID); err != nil {
		return Result{TurnID: started.Turn.ID}, fmt.Errorf("%w: サブエージェントの結果をどの依頼に返すか、記録できませんでした: %v", ErrDisconnected, err)
	}
	if changed != nil {
		b.mu.Lock()
		if b.client == c {
			if err := b.saveThread(threadID, changed); err != nil {
				slog.Warn("saving conversation rule version", "error", err)
			}
			b.ruleVersion = changed
		}
		b.mu.Unlock()
	}
	b.mu.Lock()
	t := b.turn(started.Turn.ID)
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return Result{TurnID: started.Turn.ID}, ctx.Err()
	case <-c.closed:
		return Result{TurnID: started.Turn.ID}, ErrDisconnected
	case <-t.done:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.turns, started.Turn.ID)
	res := Result{TurnID: started.Turn.ID, Reply: strings.TrimSpace(t.final)}
	if res.Reply == "" {
		res.Reply = strings.TrimSpace(t.last)
	}
	if t.status != "completed" {
		return res, fmt.Errorf("Codexの応答が途中で終わりました（%s）。%s", t.status, t.errMsg)
	}
	return res, nil
}

// Close drops the connection. The app-server keeps running.
func (b *Bridge) Close() {
	b.mu.Lock()
	c := b.client
	b.client = nil
	b.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// Stop ends the app-server this bridge started. Sub-agents stop with it.
func (b *Bridge) Stop() {
	b.Close()
	if b.process != nil && b.process.Process != nil {
		syscall.Kill(-b.process.Process.Pid, syscall.SIGTERM)
	}
}

// StopServer ends the app-server recorded in stateDir, along with its
// sub-agents. It is for planned changes that the running Codex must pick up
// (new environment, new Codex version); a normal server restart leaves the
// app-server running on purpose.
func StopServer(stateDir string) error {
	data, err := os.ReadFile(filepath.Join(stateDir, "app-server.pid"))
	if err != nil {
		return fmt.Errorf("動いているCodexの記録がありません: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return errors.New("app-server.pid の中身が正しくありません")
	}
	// The app-server leads its own session, so the group is everything it started.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

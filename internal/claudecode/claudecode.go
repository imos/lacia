// Package claudecode hands requests to Claude Code, for a Lacia whose owner
// uses it in place of Codex.
//
// Each request is one run of `claude -p`. The main conversation is kept by
// resuming the same session on every run; Claude Code compacts it when it
// grows. Unlike the Codex bridge, nothing stays running between requests,
// so sub-agents must finish within the turn that started them.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/imos/lacia/internal/rules"
)

// Note is added to the instructions of a conversation run this way. The
// common instructions describe sub-agents that outlive a turn, which holds
// for Codex only.
const Note = `
## Claude Code で動いているとき

Claude Code は依頼ごとに起動し、返事を出すと終了します。窓口では前の会話を再開します。予約などで毎回新しい会話を使う設定のときは、その依頼専用の会話を始めます。

- サブエージェントに任せた作業は、返事を終える前に結果を受け取ってください。作業中に返事を終えると、その作業も途中で止まります。
- 完了の知らせがあとから別のメッセージで届くことはありません。受け取った結果は、今回の返事で伝えてください。
- 長くかかる作業は、区切りごとにタスクの記録へ書いてから進めます。
`

// Config describes the Claude Code to run.
type Config struct {
	Command   []string // how to start Claude Code, e.g. ["claude"]
	Workspace string   // the task repository; Claude Code works here
	StateDir  string   // where the session ID is kept
	Env       []string // extra environment, as KEY=value
	// Instructions are appended to Claude Code's own system prompt.
	Instructions        string
	CurrentInstructions func(context.Context) (string, rules.Revision)
	// Permission is the permission mode (acceptEdits, bypassPermissions,
	// ...). Empty means acceptEdits: files may be edited, and of the shell
	// only the lacia command runs unasked. The repository's own
	// .claude/settings.json can allow more.
	Permission string
	Model      string
	// Ephemeral starts a new conversation on every run.
	Ephemeral bool
}

// Runner sends requests to one conversation, one at a time.
type Runner struct {
	cfg Config
	mu  sync.Mutex
}

// New prepares a runner; nothing starts until Run.
func New(cfg Config) *Runner {
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"claude"}
	}
	if cfg.Permission == "" {
		cfg.Permission = "acceptEdits"
	}
	return &Runner{cfg: cfg}
}

func (r *Runner) sessionFile() string { return filepath.Join(r.cfg.StateDir, "session") }

// Session returns the conversation that the next run resumes, if any.
func (r *Runner) Session() string {
	if r.cfg.Ephemeral {
		return ""
	}
	data, _ := os.ReadFile(r.sessionFile())
	return strings.TrimSpace(string(data))
}

// result is the last line `claude -p --output-format json` prints.
type result struct {
	Type      string `json:"type"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	UUID      string `json:"uuid"`
}

// errNoSession means the saved session is gone, so a new one must start.
var errNoSession = errors.New("Claude Code の保存された会話が見つかりません")

// Run sends one message and waits for the reply. The second value names
// the turn, for the request's record.
func (r *Runner) Run(ctx context.Context, text string) (reply, turn string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.Session()
	instructions := r.cfg.Instructions
	var current rules.Revision
	if r.cfg.CurrentInstructions != nil {
		instructions, current = r.cfg.CurrentInstructions(ctx)
	}
	message := text
	versionFile := filepath.Join(r.cfg.StateDir, "rules.json")
	if session != "" && len(current) > 0 {
		var previous rules.Revision
		data, _ := os.ReadFile(versionFile)
		_ = json.Unmarshal(data, &previous)
		if note := current.Notice(previous); note != "" {
			message = note + "\n\n" + message
		}
	}
	res, err := r.run(ctx, message, session, instructions)
	if errors.Is(err, errNoSession) && session != "" {
		// The conversation was cleared away. Losing its memory is better
		// than refusing every request from here on.
		os.Remove(r.sessionFile())
		res, err = r.run(ctx, text, "", instructions)
	}
	if err != nil {
		return "", "", err
	}
	if !r.cfg.Ephemeral && res.SessionID != "" && res.SessionID != session {
		if err := os.MkdirAll(r.cfg.StateDir, 0o700); err == nil {
			os.WriteFile(r.sessionFile(), []byte(res.SessionID+"\n"), 0o600)
		}
	}
	if !r.cfg.Ephemeral && len(current) > 0 && res.SessionID != "" {
		if data, err := json.Marshal(current); err == nil {
			_ = os.WriteFile(versionFile, data, 0o600)
		}
	}
	return res.Result, res.UUID, nil
}

func (r *Runner) args(session, instructions string) []string {
	args := append([]string{}, r.cfg.Command[1:]...)
	args = append(args, "-p", "--output-format", "json", "--permission-mode", r.cfg.Permission, "--allowedTools", "Bash(lacia:*)")
	if instructions != "" {
		args = append(args, "--append-system-prompt", instructions)
	}
	if r.cfg.Model != "" {
		args = append(args, "--model", r.cfg.Model)
	}
	if session != "" {
		args = append(args, "--resume", session)
	}
	return args
}

func (r *Runner) run(ctx context.Context, text, session, instructions string) (result, error) {
	cmd := exec.CommandContext(ctx, r.cfg.Command[0], r.args(session, instructions)...)
	cmd.Dir = r.cfg.Workspace
	cmd.Env = WithMemoryDefault(append(os.Environ(), r.cfg.Env...))
	// The message goes in on standard input: it can be long, and arguments
	// are visible to other processes.
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return result{}, ctx.Err()
	}
	out := strings.TrimSpace(stdout.String())
	if strings.Contains(out, "No conversation found") || strings.Contains(stderr.String(), "No conversation found") {
		return result{}, errNoSession
	}
	// The result is the last line that parses; warnings may come before it.
	var res result
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if json.Unmarshal([]byte(lines[i]), &res) == nil && res.Type == "result" {
			break
		}
		res = result{}
	}
	switch {
	case res.Type != "result" && runErr != nil:
		return result{}, fmt.Errorf("Claude Code を実行できませんでした: %w: %s", runErr, tail(stderr.String()))
	case res.Type != "result":
		return result{}, fmt.Errorf("Claude Code の返事を読めませんでした: %s", tail(out))
	case res.IsError:
		return result{}, fmt.Errorf("Claude Code がエラーを返しました: %s", tail(res.Result))
	}
	return res, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 300 {
		return "…" + string(r[len(r)-300:])
	}
	return s
}

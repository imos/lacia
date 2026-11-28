package self

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
)

// CodexNight runs the night on a Codex app-server of its own, started for
// the night and stopped after it, never the one the conversations use. The
// guard matches what Claude is given: the records are only read, their
// private places not even that, nothing goes out but to the model, and
// nothing of the conversation is kept.
type CodexNight struct {
	Executable string // Codex, or a launcher that ends by exec'ing it
	Model      string // empty keeps Codex's own
	Effort     string // empty keeps Codex's own
	Root       string // the records
	// ReadRecords lets the conversation read the records, all but the
	// private places. Without it, nothing of them is readable.
	ReadRecords bool
	Private     []string // more places in the records not to read
	// Scratch is a directory lacia may write, outside the records. The
	// night works in a directory made in it and removed afterwards.
	Scratch string
}

const (
	nightProfile = "lacia-night"
	checkProfile = "lacia-night-check"
	codexScratch = "codex-night"
	probeAllowed = "lacia-night-allowed\n"
	probeDenied  = "lacia-night-denied\n"
)

// codexFeaturesOff are what the night has no use for. Some reach outside
// (connectors, plugins, the browser, sub-agents); hooks and the shell
// snapshot run the owner's own programs outside the sandbox.
var codexFeaturesOff = []string{"apps", "plugins", "image_generation", "multi_agent", "multi_agent_v2",
	"goals", "browser_use", "browser_use_external", "computer_use", "tool_suggest", "memories", "hooks", "shell_snapshot"}

// errUnguarded marks a night the guard could not be made for. The night is
// not run without it, and not tried again until the next night.
var errUnguarded = errors.New("Codex で、記録の中の読ませない場所を守る仕組みを作れませんでした。守りのないままでは動かさないので、今夜の夜の処理を止めました")

func unguarded(format string, args ...any) error {
	return fmt.Errorf("%w（%s）", errUnguarded, fmt.Sprintf(format, args...))
}

// Open starts nothing until the first prompt, as Claude does: most nights
// with nothing to digest never need the model.
func (n CodexNight) Open(ctx context.Context) (Think, func(), error) {
	var session *codexSession
	var refused error
	closeSession := func() {
		if session != nil {
			session.close()
			session = nil
		}
	}
	return func(_ context.Context, prompt string) (string, error) {
		if refused != nil {
			return "", refused
		}
		if session == nil {
			s, err := n.start(ctx)
			if errors.Is(err, errUnguarded) {
				refused = err
			}
			if err != nil {
				return "", err
			}
			session = s
			if n.ReadRecords {
				prompt = "記録のリポジトリーは " + strconv.Quote(n.Root) + " にあります。下の材料に出てくるファイルの場所は、ここからの相対パスです。いまの作業ディレクトリーは空です。\n\n" + prompt
			}
		}
		result, err := session.turn(prompt)
		if err != nil {
			// A broken conversation cannot take the next phase; the next
			// prompt starts a fresh one.
			s := session
			closeSession()
			if detail := strings.TrimSpace(s.stderr.String()); detail != "" {
				err = fmt.Errorf("%w: %s", err, clip(detail, 600))
			}
		}
		return result, err
	}, closeSession, nil
}

type codexSession struct {
	cmd     *program.Cmd
	cancel  context.CancelFunc
	input   io.WriteCloser
	events  *json.Decoder
	stderr  *bytes.Buffer
	scratch *disk.Dir
	next    int
	thread  string
	reply   string
	ended   *codexTurn
}

type codexTurn struct {
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (n CodexNight) start(ctx context.Context) (*codexSession, error) {
	// Codex reads the settings, instructions and skills of the project it
	// starts in; inside the records, the conversations could write them.
	if rel, err := filepath.Rel(n.Root, n.Scratch); err == nil && filepath.IsLocal(rel) {
		return nil, unguarded("作業用のディレクトリー %s が、記録の中にあります", n.Scratch)
	}
	scratch, err := disk.OpenTools(n.Scratch)
	if err != nil {
		return nil, err
	}
	work := filepath.Join(n.Scratch, codexScratch)
	files := map[string]string{"probe/allowed": probeAllowed, "probe/denied/canary": probeDenied, "probe/key.pem": probeDenied}
	err = scratch.RemoveAll(codexScratch) // what a night that did not end left
	if err == nil {
		err = scratch.MkdirAll(filepath.Join(codexScratch, "cwd"))
	}
	for name, text := range files {
		if err == nil {
			err = scratch.WriteFile(filepath.Join(codexScratch, name), []byte(text), 0o600)
		}
	}
	if err != nil {
		scratch.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := program.Codex(n.Executable).Command(ctx, codexArgs(work)...)
	cmd.Dir = filepath.Join(work, "cwd")
	s := &codexSession{cmd: cmd, cancel: cancel, scratch: scratch, stderr: &bytes.Buffer{}}
	cmd.Stderr = s.stderr
	if s.input, err = cmd.StdinPipe(); err == nil {
		var output io.Reader
		if output, err = cmd.StdoutPipe(); err == nil {
			s.events = json.NewDecoder(output)
			err = cmd.Start()
		}
	}
	if err != nil {
		cancel()
		scratch.RemoveAll(codexScratch)
		scratch.Close()
		return nil, err
	}
	if err := s.prepare(n, work); err != nil {
		if detail := strings.TrimSpace(s.stderr.String()); detail != "" && !errors.Is(err, errUnguarded) {
			err = fmt.Errorf("%w: %s", err, clip(detail, 600))
		}
		s.close()
		return nil, err
	}
	return s, nil
}

// codexArgs start the app-server with what can be set before it runs. The
// profile of the night itself needs where Codex is, known only once it
// runs, so it comes with the thread.
func codexArgs(work string) []string {
	probe := filepath.Join(work, "probe")
	args := []string{"app-server", "--listen", "stdio://"}
	for _, feature := range codexFeaturesOff {
		args = append(args, "--disable", feature)
	}
	return append(args,
		"-c", "permissions."+checkProfile+`={filesystem={":minimal"="read", `+strconv.Quote(probe)+`={"."="read", "denied"="deny", "**/*.pem"="deny"}}, network={enabled=false}}`,
		"-c", `web_search="disabled"`,
		// Instructions in AGENTS.md files are not the night's.
		"-c", "project_doc_max_bytes=0",
		"-c", "agents.enabled=false",
		"-c", "allow_login_shell=false",
		// The shell would otherwise see every variable of the server.
		"-c", `shell_environment_policy.inherit="core"`,
		"-c", "notify=[]",
	)
}

// codexProfile is the permission profile of the night: read the records
// but their private places, and the files Codex needs to run commands.
func codexProfile(root string, readRecords bool, private, readable []string) map[string]any {
	filesystem := map[string]any{":minimal": "read"}
	for _, dir := range readable {
		filesystem[dir] = "read"
	}
	if readRecords {
		scoped := map[string]string{".": "read"}
		for _, dir := range privateTop {
			scoped[dir] = "deny"
		}
		for _, pattern := range privateAnywhere {
			scoped["**/"+pattern] = "deny"
		}
		for _, rel := range privateRel(root, private) {
			scoped[rel] = "deny"
		}
		filesystem[root] = scoped
	}
	return map[string]any{"filesystem": filesystem, "network": map[string]any{"enabled": false}}
}

// prepare makes sure the guard holds before any prompt is sent.
func (s *codexSession) prepare(n CodexNight, work string) error {
	err := s.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "lacia-night", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": true}}, nil)
	if err == nil {
		err = s.send(map[string]string{"method": "initialized"})
	}
	if err != nil {
		return err
	}
	if err := s.checkGuard(work); err != nil {
		return err
	}
	// The sandbox runs Codex itself to start each command.
	exe, err := executableOf(s.cmd.Process.Pid)
	if err != nil {
		return unguarded("Codex の実行ファイルの場所が分かりませんでした: %v", err)
	}
	readable := []string{filepath.Dir(exe), filepath.Join(work, "cwd")}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		readable = append(readable, filepath.Dir(real))
	}

	var settings struct {
		Config struct {
			MCP map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := s.call("config/read", map[string]string{"cwd": filepath.Join(work, "cwd")}, &settings); err != nil {
		return unguarded("Codex の設定を読めませんでした: %v", err)
	}
	mcp := map[string]any{}
	for name := range settings.Config.MCP {
		mcp[name] = map[string]bool{"enabled": false}
	}
	config := map[string]any{"permissions": map[string]any{nightProfile: codexProfile(n.Root, n.ReadRecords, n.Private, readable)}, "mcp_servers": mcp}
	params := map[string]any{"cwd": filepath.Join(work, "cwd"), "ephemeral": true, "permissions": nightProfile, "approvalPolicy": "never", "config": config}
	if n.Model != "" {
		params["model"] = n.Model
	}
	if n.Effort != "" {
		config["model_reasoning_effort"] = n.Effort
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Profile *struct {
			ID string `json:"id"`
		} `json:"activePermissionProfile"`
		Instructions []string `json:"instructionSources"`
	}
	if err := s.call("thread/start", params, &started); err != nil {
		return err
	}
	s.thread = started.Thread.ID
	if started.Profile == nil || started.Profile.ID != nightProfile {
		return unguarded("会話に、夜の処理の権限のプロファイルが付きませんでした")
	}
	for _, source := range started.Instructions {
		if rel, err := filepath.Rel(n.Root, source); err == nil && filepath.IsLocal(rel) {
			return unguarded("記録の中の %s が、会話への指示として読み込まれました", rel)
		}
	}
	return s.checkTools()
}

// checkGuard runs commands under a profile like the night's, on files made
// for the purpose. Where the sandbox cannot keep a readable directory's
// part unreadable (Linux without what bubblewrap needs), it fails here.
func (s *codexSession) checkGuard(work string) error {
	probe := filepath.Join(work, "probe")
	run := func(command ...string) (int, string, error) {
		var r struct {
			ExitCode int    `json:"exitCode"`
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
		}
		err := s.call("command/exec", map[string]any{"command": command, "cwd": probe, "permissionProfile": checkProfile, "timeoutMs": 30000}, &r)
		return r.ExitCode, r.Stdout + r.Stderr, err
	}
	code, out, err := run("/bin/cat", filepath.Join(probe, "allowed"))
	if err != nil || code != 0 || out != probeAllowed {
		return unguarded("守りの中で、読んでよい試しのファイルを読めませんでした: %d %s %v", code, clip(strings.TrimSpace(out), 200), err)
	}
	for _, name := range []string{"denied/canary", "key.pem"} {
		if code, out, err := run("/bin/cat", filepath.Join(probe, name)); err == nil && (code == 0 || strings.Contains(out, probeDenied)) {
			return unguarded("読ませない試しのファイル %s を、守りの中で読めました", name)
		}
	}
	written := filepath.Join(probe, "written")
	if code, _, err := run("/bin/sh", "-c", "echo x > "+strconv.Quote(written)); err == nil && code == 0 {
		return unguarded("守りの中で、ファイルを書けました")
	}
	if _, err := os.Stat(written); err == nil {
		return unguarded("守りの中で、ファイルを書けました")
	}
	return nil
}

// checkTools makes sure no MCP server is left on the thread: one may reach
// outside, and Codex has no switch to ignore the owner's list.
func (s *codexSession) checkTools() error {
	cursor := ""
	for {
		params := map[string]any{"threadId": s.thread, "detail": "toolsAndAuthOnly"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data []struct {
				Name   string `json:"name"`
				Status string `json:"runtimeStatus"`
			} `json:"data"`
			Next string `json:"nextCursor"`
		}
		if err := s.call("mcpServerStatus/list", params, &page); err != nil {
			return unguarded("外部の道具（MCP）が止まっているかを確かめられませんでした: %v", err)
		}
		for _, server := range page.Data {
			if server.Status != "disabled" {
				return unguarded("外部の道具（MCP）の %s を止められませんでした", server.Name)
			}
		}
		if cursor = page.Next; cursor == "" {
			return nil
		}
	}
}

func (s *codexSession) close() {
	s.input.Close()
	s.cancel()
	s.cmd.Wait()
	s.scratch.RemoveAll(codexScratch)
	s.scratch.Close()
}

func (s *codexSession) send(message any) error {
	return json.NewEncoder(s.input).Encode(message)
}

func (s *codexSession) read() (rpcMessage, error) {
	var msg rpcMessage
	err := s.events.Decode(&msg)
	if errors.Is(err, io.EOF) {
		err = errors.New("Codex が返事を返す前に終了しました")
	}
	return msg, err
}

// call sends a request and handles what arrives until its answer.
func (s *codexSession) call(method string, params, result any) error {
	s.next++
	id := strconv.Itoa(s.next)
	if err := s.send(map[string]any{"id": s.next, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		msg, err := s.read()
		if err != nil {
			return err
		}
		if msg.Method != "" || string(msg.ID) != id {
			s.handle(msg)
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("Codex が %s を受け付けませんでした: %s", method, msg.Error.Message)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(msg.Result, result)
	}
}

func (s *codexSession) handle(msg rpcMessage) {
	switch {
	case msg.ID != nil && msg.Method != "":
		// Approvals and questions: the night grants nothing more.
		s.send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "夜の処理では受け付けません"}})
	case msg.Method == "item/completed":
		var p struct {
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(msg.Params, &p) == nil && p.Item.Type == "agentMessage" {
			s.reply = p.Item.Text
		}
	case msg.Method == "turn/completed":
		var p struct {
			Turn codexTurn `json:"turn"`
		}
		if json.Unmarshal(msg.Params, &p) == nil {
			s.ended = &p.Turn
		}
	}
}

func (s *codexSession) turn(prompt string) (string, error) {
	timer := time.AfterFunc(20*time.Minute, s.cancel)
	defer timer.Stop()
	s.reply, s.ended = "", nil
	input := []map[string]string{{"type": "text", "text": prompt}}
	if err := s.call("turn/start", map[string]any{"threadId": s.thread, "input": input}, nil); err != nil {
		return "", err
	}
	for s.ended == nil {
		msg, err := s.read()
		if err != nil {
			return "", err
		}
		s.handle(msg)
	}
	if s.ended.Status != "completed" || s.reply == "" {
		detail := ""
		if s.ended.Error != nil {
			detail = s.ended.Error.Message
		}
		return "", fmt.Errorf("Codex の応答を得られませんでした（%s）: %s", s.ended.Status, clip(detail, 600))
	}
	return s.reply, nil
}

// privateTop and privateAnywhere are the places of the records the night
// may not read: those at the top, and names anywhere. The instructions and
// settings for the conversations are not the night's.
var (
	privateTop      = []string{".git", ".lacia", ".claude", ".codex", ".agents", "local", "runtime", "AGENTS.md", "CLAUDE.md"}
	privateAnywhere = []string{"secrets", "secrets/**", ".env", ".env.*", "*.pem", "*.key"}
)

// privateRel are the given places that lie in the records, relative to them.
func privateRel(root string, paths []string) []string {
	var rels []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, abs)
		if err == nil && filepath.IsLocal(rel) && rel != "." && !slices.Contains(rels, filepath.ToSlash(rel)) {
			rels = append(rels, filepath.ToSlash(rel))
		}
	}
	return rels
}

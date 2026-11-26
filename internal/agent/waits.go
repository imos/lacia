package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/claudecode"
	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/wait"

	"github.com/imos/lacia/internal/task"
)

// Watcher checks jobs and files from the device that has the credentials
// for those targets. It reports what it saw to the server and hands work
// to a conversation addressed to this device. The server can run on the
// same device or somewhere else.
//
// It keeps no state of its own. Every tick it asks the server for the
// waits and looks at each target as it is now, so a sleep or a restart
// loses nothing.
type Watcher struct {
	Kubectl        string
	Base           func() string // the server's address
	Client         *http.Client
	Host           string
	Place          string // configured conversation place, local by default
	HandoverSecret func() (string, error)
	Dir            string // the task repository; continuations run here
	LogDir         string // where the output of continuations is kept
	// Runners maps codex and claude to the command that starts a new
	// conversation and reads its first message from standard input.
	// "{dir}" in an argument is replaced with Dir.
	Runners map[string][]string
	// Logins lists the ways Codex is started on this device. Which of them the
	// user runs in the task repository differs from day to day, so the
	// continuation goes to whichever conversation is running there.
	Logins []Login
	// Alive reports whether a process exists. Replaced in tests.
	Alive func(pid int) bool
	// Path is prepended to PATH for the continuations, so that they find
	// lacia and kubectl even when a menu-bar app started the agent.
	Path []string
	// Every is how often one target is looked at.
	Every time.Duration
	// Exec runs a command and returns its output and exit code. Replaced
	// in tests.
	Exec func(ctx context.Context, name string, args ...string) (stdout, stderr string, code int, err error)

	tick    sync.Mutex // held while a Tick is looking
	mu      sync.Mutex
	looked  map[string]time.Time
	posted  map[string]posted
	running map[string]bool
	awake   *exec.Cmd
}

type posted struct {
	seen, detail string
	at           time.Time
}

// Login is one way Codex is started on this device. Someone who keeps two
// accounts apart has two: by a command of their own (codex-work), by a flag
// (codex --profile work), or by a variable (env CODEX_HOME=... codex).
type Login struct {
	Name  string // for the log
	Label string // for people, such as "仕事用"
	// Command starts Codex with this login. The subcommand and its
	// arguments are appended.
	Command []string
	// Home is the directory this login keeps its settings in (CODEX_HOME).
	// A running conversation says which directory it uses, and that is how
	// its login is told. Empty when there is only one.
	Home string
	// Unattended starts a new conversation without approvals or a sandbox,
	// with the flag Codex itself has for it. A conversation nobody watches
	// cannot ask.
	Unattended bool
}

// queue hands a message to a running conversation; "--thread ID --message
// TEXT" is appended.
func (l Login) queue() []string {
	return append(append([]string{}, l.Command...), "queue")
}

// exec starts a new conversation that reads its first message from
// standard input.
func (l Login) exec() []string {
	command := codex.WithMemoryDefaults(append([]string{}, l.Command...))
	if l.Unattended {
		command = append(command, "--dangerously-bypass-approvals-and-sandbox")
	}
	return append(command, "exec", "-C", "{dir}", "-")
}

// title is how a message names the login: "Codex", or "Codex（仕事用）".
func (l Login) title() string {
	if l.Label == "" {
		return "Codex"
	}
	return "Codex（" + l.Label + "）"
}

// DefaultRunners start the installed Claude Code without a named login.
func DefaultRunners() map[string][]string {
	return map[string][]string{wait.NextClaude: {"claude", "-p"}}
}

// DefaultLogins is Codex as it is installed, with no login to choose.
func DefaultLogins() []Login {
	return []Login{{Command: []string{"codex"}}}
}

func run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), errOut.String(), exit.ExitCode(), nil
	}
	return out.String(), errOut.String(), 0, err
}

func (w *Watcher) call(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.Base()+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.HandoverSecret != nil && (req.URL.Path == "/api/handovers" || strings.HasPrefix(req.URL.Path, "/api/handovers/")) {
		secret, err := w.HandoverSecret()
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := w.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s が %d を返しました", path, resp.StatusCode)
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
}

// Tick looks at what is due and starts what is ready. It returns after the
// looking; continuations go on in the background. A Tick that comes while
// the previous one is still looking, as when kubectl hangs, does nothing.
func (w *Watcher) Tick(ctx context.Context) {
	if !w.tick.TryLock() {
		return
	}
	defer w.tick.Unlock()
	w.mu.Lock()
	if w.looked == nil {
		w.looked, w.posted, w.running = map[string]time.Time{}, map[string]posted{}, map[string]bool{}
	}
	if w.Exec == nil {
		w.Exec = run
	}
	if w.Every == 0 {
		w.Every = time.Minute
	}
	w.mu.Unlock()

	var waits []wait.Wait
	if _, err := w.call(ctx, http.MethodGet, "/api/waits", nil, &waits); err != nil {
		// Watching may be disabled while handovers remain available.
		slog.Debug("waits", "error", err)
	}
	now := time.Now()
	busy := false
	for _, item := range waits {
		switch {
		case item.State == wait.Waiting:
			busy = true
			w.mu.Lock()
			due := now.Sub(w.looked[item.ID]) >= w.Every
			if due {
				w.looked[item.ID] = now
			}
			w.mu.Unlock()
			if due {
				w.report(ctx, item, now)
			}
		}
	}
	var handovers []handover.Handover
	handoverPath := "/api/handovers"
	if w.HandoverSecret != nil {
		handoverPath += "?place=" + url.QueryEscape(w.place())
	}
	if _, err := w.call(ctx, http.MethodGet, handoverPath, nil, &handovers); err != nil {
		slog.Debug("handovers", "error", err)
	} else {
		for _, item := range handovers {
			if _, found := w.handoverKind(item); item.Runnable() && found {
				busy = true
				w.start(ctx, item)
			}
		}
	}
	w.mu.Lock()
	busy = busy || len(w.running) > 0
	w.mu.Unlock()
	w.keepAwake(busy)
}

// report looks at one target and tells the server when what it sees has
// changed, or every ten minutes so that the page shows the watch is alive.
func (w *Watcher) report(ctx context.Context, item wait.Wait, now time.Time) {
	look, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	seen, detail := w.Look(look, item)
	w.mu.Lock()
	last := w.posted[item.ID]
	w.mu.Unlock()
	if last.seen == seen && last.detail == detail && now.Sub(last.at) < 10*time.Minute {
		return
	}
	if _, err := w.call(ctx, http.MethodPost, "/api/waits/"+item.ID, map[string]string{"seen": seen, "detail": detail}, nil); err != nil {
		slog.Warn("wait: report", "id", item.ID, "error", err)
		return
	}
	slog.Info("wait seen", "id", item.ID, "task", item.TaskID, "seen", seen, "detail", detail)
	w.mu.Lock()
	w.posted[item.ID] = posted{seen, detail, now}
	w.mu.Unlock()
}

// Look says what the target of a wait is doing now.
func (w *Watcher) Look(ctx context.Context, item wait.Wait) (seen, detail string) {
	base := []string{"--context", item.Context, "-n", item.Namespace, "--request-timeout=20s"}
	if item.Kind == wait.File {
		// The file is the sign that the work ended. Its first line says
		// how: a line starting with "fail" means it ended badly.
		args := append(base, "exec", item.Name, "--", "sh", "-c", `if [ -e "$1" ]; then head -c 300 "$1"; else exit 7; fi`, "sh", item.Path)
		out, errOut, code, err := w.Exec(ctx, w.Kubectl, args...)
		switch {
		case err != nil:
			return wait.Unseen, firstLine(err.Error())
		case code == 7:
			return wait.Running, "終了の印のファイルは、まだありません"
		case code != 0:
			return wait.Unseen, firstLine(errOut)
		}
		line := firstLine(out)
		if strings.HasPrefix(strings.ToLower(line), "fail") {
			return wait.Failed, line
		}
		return wait.Succeeded, line
	}
	out, errOut, code, err := w.Exec(ctx, w.Kubectl, append(base, "get", "job", item.Name, "-o", "json")...)
	switch {
	case err != nil:
		return wait.Unseen, firstLine(err.Error())
	case code != 0 && strings.Contains(errOut, "NotFound"):
		return wait.Gone, ""
	case code != 0:
		return wait.Unseen, firstLine(errOut)
	}
	var job struct {
		Spec struct {
			Suspend bool `json:"suspend"`
		} `json:"spec"`
		Status struct {
			Active     int `json:"active"`
			Succeeded  int `json:"succeeded"`
			Failed     int `json:"failed"`
			Conditions []struct {
				Type, Status, Reason, Message string
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &job); err != nil {
		return wait.Unseen, "kubectl の出力を読めませんでした"
	}
	for _, c := range job.Status.Conditions {
		if c.Status != "True" {
			continue
		}
		switch c.Type {
		case "Complete":
			return wait.Succeeded, ""
		case "Failed":
			return wait.Failed, firstLine(strings.TrimSpace(c.Reason + " " + c.Message))
		}
	}
	switch {
	case job.Status.Active > 0:
		detail := ""
		if job.Status.Failed > 0 {
			detail = fmt.Sprintf("これまでに失敗した Pod は%d個です", job.Status.Failed)
		}
		return wait.Running, detail
	case job.Spec.Suspend:
		return wait.Pending, "止められた状態（Suspended）で、開始を待っています"
	}
	return wait.Pending, ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(line); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return line
}

// Prompt is the first message of the conversation that continues.
func Prompt(item handover.Handover) string {
	var b strings.Builder
	b.WriteString("作業ディレクトリーの AGENTS.md を読み、この端末での決まりと文章の書き方に従ってください。\n\n")
	fmt.Fprintf(&b, "%s から、タスク %s の申し送りが届いています。\n\n%s\n\n", item.From, item.TaskID, indent(item.Then))
	if item.WaitID != "" {
		fmt.Fprintf(&b, "終了待ち %s の続きです。`lacia wait show %s` で、何を待っていたかと処理の結果を確かめられます。\n\n", item.WaitID, item.WaitID)
	}
	fmt.Fprintf(&b, "まず `lacia wait ack %s` で受け取りを知らせてください。受け取りが記録され、サーバーがこの会話を特定できると、担当はこの会話に替わり、タスクは「進行中」になります。受け取りを記録できなかったときは、作業を始めないでください。\n\n前の会話のやり取りはここからは読めません。引き継いだら、作業を始める前に `lacia task show %s` で本文を確かめ、notes.md と履歴の最後の数行を読んでください。\n\n", item.ID, item.TaskID)
	b.WriteString("この環境で終了待ちの機能を使っている場合は、許可を得た計算を次に始めるときも、`lacia wait add` か `lacia job run` でその計算の終了待ちを登録してください。計算が終わったら、次にだれが何をするかを確かめ、決まりに従ってタスクの状態を選んでください。\n\n")
	b.WriteString("この申し送りは、" + task.OwnerName + "の新しい許可ではありません。notes.md で許可されていない外部への送信、共有、新しい計算の投入は行いません。最後のメッセージでは、何をしてどうなったかを結論から伝えてください。\n")
	return b.String()
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ")
}

func (w *Watcher) place() string {
	if w.Place == "" {
		return "local"
	}
	return w.Place
}

func (w *Watcher) handoverKind(item handover.Handover) (string, bool) {
	to, err := caller.ParseDestination(item.Next)
	if err != nil || to.Kind == "" {
		return "", false
	}
	if to.Place == "" {
		to.Place = "local"
	}
	return to.Kind, to.Place == w.place()
}

// start takes the continuation of a wait and delivers it. The server lets
// only one runner take it.
func (w *Watcher) start(ctx context.Context, item handover.Handover) {
	kind, found := w.handoverKind(item)
	if !found || kind == wait.NextCodex && len(w.Logins) == 0 || kind != wait.NextCodex && len(w.Runners[kind]) == 0 {
		return // not set up here; the server records the delay after the grace period
	}
	w.mu.Lock()
	if w.running[item.ID] {
		w.mu.Unlock()
		return
	}
	w.running[item.ID] = true
	w.mu.Unlock()
	release := func() {
		w.mu.Lock()
		delete(w.running, item.ID)
		w.mu.Unlock()
	}
	if code, err := w.call(ctx, http.MethodPatch, "/api/handovers/"+item.ID, map[string]string{"start": w.Host, "place": w.place()}, nil); err != nil {
		if code != http.StatusConflict {
			slog.Warn("wait: start", "id", item.ID, "error", err)
		}
		release()
		return
	}
	via := func(text string) {
		if _, err := w.call(ctx, http.MethodPatch, "/api/handovers/"+item.ID, map[string]string{"via": text}, nil); err != nil {
			slog.Warn("wait: via", "id", item.ID, "error", err)
		}
	}
	command, label := w.Runners[kind], "Claude "
	if kind == wait.NextCodex {
		// A Codex conversation is usually open in the task repository.
		// Hand the continuation to it, rather than starting another.
		login, delivered := w.queueToCodex(ctx, item)
		if delivered != "" {
			via(delivered)
			release()
			return
		}
		command, label = login.exec(), login.title()
	}
	via(fmt.Sprintf("%s で %sの新しい会話を起動します", w.Host, label))
	go func() {
		defer release()
		code, tail := w.runContinuation(ctx, item, kind, command)
		// Report the end even when the agent is being stopped.
		report, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if _, err := w.call(report, http.MethodPatch, "/api/handovers/"+item.ID, map[string]any{"end": code, "tail": tail}, nil); err != nil {
			slog.Warn("wait: end", "id", item.ID, "error", err)
		}
	}()
}

// codexSession is a Codex conversation the server has heard from.
type codexSession struct {
	label, key, thread, home string
	// pids are the processes of the conversation; it is running while any
	// of them is. last is when the server last heard from it.
	pids []int
	last time.Time
}

// running reports whether a process of the conversation is still there.
func (s codexSession) running(isAlive func(int) bool) bool {
	for _, pid := range s.pids {
		if isAlive(pid) {
			return true
		}
	}
	return false
}

// codexSessions lists the Codex conversations that ran the lacia command
// on this device inside the task repository, the most recently heard first.
func (w *Watcher) codexSessions(ctx context.Context) []codexSession {
	var rows []struct {
		Label  string    `json:"label"`
		Key    string    `json:"key"`
		Last   time.Time `json:"last"`
		Active bool      `json:"active"`
		Caller struct {
			Place  string `json:"place"`
			Host   string `json:"host"`
			Dir    string `json:"dir"`
			Agents []struct {
				Kind    string        `json:"kind"`
				PID     int           `json:"pid"`
				Session string        `json:"session"`
				Home    string        `json:"codex_home"`
				Above   []caller.Proc `json:"above"`
			} `json:"agents"`
		} `json:"caller"`
	}
	if _, err := w.call(ctx, http.MethodGet, "/api/sessions", nil, &rows); err != nil {
		slog.Warn("wait: sessions", "error", err)
		return nil
	}
	var out []codexSession
	for _, row := range rows {
		c := row.Caller
		if !row.Active || c.Place != w.place() || c.Host != w.Host || len(c.Agents) == 0 || c.Agents[0].Kind != wait.NextCodex || c.Agents[0].Session == "" {
			continue
		}
		if c.Dir != w.Dir && !strings.HasPrefix(c.Dir, strings.TrimSuffix(w.Dir, "/")+"/") {
			continue
		}
		a := c.Agents[0]
		sess := codexSession{label: row.Label, key: row.Key, thread: a.Session, home: a.Home, pids: []int{a.PID}, last: row.Last}
		// The nearest process may be replaced while the conversation goes
		// on (the Codex app-server is restarted by the loop above it when
		// it updates); the ones above it belong to the same agent.
		for _, p := range a.Above {
			sess.pids = append(sess.pids, p.PID)
		}
		out = append(out, sess)
	}
	return out
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// queueToCodex hands the continuation to a Codex conversation that is
// running in the task repository: the one that registered the wait when it
// is recently active, else the one heard from most recently. The age of the process does not
// say this: conversations served by one app-server share its process. It
// returns a sentence
// saying where the message went, or "" and the installation to start a new
// conversation with when there is no such conversation.
func (w *Watcher) queueToCodex(ctx context.Context, item handover.Handover) (Login, string) {
	isAlive := w.Alive
	if isAlive == nil {
		isAlive = alive
	}
	sessions := w.codexSessions(ctx)
	fresh := w.Logins[0]
	// A new conversation uses the login that was used last here.
	for _, sess := range sessions {
		if login, ok := w.login(sess.home); ok {
			fresh = login
			break
		}
	}
	var running []codexSession
	for _, sess := range sessions {
		if sess.running(isAlive) {
			running = append(running, sess)
		}
	}
	sort.SliceStable(running, func(i, j int) bool {
		a, b := running[i], running[j]
		if (a.key == item.FromKey) != (b.key == item.FromKey) {
			return a.key == item.FromKey
		}
		return a.last.After(b.last) // the most recently active first
	})
	if len(running) == 0 {
		return fresh, ""
	}
	sess := running[0]
	// The conversation says which login it uses. One that has not said so
	// is looked for in each: only the right one knows the thread.
	logins := w.Logins
	if login, ok := w.login(sess.home); ok {
		logins = []Login{login}
	}
	for _, login := range logins {
		queue := login.queue()
		args := append(queue[1:], "--thread", sess.thread, "--message", QueueMessage(item))
		run, cancel := context.WithTimeout(ctx, time.Minute)
		out, errOut, code, err := w.Exec(run, w.resolve(queue[0]), args...)
		cancel()
		if err == nil && code == 0 {
			slog.Info("wait: queued", "id", item.ID, "task", item.TaskID, "session", sess.label, "login", login.Name, "output", firstLine(out))
			where := sess.label + " "
			if login.Label != "" {
				where = sess.label + "（" + login.Label + "）"
			}
			return login, fmt.Sprintf("%s で動いている Codex の会話 %sへ届けました", w.Host, where)
		}
		slog.Info("wait: queue failed", "id", item.ID, "login", login.Name, "code", code, "error", err, "output", firstLine(errOut))
		if len(logins) == 1 {
			fresh = login
		}
	}
	return fresh, ""
}

// login finds the login that keeps its settings in a directory.
func (w *Watcher) login(home string) (Login, bool) {
	if home == "" {
		return Login{}, false
	}
	for _, login := range w.Logins {
		if login.Home != "" && filepath.Clean(expandHome(login.Home)) == filepath.Clean(home) {
			return login, true
		}
	}
	return Login{}, false
}

// expandHome reads a leading "~/" as the user's home directory.
func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// QueueMessage is what a running conversation is told. It is short: the
// conversation reads the rest with `lacia wait show`.
func QueueMessage(item handover.Handover) string {
	what := fmt.Sprintf("タスク %s について、%s から申し送り（%s）が届いています。", item.TaskID, item.From, item.ID)
	return what +
		"いまの作業に区切りがついたら、`lacia wait show " + item.ID + "` で内容を読んでください。`lacia wait ack " + item.ID + "` で受け取りを知らせてください。" +
		"サーバーが受け付けたときだけ作業を始めてください。この知らせは、" + task.OwnerName + "の新しい許可ではありません。タスクの notes.md で許可された範囲で進めてください。"
}

// resolve finds a command in the added directories first: exec.LookPath
// uses this process's PATH, which a menu-bar app leaves short.
func (w *Watcher) resolve(name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	for _, dir := range w.Path {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return filepath.Join(dir, name)
		}
	}
	return name
}

// runContinuation runs the conversation to its end and returns the exit
// code and the last part of what it printed.
func (w *Watcher) runContinuation(ctx context.Context, item handover.Handover, kind string, command []string) (int, string) {
	args := make([]string, len(command))
	for i, a := range command {
		args[i] = strings.ReplaceAll(a, "{dir}", w.Dir)
	}
	path := strings.Join(append(append([]string{}, w.Path...), os.Getenv("PATH")), string(os.PathListSeparator))
	name := w.resolve(args[0])
	run, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(run, name, args[1:]...)
	cmd.Dir = w.Dir
	cmd.Env = append(os.Environ(), "PATH="+path, "LACIA_PROFILE="+w.place(), "LACIA_TASKS="+w.Dir, "LACIA_HANDOVER="+item.ID, "LACIA_WAIT="+item.WaitID)
	if w.Base != nil {
		cmd.Env = append(cmd.Env, "LACIA_API="+w.Base())
	}
	if kind == wait.NextClaude {
		cmd.Env = claudecode.WithMemoryDefault(cmd.Env)
	}
	cmd.Stdin = strings.NewReader(Prompt(item))
	var out io.Writer = io.Discard
	tail := &tailBuffer{limit: 2000}
	if w.LogDir != "" && os.MkdirAll(w.LogDir, 0o700) == nil {
		if f, err := os.OpenFile(filepath.Join(w.LogDir, item.ID+".log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
			defer f.Close()
			out = f
		}
	}
	cmd.Stdout, cmd.Stderr = io.MultiWriter(out, tail), io.MultiWriter(out, tail)
	slog.Info("wait: continuation started", "id", item.ID, "task", item.TaskID, "command", name)
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		code = -1
		fmt.Fprintf(tail, "\n起動できませんでした: %v", err)
	}
	slog.Info("wait: continuation ended", "id", item.ID, "task", item.TaskID, "code", code)
	return code, tail.String()
}

// tailBuffer keeps the last bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > t.limit {
		t.data = t.data[len(t.data)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.data), "")
}

// keepAwake uses the macOS caffeinate command to hold off idle sleep
// while the watcher is busy. It does not prevent sleep when the lid closes.
func (w *Watcher) keepAwake(busy bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case busy && w.awake == nil:
		cmd := exec.Command("/usr/bin/caffeinate", "-i", "-w", fmt.Sprint(os.Getpid()))
		if err := cmd.Start(); err != nil {
			slog.Debug("caffeinate", "error", err)
			return
		}
		w.awake = cmd
		go cmd.Wait()
	case !busy && w.awake != nil:
		w.awake.Process.Kill()
		w.awake = nil
	}
}

// Stop releases the idle-sleep assertion when one is active.
func (w *Watcher) Stop() { w.keepAwake(false) }

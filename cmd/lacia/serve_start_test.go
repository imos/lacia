package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/starter"
)

// serveChild makes the test binary run `lacia <args>` instead of the tests,
// so that a server can be started and stopped by a signal in a process of
// its own.
const serveChild = "LACIA_TEST_SERVE_CHILD"

func TestMain(m *testing.M) {
	if encoded := os.Getenv(serveChild); encoded != "" {
		var args []string
		if err := json.Unmarshal([]byte(encoded), &args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := run(args); err != nil {
			fmt.Fprintln(os.Stderr, "lacia: "+err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	// The tests run with a home of their own: the settings of the machine
	// they run on (~/.lacia/config/lacia.conf names the records) must not
	// decide what a test finds. A test that runs this binary again for a
	// child of its own passes the home on, with what it sets itself.
	if os.Getenv(testHome) != "" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "lacia-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("HOME", dir)
	os.Setenv(testHome, dir)
	for _, name := range []string{"LACIA_HOME", "LACIA_CONFIG_DIR", "XDG_CONFIG_HOME", "LACIA_TASKS", "LACIA_API"} {
		os.Unsetenv(name)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// testHome marks a process whose home the tests have set.
const testHome = "LACIA_TEST_HOME"

// The settings lacia setup writes, with the first content of the records: the
// machine's file names the records, the state and the agent, and the
// records keep only what is safe to leave to them.
func TestServeStartsWithTemplateSettings(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			env := serveEnv(t)
			root := templateRecords(t, env)
			state := filepath.Join(t.TempDir(), "state")
			command := fakeAgent(t)
			config := writeFile(t, filepath.Join(t.TempDir(), "serve.conf"), fmt.Sprintf(
				"root = %s\nstate = %s\n%s = %s\ngit-sync = true\n",
				root, state, agent, command))
			addr := freeAddr(t)
			server := startServe(t, env, "serve", "--config", config, "--addr", addr)
			server.waitHealthy(t, addr)
			server.expectPage(t, addr, "/")
			server.stop(t)
			// The night runs by default; with Claude Code for the
			// conversations, on the same one. Whether a machine with
			// Codex alone has Claude Code depends on the machine.
			if agent == "claude" {
				server.expectNight(t, "毎日 04:00 に、Claude Code（"+command+"）")
			}
		})
	}
}

// The settings of a shared deployment: one file holds every profile, and
// each server is started with --profile. The two profiles here are each
// other's peer, as the task side and the chat side are. The values are made
// up; only the shape follows the deployment.
func TestServeStartsWithSharedProfiles(t *testing.T) {
	env := serveEnv(t)
	dir := t.TempDir()
	work, home := templateRecords(t, env), templateRecords(t, env)
	workAddr, homeAddr := freeAddr(t), freeAddr(t)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(dir, "secrets", "schedule-worker")
	if err := os.MkdirAll(filepath.Dir(secretFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		t.Fatal(err)
	}
	places, err := json.Marshal(map[string]any{"local": map[string]string{"device": "test-mac", "default": "codex", "secret_file": secretFile}})
	if err != nil {
		t.Fatal(err)
	}
	calendars := writeFile(t, filepath.Join(dir, "calendars.json"), `[{"account": "owner@example.com", "calendar_id": "primary", "label": "仕事"}]`)
	members := writeFile(t, filepath.Join(dir, "discord-members.json"), `{"members": [{"id": "1", "name": "試しさん", "role": "adult"}]}`)
	workExtra := writeFile(t, filepath.Join(dir, "instructions-work.md"), "仕事の側だけの決まりです。\n")
	homeExtra := writeFile(t, filepath.Join(dir, "instructions-home.md"), "家庭の側だけの決まりです。\n")
	agent := fakeAgent(t)
	config := writeFile(t, filepath.Join(dir, "lacia.conf"), strings.Join([]string{
		"state = " + filepath.Join(dir, "state"),
		"git-sync = true",
		"codex = " + agent,
		"codex-sandbox = danger-full-access",
		"codex-model = test-model",
		"codex-effort = medium",
		"people = ご家族",
		"self = memory/self.md",
		"self-model = test-model",
		"self-claude = " + agent,
		"",
		"[work]",
		"routine-explicit-matters = true",
		"label = 仕事モード",
		"url = http://" + workAddr,
		"addr = " + workAddr,
		"root = " + work,
		"site = https://lacia.example.invalid",
		"instructions-extra = " + workExtra,
		"calendars = " + calendars,
		"calendar = " + filepath.Join(work, "files", "snapshot.json"),
		"places = " + string(places),
		"notice-log = log/notices",
		"peer = home",
		"self-at = 04:00",
		"self-improve = true",
		"",
		"[home]",
		"label = 家庭モード",
		"url = http://" + homeAddr,
		"addr = " + homeAddr,
		"root = " + home,
		"instructions-extra = " + homeExtra,
		"pages = false",
		"waits = false",
		"peer = work",
		"inbox = true",
		"discord = true",
		"discord-members = " + members,
		"discord-log = log/discord",
		"notice-log = log/discord",
		"self-at = 03:30",
		"",
	}, "\n"))
	// Both profiles share one state directory in the file; each server
	// needs its own, as each Pod has its own volume.
	workServer := startServe(t, env, "serve", "--config", config, "--profile", "work", "--state", filepath.Join(dir, "work-state"))
	// Discord needs a token and the network, which a test has neither of;
	// the rest of the chat side's settings are kept.
	homeServer := startServe(t, env, "serve", "--config", config, "--profile", "home", "--state", filepath.Join(dir, "home-state"), "--discord=false")
	workServer.waitHealthy(t, workAddr)
	homeServer.waitHealthy(t, homeAddr)
	workServer.expectPage(t, workAddr, "/")
	homeServer.stop(t)
	workServer.stop(t)
	// The deployment names the times and the command of the night, so a
	// change of the defaults leaves them as they were.
	workServer.expectNight(t, "毎日 04:00 に、Claude Code（"+agent+"）")
	homeServer.expectNight(t, "毎日 03:30 に、Claude Code（"+agent+"）")
}

// expectNight finds what the server said at start about the nightly rewrite.
func (p *serveProcess) expectNight(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(p.output(), want) {
		t.Fatalf("the server did not say %q\n%s", want, p.output())
	}
}

type serveProcess struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	log    bytes.Buffer
	exited chan struct{}
	err    error
}

func (p *serveProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.log.Write(b)
}

func (p *serveProcess) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.log.String()
}

func startServe(t *testing.T, env []string, args ...string) *serveProcess {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	p := &serveProcess{exited: make(chan struct{})}
	p.cmd = exec.Command(os.Args[0])
	p.cmd.Env = append(append([]string(nil), env...), serveChild+"="+string(encoded))
	p.cmd.Stdout, p.cmd.Stderr = p, p
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			p.cmd.Process.Kill()
			<-p.exited
		}
	})
	return p
}

func (p *serveProcess) waitHealthy(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			t.Fatalf("the server exited before it answered: %v\n%s", p.err, p.output())
		default:
		}
		if body, status, err := get("http://" + addr + "/healthz"); err == nil && status == http.StatusOK && strings.TrimSpace(body) == "ok" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the server at %s did not answer within 30 seconds\n%s", addr, p.output())
}

func (p *serveProcess) expectPage(t *testing.T, addr, path string) {
	t.Helper()
	body, status, err := get("http://" + addr + path)
	if err != nil || status != http.StatusOK || !strings.Contains(body, "<html") {
		t.Fatalf("GET %s = %d, %v\n%.500s\n%s", path, status, err, body, p.output())
	}
}

// stop sends what a Pod or launchd sends, and expects a clean exit.
func (p *serveProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("the server did not stop within 30 seconds\n%s", p.output())
	}
	if p.err != nil {
		t.Fatalf("the server stopped with %v\n%s", p.err, p.output())
	}
	if !strings.Contains(p.output(), "msg=serving") {
		t.Fatalf("the server never logged that it was serving\n%s", p.output())
	}
	// A setting the binary no longer knows is only warned about at start,
	// so a deployment would keep running without it; here it fails.
	for _, bad := range []string{"level=ERROR", "msg=\"config: unknown setting skipped\"", "msg=\"config: setting not allowed in this file skipped\""} {
		if strings.Contains(p.output(), bad) {
			t.Fatalf("the server logged %s\n%s", bad, p.output())
		}
	}
}

func get(url string) (string, int, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode, err
}

// serveEnv is the environment of the servers: the variables of a Lacia
// that may run on the machine are dropped, and home is a fresh directory,
// so that nothing of the machine's own setup is read or written.
func serveEnv(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "LACIA_") || strings.HasPrefix(name, "DISCORD_") || strings.HasPrefix(name, "GIT_") ||
			name == "HOME" || name == "XDG_CONFIG_HOME" || name == "CLAUDE_CONFIG_DIR" || name == "CODEX_HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
}

// templateRecords makes records with the first content lacia setup writes, committed
// and pushed to a bare repository, so that saving to Git works offline.
func templateRecords(t *testing.T, env []string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "records")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	records, err := disk.OpenRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	if err := starter.Write(records, "オーナーさん"); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(root, "init", "-q", "-b", "main")
	git(root, "add", ".")
	git(root, "commit", "-q", "-m", "init")
	git(root, "init", "-q", "--bare", remote)
	git(root, "remote", "add", "origin", remote)
	git(root, "push", "-q", "-u", "origin", "main")
	return root
}

// fakeAgent stands for Codex and Claude Code. It is never asked anything
// during the test; a server that tries to connect sees it fail at once.
func fakeAgent(t *testing.T) string {
	t.Helper()
	path := writeFile(t, filepath.Join(t.TempDir(), "agent"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

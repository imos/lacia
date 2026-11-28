package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/rules"
)

// fake writes a stand-in for the claude command. It records its arguments
// and standard input, and answers as the script says.
func fake(t *testing.T, body string) (command, dir string) {
	t.Helper()
	dir = t.TempDir()
	command = filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + dir + "/args\ncat >> " + dir + "/stdin\n" + body + "\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return command, dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestRunKeepsOneConversation(t *testing.T) {
	command, dir := fake(t, `echo '{"type":"result","is_error":false,"result":"はい。","session_id":"s-1","uuid":"u-1"}'`)
	state := filepath.Join(dir, "state")
	r := New(Config{Command: []string{command}, Workspace: dir, StateDir: state, Instructions: "指示"})

	reply, turn, err := r.Run(context.Background(), "こんにちは")
	if err != nil || reply != "はい。" || turn != "u-1" {
		t.Fatalf("reply=%q turn=%q err=%v", reply, turn, err)
	}
	if r.Session() != "s-1" {
		t.Fatalf("session = %q", r.Session())
	}
	if _, _, err := r.Run(context.Background(), "続きです"); err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(read(t, dir+"/args")), "\n")
	if strings.Contains(args[0], "--resume") || !strings.HasSuffix(args[1], "--resume s-1") {
		t.Errorf("the second run must resume the first:\n%s", strings.Join(args, "\n"))
	}
	for _, want := range []string{"-p --output-format json", "--permission-mode acceptEdits", "--allowedTools Bash(lacia:*)", "--append-system-prompt 指示"} {
		if !strings.Contains(args[0], want) {
			t.Errorf("arguments lack %q: %s", want, args[0])
		}
	}
	if got := read(t, dir+"/stdin"); got != "こんにちは続きです" {
		t.Errorf("the messages must go in on standard input: %q", got)
	}
}

func TestRunStartsAgainWhenTheSessionIsGone(t *testing.T) {
	command, dir := fake(t, `case "$*" in *--resume*) echo "No conversation found with session ID: old";; *) echo '{"type":"result","result":"新しく始めました。","session_id":"s-2"}';; esac`)
	state := filepath.Join(dir, "state")
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, "session"), []byte("old\n"), 0o600)
	r := New(Config{Command: []string{command}, Workspace: dir, StateDir: state})
	reply, _, err := r.Run(context.Background(), "こんにちは")
	if err != nil || reply != "新しく始めました。" || r.Session() != "s-2" {
		t.Fatalf("reply=%q session=%q err=%v", reply, r.Session(), err)
	}
}

func TestRunReportsFailures(t *testing.T) {
	command, dir := fake(t, `echo '{"type":"result","is_error":true,"result":"利用の上限に達しました"}'`)
	r := New(Config{Command: []string{command}, Workspace: dir, StateDir: dir, Ephemeral: true})
	if _, _, err := r.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "利用の上限") {
		t.Errorf("err = %v", err)
	}
	command, dir = fake(t, `echo "boom" >&2; exit 3`)
	r = New(Config{Command: []string{command}, Workspace: dir, StateDir: dir, Ephemeral: true})
	if _, _, err := r.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if r.Session() != "" {
		t.Error("an ephemeral runner keeps no session")
	}
}

func TestRunReloadsRulesBetweenRequests(t *testing.T) {
	command, dir := fake(t, `echo '{"type":"result","result":"はい。","session_id":"s-1"}'`)
	state := filepath.Join(dir, "state")
	current := "版一"
	config := Config{Command: []string{command}, Workspace: dir, StateDir: state, CurrentInstructions: func(context.Context) (string, rules.Revision) {
		return current, rules.Revision{"core": current}
	}}
	for i, text := range []string{"最初。", "更新後。", "再開後。"} {
		if i == 1 {
			current = "版二"
		}
		// A replacement server reopens the same Claude session.
		if _, _, err := New(config).Run(context.Background(), text); err != nil {
			t.Fatal(err)
		}
	}
	input := read(t, filepath.Join(dir, "stdin"))
	if count := strings.Count(input, "決まりの版が変わりました"); count != 1 || !strings.HasPrefix(input, "最初。決まりの版が変わりました") {
		t.Fatalf("unexpected delivery: %q", input)
	}
	if args := read(t, filepath.Join(dir, "args")); strings.Count(args, "--append-system-prompt 版二") != 2 {
		t.Fatalf("old instructions reused: %q", args)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/schedule"
)

func TestAgentUsesTheConfiguredScheduleLoginAndConversation(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	logfile := filepath.Join(t.TempDir(), "starts")
	t.Setenv("SCHEDULE_TEST_STARTS", logfile)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	if err := os.Unsetenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY"); err != nil {
		t.Fatal(err)
	}
	command := func(name, body string) []string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
		return []string{path}
	}
	codexCommand := command("codex", `
	cat >/dev/null
	printf '%s:%s:%s:%s\n' "$LACIA_PROFILE" "$LACIA_LOGIN" "$LACIA_SCHEDULE_CONVERSATION" "$LACIA_SCHEDULE_RUN" >>"$SCHEDULE_TEST_STARTS"
	while [ "$#" -gt 0 ]; do
		case "$1" in
			--output-last-message) out=$2; shift ;;
			features.memories=false) memory=disabled ;;
			memories.generate_memories=false) generation=disabled ;;
			resume) echo resume >>"$SCHEDULE_TEST_STARTS" ;;
			--ephemeral) echo ephemeral >>"$SCHEDULE_TEST_STARTS" ;;
		esac
		shift
	done
	[ "$memory" = disabled ] && [ "$generation" = disabled ] || exit 1
	printf '%s\n' '{"type":"thread.started","thread_id":"11111111-2222-3333-4444-555555555555"}'
	printf 'codex reply' >"$out"
	`)
	claudeCommand := command("claude", `
	cat >/dev/null
	printf '%s:%s:%s:%s\n' "$LACIA_PROFILE" "$LACIA_LOGIN" "$LACIA_SCHEDULE_CONVERSATION" "$LACIA_SCHEDULE_RUN" >>"$SCHEDULE_TEST_STARTS"
	printf "claude-memory:%s\n" "$CLAUDE_CODE_DISABLE_AUTO_MEMORY" >>"$SCHEDULE_TEST_STARTS"
	for arg in "$@"; do [ "$arg" = --resume ] && echo claude-resume >>"$SCHEDULE_TEST_STARTS"; done
	printf '%s\n' '{"type":"result","session_id":"fake-claude-session","result":"claude reply"}'
	`)
	runners := scheduleConversations{root: root, state: testDir(t, state), base: localSite, place: "local", logins: &login.List{
		Default: "one",
		Logins: map[string]login.Entry{
			"one": {Codex: codexCommand},
			"two": {Codex: codexCommand, Claude: claudeCommand},
		},
		Schedule: map[string]login.ScheduleLogins{"codex": {Side: "two"}},
	}}
	run := func(id, kind string, side bool) {
		t.Helper()
		e := schedule.Execution{ID: id, To: "example-" + kind, Kind: kind, Job: schedule.Job{Side: side}}
		reply, err := runners.run(context.Background(), e, "fixture prompt")
		if err != nil || reply != kind+" reply" {
			t.Fatalf("%s: reply %q, error %v", id, reply, err)
		}
	}
	run("codex-1", "codex", false)
	run("codex-2", "codex", false)
	run("codex-3", "codex", true)
	run("claude-1", "claude", false)
	run("claude-2", "claude", false)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0")
	run("claude-3", "claude", true)
	got, err := os.ReadFile(logfile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"example:one:main:codex-1", "example:one:main:codex-2", "resume",
		"example:two:side:codex-3", "ephemeral",
		"example:two:main:claude-1", "claude-memory:1",
		"example:two:main:claude-2", "claude-memory:1", "claude-resume",
		"example:two:side:claude-3", "claude-memory:0",
	}
	if string(got) != strings.Join(want, "\n")+"\n" {
		t.Fatalf("unexpected starts:\n%s", got)
	}
}

func TestProfileSelectionKeepsConversationStateSeparate(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	script := filepath.Join(t.TempDir(), "runner")
	body := `#!/bin/sh
cat >/dev/null
for arg in "$@"; do
 [ "$arg" = resume ] && exit 99
 done
while [ "$#" -gt 0 ]; do
 case "$1" in --output-last-message) out=$2; shift ;; esac
 shift
done
printf '%s\n' '{"type":"thread.started","thread_id":"11111111-2222-3333-4444-555555555555"}'
printf '%s' "$LACIA_EXECUTION_PROFILE" >"$out"
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	r := scheduleConversations{root: root, state: testDir(t, state), place: "local", logins: &login.List{
		Default: "work", Logins: map[string]login.Entry{"work": {Codex: []string{script}}},
		ExecutionProfiles: map[string]login.ExecutionProfile{"restricted": {Login: "work", Kind: "codex"}},
	}}
	e := schedule.Execution{ID: "one", Kind: "codex"}
	if _, err := r.run(context.Background(), e, "prompt"); err != nil {
		t.Fatal(err)
	}
	e.Job.Profile = "restricted"
	if reply, err := r.run(context.Background(), e, "prompt"); err != nil || reply != "restricted" {
		t.Fatalf("reply %q error %v", reply, err)
	}
	profile, name, err := r.selection(e)
	if err != nil || profile != "restricted" || name != "work" {
		t.Fatalf("selection %s %s %v", profile, name, err)
	}
	e.Job.Profile = "missing"
	if _, err := r.run(context.Background(), e, "prompt"); err == nil {
		t.Fatal("unknown profile fell back")
	}
}

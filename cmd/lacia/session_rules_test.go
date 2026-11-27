package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/rules"
)

func TestSessionRulesFollowExecutableAndFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LACIA_USER", "")
	if err := os.MkdirAll(filepath.Join(root, ".lacia"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"user":"予備さん"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("lacia rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "lacia")
	replace := func(body string) {
		t.Helper()
		replacement := executable + ".next"
		if err := os.WriteFile(replacement, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, executable); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	// A trusted replacement is launched from the selected record root.
	replace(`[ -f "$LACIA_TASKS/AGENTS.md" ] && [ -f ./AGENTS.md ] || exit 1
[ "$1" = rules ] && [ "$2" = --session-json ] || exit 1
echo '{"core":"FIRST","behavior":"BEHAVIOR","revision":{"core":"one"}}'`)
	first := sessionRules(ctx, root, executable)
	if got := resolveWindowInstructions("HEAD", root, first); !strings.Contains(got, "FIRST") || !strings.Contains(got, "BEHAVIOR") {
		t.Fatalf("replacement missing from startup: %.160s", got)
	}
	replace(`echo '{"core":"SECOND","behavior":"BEHAVIOR","revision":{"core":"two"}}'`)
	second := sessionRules(ctx, root, executable)
	if second.Core != "SECOND" || !strings.Contains(second.Revision.Notice(first.Revision), "core") {
		t.Fatalf("replacement: %+v", second)
	}
	replace("exit 1\n")
	fallback := sessionRules(ctx, root, executable)
	if !strings.Contains(fallback.Core, "レイシアの決まり") || fallback.Behavior == "" {
		t.Fatal("cannot read the embedded fallback")
	}
	got := resolveWindowInstructions("HEAD", root, fallback)
	if strings.Count(got, fallback.Behavior) != 1 || !strings.Contains(fallback.Behavior, "予備さん") ||
		strings.Contains(got, rules.OwnerMark) || strings.Contains(got, rules.DefaultOwner) {
		t.Fatal("the fallback must include exactly one copy of the rendered policy")
	}
	if note := fallback.Revision.Notice(second.Revision); note != "" {
		t.Fatalf("temporary child failure must not announce a rollback: %q", note)
	}
	replace(`echo '{"core":"FIRST","behavior":"BEHAVIOR","revision":{"core":"one"}}'`)
	if note := sessionRules(ctx, root, executable).Revision.Notice(second.Revision); note == "" {
		t.Fatal("an actual rollback must be announced")
	}
}

func TestRulesNoticeFollowsConversationVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notices", "conversation.json")
	one := rules.Revision{"core": "one"}
	two := rules.Revision{"core": "two"}
	var stderr bytes.Buffer
	for _, current := range []rules.Revision{one, two, two, one, one} {
		writeRulesNotice(path, one, current, &stderr)
	}
	if got := strings.Count(stderr.String(), "決まりの版が変わりました"); got != 2 {
		t.Fatalf("wanted one notice for the change and one for the rollback, got %d: %q", got, stderr.String())
	}
}

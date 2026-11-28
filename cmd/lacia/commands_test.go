package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestCommandTable(t *testing.T) {
	help := helpText()
	seen := map[string]bool{}
	for _, c := range commandTable() {
		if c.name == "" || seen[c.name] || c.run == nil {
			t.Errorf("command %q is unnamed, repeated or does nothing", c.name)
		}
		seen[c.name] = true
		if found, ok := findCommand(c.name); !ok || found.name != c.name {
			t.Errorf("%q is not dispatched", c.name)
		}
		// A command is listed in lacia help exactly when it has a group.
		if (len(c.uses) == 0) != (c.group == "") || c.group != "" && !slices.Contains(commandGroups, c.group) {
			t.Errorf("%q: group %q with %d uses", c.name, c.group, len(c.uses))
		}
		for _, u := range c.uses {
			if head := strings.TrimSpace(c.name + " " + u.args); !strings.Contains(help, "\n  "+head) {
				t.Errorf("lacia help lacks %q", head)
			}
		}
	}
	for _, name := range []string{"codex-work", "claude-personal"} {
		if _, ok := findCommand(name); !ok {
			t.Errorf("%q, a login, is not dispatched", name)
		}
	}
	for _, name := range []string{"codex-", "nothing", ""} {
		if _, ok := findCommand(name); ok {
			t.Errorf("%q is dispatched", name)
		}
	}
	if err := run([]string{"nothing"}); err == nil || !strings.Contains(err.Error(), "lacia help") {
		t.Errorf("an unknown command: %v", err)
	}
	if got := usageError("peer").Error(); !strings.HasPrefix(got, "使い方:\n  lacia peer tell ") || !strings.Contains(got, "\n  lacia peer status ") {
		t.Errorf("usage of peer = %q", got)
	}
	if got := strings.Join(subcommands("self"), " "); got != "show tidy improve sleep dates" {
		t.Errorf("subcommands of self = %q", got)
	}
}

// documentedCommand finds `lacia <command> <word>` in the documents.
var documentedCommand = regexp.MustCompile("`lacia ([a-z][a-z0-9-]*)(?: ([a-z][a-z-]*))?")

// The documents name only commands the table has, with the words that may
// follow them, and the README lists every command lacia help lists.
func TestDocumentsNameTheCommands(t *testing.T) {
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range documentedCommand.FindAllStringSubmatch(string(text), -1) {
			name, word := m[1], m[2]
			c, ok := findCommand(name)
			if !ok && name != "help" && name != "codex-" && name != "claude-" {
				t.Errorf("%s: lacia %s is not a command", path, name)
				continue
			}
			if subs := subcommands(c.name); word != "" && len(subs) > 0 && !slices.Contains(subs, word) {
				t.Errorf("%s: lacia %s %s is not a form of the command (%v)", path, name, word, subs)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, _ := strings.Cut(string(readme), "\n## コマンド\n")
	section, _, _ = strings.Cut(section, "\n## ")
	for _, c := range commandTable() {
		if c.group != "" && !strings.Contains(section, "`lacia "+c.name) && !strings.Contains(section, "`"+c.name+"`") {
			t.Errorf("the commands of README.md lack %s", c.name)
		}
	}
}

// makeRecords makes the smallest records findRoot recognizes.
func makeRecords(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".lacia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, task.ConfigFile), []byte(`{"format": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRootOutsideTheRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"LACIA_HOME", "LACIA_TASKS", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	records := makeRecords(t, filepath.Join(home, "records"))
	other := makeRecords(t, filepath.Join(home, "other"))
	outside := filepath.Join(home, "code")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(outside)
	if _, err := findRoot(""); err == nil || !strings.Contains(err.Error(), "lacia.conf") {
		t.Errorf("without a root: %v", err)
	}

	// A profile's root is for its server; the common one is the machine's.
	conf := filepath.Join(home, ".lacia", "config", "lacia.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("addr = 127.0.0.1:10148\nroot = ~/records\n\n[home]\nroot = "+other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := findRoot(""); err != nil || got != records {
		t.Errorf("outside the records: %q, %v; want %q", got, err, records)
	}
	t.Chdir(filepath.Join(other, ".lacia"))
	if got, err := findRoot(""); err != nil || got != other {
		t.Errorf("inside other records: %q, %v; want %q", got, err, other)
	}
	t.Setenv("LACIA_TASKS", "~/records")
	if got, err := findRoot(""); err != nil || got != records {
		t.Errorf("named by LACIA_TASKS: %q, %v; want %q", got, err, records)
	}
}

func TestOpenPage(t *testing.T) {
	t.Setenv("LACIA_API", "http://127.0.0.1:18190/")
	for args, want := range map[string]string{"": "http://127.0.0.1:18190/", "T1271": "http://127.0.0.1:18190/tasks/T1271"} {
		if got, err := openPage(strings.Fields(args)); err != nil || got != want {
			t.Errorf("lacia open %s: %q, %v; want %q", args, got, err, want)
		}
	}
	for _, args := range [][]string{{"-h"}, {"T1", "T2"}} {
		if _, err := openPage(args); err == nil || !strings.HasPrefix(err.Error(), "使い方:") {
			t.Errorf("lacia open %v: %v", args, err)
		}
	}
}

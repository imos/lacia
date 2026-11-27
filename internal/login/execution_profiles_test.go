package login

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExecutionProfilesSelectOnlyLocalRegistration(t *testing.T) {
	l := &List{Default: "work", Logins: map[string]Entry{"work": {Codex: []string{"codex-work"}}}, ExecutionProfiles: map[string]ExecutionProfile{"cluster-read": {Login: "work", Kind: "codex", Args: []string{"--add-dir", "/local/kube"}}}}
	name, command, err := l.ExecutionCommand("cluster-read", "codex", true)
	if err != nil || name != "work" || !reflect.DeepEqual(command, []string{"codex-work", "--add-dir", "/local/kube"}) {
		t.Fatalf("%q %v %v", name, command, err)
	}
	name, command, err = l.ExecutionCommand("", "codex", true)
	if err != nil || name != "work" || !reflect.DeepEqual(command, []string{"codex-work"}) {
		t.Fatalf("default changed: %v %v", command, err)
	}
	for _, profile := range []string{"missing", "../cluster-read", "cluster-read --yolo", "default"} {
		if _, _, err := l.ExecutionCommand(profile, "codex", false); err == nil {
			t.Fatalf("accepted %q", profile)
		}
	}
	if _, _, err := l.ExecutionCommand("cluster-read", "claude", false); err == nil {
		t.Fatal("accepted wrong kind")
	}
	if len(l.Logins["work"].Codex) != 1 {
		t.Fatal("mutated login command")
	}
}

func TestLoadExecutionProfilesValidation(t *testing.T) {
	for _, data := range []string{
		`{"logins":{"work":{"codex":["codex-work"]}},"execution_profiles":{"bad name":{"login":"work","kind":"codex"}}}`,
		`{"logins":{"work":{"codex":["codex-work"]}},"execution_profiles":{"cluster":{"login":"missing","kind":"codex"}}}`,
		`{"logins":{"work":{"codex":["codex-work"]}},"execution_profiles":{"cluster":{"login":"work","kind":"shell"}}}`,
	} {
		path := filepath.Join(t.TempDir(), "logins.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadList(path); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestCommandDirsUseOnlyAbsoluteConfiguredLaunchers(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	tools := filepath.Join(root, "tools")
	other := filepath.Join(root, "other")
	list := &List{
		Default: "work",
		Logins: map[string]Entry{
			"work": {
				Codex:  []string{filepath.Join(work, "codex")},
				Claude: []string{filepath.Join(tools, "claude")},
			},
			"another": {
				Codex:  []string{filepath.Join(other, "codex")},
				Claude: []string{filepath.Join(work, "claude")},
			},
			"relative": {
				Codex:  []string{"codex", "--cwd", filepath.Join(root, "argument")},
				Claude: []string{"bin/claude"},
			},
		},
	}
	if got, want := list.CommandDirs(), []string{work, tools, other}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CommandDirs = %q, want %q", got, want)
	}
	if got := (&List{}).CommandDirs(); len(got) != 0 {
		t.Fatalf("CommandDirs without a configuration = %q", got)
	}
}

package program

import (
	"path/filepath"
	"testing"
)

// The list is what lacia may start, so a change to it shows here.
func TestList(t *testing.T) {
	system := map[string]Program{
		"/usr/bin/caffeinate":   Caffeinate(),
		"/bin/launchctl":        Launchctl(),
		"/usr/bin/xcode-select": XcodeSelect(),
		"/usr/bin/open":         Open(),
		"/usr/bin/curl":         Curl(),
		"/bin/sh":               Sh(),
		"/usr/bin/make":         Make(),
		"/usr/bin/python3":      Python3(),
	}
	for want, p := range system {
		if p.Name() != want || !filepath.IsAbs(p.Name()) {
			t.Errorf("system tool %q, want the absolute path %q", p.Name(), want)
		}
	}
	if Git().Name() != "git" {
		t.Errorf("Git() = %q, want git from PATH", Git().Name())
	}
	for _, configured := range []func(string) Program{Kubectl, Codex, ClaudeCode, AuthCheck, Lacia} {
		if got := configured("/opt/bin/tool").Name(); got != "/opt/bin/tool" {
			t.Errorf("configured program = %q, want the path given", got)
		}
	}
}

func TestShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if Shell().Name() != "/bin/zsh" {
		t.Errorf("Shell() = %q, want $SHELL", Shell().Name())
	}
	// A relative $SHELL would be looked up in PATH, so it is not used.
	t.Setenv("SHELL", "zsh")
	if Shell().Name() != "/bin/sh" {
		t.Errorf("Shell() with a relative $SHELL = %q, want /bin/sh", Shell().Name())
	}
}

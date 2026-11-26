package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRevisionIncludesOverridesButNotMemory(t *testing.T) {
	for _, profile := range []string{"work", "home"} {
		t.Run(profile, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, text string) {
				t.Helper()
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("AGENTS.md", "lacia rules")
			write("rules/local.md", "# "+profile+" local")
			version := func() SessionRules {
				t.Helper()
				current, err := ReadSessionRules(root, "")
				if err != nil {
					t.Fatal(err)
				}
				return current
			}
			started := version()
			if !strings.Contains(started.Core, profile+" local") {
				t.Fatal("local rules not loaded")
			}
			write("memory/now.md", "different context")
			write("memory/self.md", "different memory")
			if version().Revision.Notice(started.Revision) != "" {
				t.Fatal("memory is not a change to the rules")
			}
			write("rules/voice.local.md", "# New voice appendix")
			if note := version().Revision.Notice(started.Revision); !strings.Contains(note, "voice") || strings.Contains(note, "core") {
				t.Fatalf("wrong changes: %q", note)
			}
		})
	}
}

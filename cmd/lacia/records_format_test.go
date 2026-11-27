package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestLegacyRecordsStopBeforeStartingOrWriting(t *testing.T) {
	for _, fixture := range []struct {
		format string
		config string
	}{
		{"形式0", `{"format":0}`},
		{"形式1", `{"format":1}`},
	} {
		t.Run(fixture.format, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("LACIA_TASKS", root)
			t.Setenv("LACIA_API", "")
			path := filepath.Join(root, task.ConfigFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(fixture.config), 0o600); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "state")
			for _, args := range [][]string{
				{"serve", "--root", root, "--state", state, "--addr", "127.0.0.1:0"},
				{"codex"},
				{"init", "--user", "田中さん"},
				{"task", "new", "--area", "work", "--title", "試し"},
				{"project", "note", "sample", "試し"},
				{"todo"},
			} {
				err := run(args)
				if err == nil || !strings.Contains(err.Error(), fixture.format+"です") || !strings.Contains(err.Error(), "lacia migrate --dry-run") || !strings.Contains(err.Error(), "形式2") {
					t.Errorf("%v: migration guidance = %v", args, err)
				}
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != fixture.config {
				t.Errorf("config changed: %q, %v", got, err)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
				t.Errorf("records changed: %v, %v", entries, err)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Errorf("server state was created: %v", err)
			}
		})
	}
}

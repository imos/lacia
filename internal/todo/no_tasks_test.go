package todo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imos/lacia/internal/task"
)

func TestNoListWithoutTasks(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.MkdirAll(filepath.Join(root, "areas", "family", "tasks"), 0o755)
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format": 1, "areas": ["family"], "tasks": false}`), 0o644)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(store, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "TODO.md")); !os.IsNotExist(err) {
		t.Errorf("TODO.md was written: %v", err)
	}
}

// Package testrecords prepares repository fixtures for integration tests.
package testrecords

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/imos/lacia/internal/effect/disk"
)

// Init makes a test repository use format 2, preserving explicit settings.
func Init(t testing.TB, root string) string {
	t.Helper()
	path := filepath.Join(root, ".lacia", "config.json")
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, ok := settings["format"]; ok {
		return root
	}
	settings["format"] = 2
	data, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, filepath.Join(".lacia", "config.json"), data)
	return root
}

// write replaces a file in the records at root.
func write(t testing.TB, root, name string, data []byte) {
	t.Helper()
	records, err := disk.OpenRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	if err := records.WriteFileNoSync(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

var (
	statusLine = regexp.MustCompile(`(?m)^status: .*$`)
	ownerLines = regexp.MustCompile(`(?m)^(owner|owner_session|done): .*\n`)
)

// WaitWithoutCondition rewrites the status in a task.md to "waiting",
// the way a record written before a waiting task needed a condition still
// reads. The store refuses to make such a task; reload it after this.
func WaitWithoutCondition(t testing.TB, root, dir string) {
	t.Helper()
	name := filepath.Join(filepath.FromSlash(dir), "task.md")
	path := filepath.Join(root, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !statusLine.Match(data) {
		t.Fatalf("%s has no status", path)
	}
	data = ownerLines.ReplaceAll(statusLine.ReplaceAll(data, []byte("status: waiting")), nil)
	write(t, root, name, data)
}

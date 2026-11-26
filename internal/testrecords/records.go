// Package testrecords prepares repository fixtures for integration tests.
package testrecords

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Init makes a test repository use format 1, preserving explicit settings.
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
	settings["format"] = 1
	data, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

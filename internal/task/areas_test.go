package task

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAreaLabelsAndHandoffArea(t *testing.T) {
	open := func(config string) *Store {
		root := t.TempDir()
		if config == "" {
			config = `{"format":2}`
		}
		if config != "" {
			os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
			os.WriteFile(filepath.Join(root, ConfigFile), []byte(config), 0o644)
		}
		s, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	// The list of names alone is read as before; an area without a label
	// is shown by its name.
	s := open(`{"format":2,"areas":["work","personal","family"],"area_labels":{"work":"仕事","family":"家族"}}`)
	if s.AreaLabel("work") != "仕事" || s.AreaLabel("personal") != "personal" || s.AreaLabel("family") != "家族" {
		t.Errorf("labels = %q %q %q", s.AreaLabel("work"), s.AreaLabel("personal"), s.AreaLabel("family"))
	}
	// Handed-over tasks go where the repository says; without a word, to
	// the first area.
	for config, want := range map[string]string{
		`{"format":2,"areas":["work","personal","family"]}`:                       "work",
		`{"format":2,"areas":["work","personal","family"],"handoff_area":"work"}`: "work",
		`{"format":2,"areas":["job","private"]}`:                                  "job",
		``:                                                                        "work",
	} {
		if got := open(config).HandoffArea(); got != want {
			t.Errorf("%s: handoff area = %q, want %q", config, got, want)
		}
	}
}

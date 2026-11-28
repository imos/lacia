package starter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/effect/disk"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	records, err := disk.OpenRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	if err := Write(records, `田中"さん`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".lacia", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		User   string
		Format int
	}
	if err := json.Unmarshal(data, &config); err != nil || config.User != `田中"さん` || config.Format != 2 {
		t.Errorf("config.json = %s (%v)", data, err)
	}
	profile, _ := os.ReadFile(filepath.Join(dir, "memory", "profile.md"))
	if !strings.Contains(string(profile), "\n- 呼び名：田中\"さん\n") {
		t.Errorf("profile lacks the name:\n%s", profile)
	}
	for _, name := range []string{".gitignore", "AGENTS.md", "CLAUDE.md", "areas/work/tasks/.gitkeep", "areas/personal/routines/S1016/routine.md", "rules/local.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The tools of the template stay out of the records.
	for _, name := range []string{"Makefile", "LICENSE", "tools", "LACIA_SOURCE"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was written", name)
		}
	}
}

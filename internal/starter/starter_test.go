package starter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/schedule"
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
	for _, name := range []string{".gitignore", "AGENTS.md", "CLAUDE.md", "areas/work/tasks/.gitkeep", "rules/local.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The routines that come with the records take S0001 to S0999, leaving
	// S1001 onward to the owner (docs/design/records.md).
	var got []string
	for _, job := range schedule.Load(dir) {
		if job.Err != "" {
			t.Errorf("%s: %s", job.Path, job.Err)
		}
		got = append(got, job.ID+" "+job.Daily+" "+strconv.FormatBool(job.Enabled))
	}
	if want := []string{"S0001 09:00 false", "S0002 10:00 true"}; !slices.Equal(got, want) {
		t.Errorf("routines = %q, want %q", got, want)
	}
	if report, _ := os.ReadFile(filepath.Join(dir, "areas", "personal", "routines", "S0002", "routine.md")); !strings.Contains(string(report), "lacia run report S0002 ") {
		t.Errorf("S0002 reports under another id:\n%s", report)
	}
	// The tools of the template stay out of the records.
	for _, name := range []string{"Makefile", "LICENSE", "tools", "LACIA_SOURCE"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was written", name)
		}
	}
}

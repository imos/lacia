package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putDefinition(t *testing.T, root, path, body string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// newRecords is a repository of the latest format whose area work holds
// the routines ids.
func newRecords(t *testing.T, ids ...string) string {
	t.Helper()
	root := t.TempDir()
	putDefinition(t, root, ".lacia/config.json", `{"format":2,"areas":["work"]}`)
	for _, id := range ids {
		putDefinition(t, root, "areas/work/routines/"+id+"/routine.md", "---\nid: "+id+"\ndaily: 09:00\n---\n確認します。\n")
	}
	return root
}

func TestRoutineLoaderAndAlias(t *testing.T) {
	root := newRecords(t)
	putDefinition(t, root, "areas/work/routines/S1001/routine.md", "---\nid: S1001\ntitle: 新しい名前です\narea: work\noutput: matters\nlegacy_name: github\ndaily: 09:00\nenabled: false\n---\n確認します。")
	jobs := Load(root)
	if len(jobs) != 1 {
		t.Fatalf("duplicate schedule: %+v", jobs)
	}
	j := jobs[0]
	if j.Err != "" || j.Enabled || j.ID != "S1001" || j.Name != "S1001" || j.Area != "work" || j.LegacyName != "github" || j.Output != "matters" {
		t.Fatalf("job: %+v", j)
	}
	if j.Path != filepath.Join("areas", "work", "routines", "S1001", "routine.md") {
		t.Fatal(j.Path)
	}
	if got, ok := Resolve(root, "github"); !ok || got.ID != "S1001" {
		t.Fatalf("alias: %+v %v", got, ok)
	}
	if _, ok := Resolve(root, "../S1001"); ok {
		t.Fatal("accepted unknown path")
	}
}

func TestBrokenReplacementDoesNotRunOldSchedule(t *testing.T) {
	for _, extra := range []string{"id: S1002\n", "id: S1001\noutput: invalid\n", "id: S1001\n"} {
		t.Run(strings.ReplaceAll(extra, "\n", ";"), func(t *testing.T) {
			root := newRecords(t)
			putDefinition(t, root, "schedules/github.md", "---\ndaily: 09:00\n---\n旧定義です。")
			timing := "daily: 09:00\n"
			if extra == "id: S1001\n" {
				timing = "every: 7m\n"
			}
			putDefinition(t, root, "areas/work/routines/S1001/routine.md", "---\n"+extra+"legacy_name: github\n"+timing+"---\n確認します。")
			jobs := Load(root)
			if len(jobs) != 2 {
				t.Fatalf("jobs: %+v", jobs)
			}
			for _, j := range jobs {
				if j.Err == "" || j.Enabled {
					t.Fatalf("unsafe fallback: %+v", j)
				}
			}
		})
	}
}

func TestDuplicateRoutineAliasIsDisabled(t *testing.T) {
	root := newRecords(t)
	for _, id := range []string{"S1001", "S1002"} {
		putDefinition(t, root, "areas/work/routines/"+id+"/routine.md", "---\nid: "+id+"\nlegacy_name: github\ndaily: 09:00\n---\n確認します。")
	}
	for _, j := range Load(root) {
		if j.Err == "" || j.Enabled {
			t.Fatalf("duplicate alias enabled: %+v", j)
		}
	}
}

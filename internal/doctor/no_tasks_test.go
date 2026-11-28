package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

// What does not fit a repository that keeps no tasks is pointed out, and so
// is a core of its own, which the embedded rules no longer reach.
func TestRepositoryWithoutTasks(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(task.ConfigFile, `{"format": 2, "areas": ["family"], "id_prefix": "F"}`)
	os.MkdirAll(filepath.Join(root, "areas", "family", "tasks"), 0o755)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	open, _ := store.Create(task.NewTask{Area: "family", Title: "閉じていないタスク"})
	closed, _ := store.Create(task.NewTask{Area: "family", Title: "済んだタスク", Status: task.Done})
	write("areas/family/routines/S1001/routine.md", "---\nid: S1001\ntitle: 見守り\narea: family\noutput: task\nevery: 1h\n---\n見守ります。\n")
	write("TODO.md", "# やること\n")
	write("rules/core.md", "# 自分の決まり\n")

	notes := strings.Join(Check(root, false).Notes, "\n")
	if strings.Contains(notes, "TODO.md は、消して") || strings.Contains(notes, "S1001 は output: task") || strings.Contains(notes, open.ID+" は閉じていない") {
		t.Errorf("a repository with tasks was told about them:\n%s", notes)
	}
	if !strings.Contains(notes, "rules/core.md が、実行ファイルの決まり") {
		t.Errorf("the replaced core was not pointed out:\n%s", notes)
	}

	write(task.ConfigFile, `{"format": 2, "areas": ["family"], "id_prefix": "F", "tasks": false}`)
	report := Check(root, false)
	notes = strings.Join(report.Notes, "\n")
	for _, want := range []string{"TODO.md は、消して", "S1001 は output: task", open.ID + " は閉じていない"} {
		if !strings.Contains(notes, want) {
			t.Errorf("missing %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, closed.ID+" は閉じていない") {
		t.Errorf("a closed task was pointed out:\n%s", notes)
	}
}

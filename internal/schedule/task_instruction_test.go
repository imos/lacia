package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskInstructionOnlyWhenTheRoutineHasATask(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "areas", "work", "routines", "S1011")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "S1011", Name: "S1011", Path: "areas/work/routines/S1011/routine.md"}
	if got := job.TaskInstruction(root, false); got != "" {
		t.Fatalf("without task.md: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("---\nid: S1011\nstatus: waiting\ncreated: 2026-11-27\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := job.TaskInstruction(root, false)
	for _, want := range []string{"areas/work/routines/S1011/task.md", "lacia task show S1011", "lacia task write S1011 task.md", "lacia task note S1011", "受付に保存します", "「伝え方」に従います"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	collected := job.TaskInstruction(root, true)
	if !strings.Contains(collected, "サーバーが本文の「実行から分かった対応」へ集めます") || strings.Contains(collected, "タスクの本文は自動では変わりません") {
		t.Errorf("collected: %q", collected)
	}
	if legacy := (Job{Name: "github", Path: "schedules/github.md"}).TaskInstruction(root, false); legacy != "" {
		t.Errorf("legacy schedule: %q", legacy)
	}
}

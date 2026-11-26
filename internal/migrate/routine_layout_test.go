package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/schedule"
)

func TestAreaRoutineMigrationPreservesHistoryAndUndo(t *testing.T) {
	root := t.TempDir()
	run(t, root, "init", "-q")
	run(t, root, "config", "user.email", "test@example.com")
	run(t, root, "config", "user.name", "Test")
	put(t, root, ".lacia/config.json", "{\"format\": 1, \"areas\": [\"work\", \"personal\"]}\n")
	put(t, root, ".gitignore", "/local/\n/routines/*/files/\n")
	put(t, root, "areas/personal/knowledge/read.md", "# 読むもの\n")
	const definition = "---\nid: S1001\ntitle: 定期確認\narea: personal\nenabled: false\ndaily: 12:30\n---\n[読むもの](../../areas/personal/knowledge/read.md)\n"
	put(t, root, "routines/S1001/routine.md", definition)
	put(t, root, "routines/S1001/task.md", "本文と[図](artifacts/figure.md)\n")
	put(t, root, "routines/S1001/artifacts/figure.md", "図の説明\n")
	put(t, root, "routines/S1001/runs/2026-11/reviews.jsonl", "unchanged review history\n")
	put(t, root, "routines/S1001/runs/2026-11/runs.jsonl", "unchanged run history\n")
	put(t, root, "routines/S1001/files/local.txt", "Git外の添付\n")
	put(t, root, "routines/migration-map.json", "{}\n")
	put(t, root, "README.md", "[定期確認](routines/S1001/routine.md)\n\n`routines/S1001/scripts/run.py`\n")
	run(t, root, "add", ".")
	run(t, root, "commit", "-qm", "before")
	plan, err := Make(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, root, "status", "--short"); got != "" {
		t.Fatalf("planning changed files: %s", got)
	}
	if err := plan.Run(time.Now()); err != nil {
		t.Fatal(err)
	}
	const into = "areas/personal/routines/S1001/"
	want := map[string]string{
		into + "routine.md": strings.Replace(strings.Replace(definition, "area: personal\n", "", 1), "../../areas/personal/knowledge/read.md", "../../knowledge/read.md", 1),
		into + "task.md":    "本文と[図](artifacts/figure.md)\n", into + "artifacts/figure.md": "図の説明\n",
		into + "runs/2026-11/reviews.jsonl": "unchanged review history\n", into + "runs/2026-11/runs.jsonl": "unchanged run history\n",
		into + "files/local.txt": "Git外の添付\n", "archive/routine-migration-map.json": "{}\n",
		".gitignore": "/local/\n/areas/*/routines/*/files/\n",
		"README.md":  "[定期確認](areas/personal/routines/S1001/routine.md)\n\n`areas/personal/routines/S1001/scripts/run.py`\n",
	}
	for filename, expected := range want {
		data, err := os.ReadFile(filepath.Join(root, filename))
		if err != nil || string(data) != expected {
			t.Errorf("%s = %q, %v; want %q", filename, data, err, expected)
		}
	}
	if exists(root, "routines") {
		t.Error("the old routine directory remains")
	}
	jobs := schedule.Load(root)
	if len(jobs) != 1 || jobs[0].ID != "S1001" || jobs[0].Area != "personal" || jobs[0].Enabled || jobs[0].Daily != "12:30" || jobs[0].Err != "" {
		t.Errorf("unexpected routines: %+v", jobs)
	}
	put(t, root, ".git/lacia-server", "http://127.0.0.1:1\n")
	if count, err := Undo(root); count != 0 || err == nil || !strings.Contains(err.Error(), "サーバー") {
		t.Fatalf("undo while serving: %d, %v", count, err)
	}
	if err := os.Remove(filepath.Join(root, ".git", "lacia-server")); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(root); err != nil {
		t.Fatal(err)
	}
	if got := run(t, root, "status", "--short"); got != "" {
		t.Errorf("undo did not restore the checkout: %s", got)
	}
	if data, err := os.ReadFile(filepath.Join(root, "routines/S1001/files/local.txt")); err != nil || string(data) != "Git外の添付\n" {
		t.Errorf("undo did not restore the local attachment: %q %v", data, err)
	}
}

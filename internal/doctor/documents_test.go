package doctor

import (
	"strings"
	"testing"
)

func TestDocumentWarningsStayAdvisoryAndSkipClosedAndArchived(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lacia/config.json", `{"format":2,"areas":["work"]}`)
	write(t, root, ".gitignore", "/local/\n/.lacia/source/\n/areas/*/tasks/*/*/files/\n/areas/*/routines/*/files/\n")
	write(t, root, "areas/work/tasks/2026-11/T1001/task.md", strings.Replace(taskMD, "title: 試し", "title: 試し\ncustom: value\nx_custom: value", 1))
	write(t, root, "areas/work/tasks/2026-11/T1001/notes.md", "# 補足\n\n## 今回\n")
	write(t, root, "areas/work/tasks/2026-11/T1002/task.md", strings.ReplaceAll(strings.ReplaceAll(taskMD, "T1001", "T1002"), "status: todo", "status: done"))
	write(t, root, "areas/work/tasks/2026-11/T1002/notes.md", "# 補足\n\n## 今回\n")
	write(t, root, "areas/work/knowledge/source.md", "---\ntitle: 知識\n---\n本文です。\n")
	write(t, root, "areas/work/procedures/tool/procedure.md", "---\ntitle: 手順\nwhen: 使うとき\nupdated: 2026-11-24\n---\n本文です。\n")
	write(t, root, "archive/areas/work/knowledge/old.md", "# 古い文書\n")
	report := Check(root, false)
	if len(report.Errors) != 0 {
		t.Fatal(report.Errors)
	}
	notes := strings.Join(report.Notes, "\n")
	for _, want := range []string{"custom", "T1001/notes.md", "when", "updated", "大まかな流れ"} {
		if !strings.Contains(notes, want) {
			t.Errorf("missing %s: %s", want, notes)
		}
	}
	for _, excluded := range []string{"x_custom", "T1002/notes.md", "old.md"} {
		if strings.Contains(notes, excluded) {
			t.Errorf("unexpected %s: %s", excluded, notes)
		}
	}
}

func TestTaskReasonAdvice(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lacia/config.json", `{"format":2,"areas":["work"]}`)
	write(t, root, ".gitignore", "/local/\n/.lacia/source/\n/areas/*/tasks/*/*/files/\n/areas/*/routines/*/files/\n")
	for _, text := range []string{"困りました。", "困りました。調べます。直します。伝えます。"} {
		body := strings.Replace(taskMD, "## 次にすること", "## このタスクを作ったわけ\n\n"+text+"\n\n## 次にすること", 1)
		write(t, root, "areas/work/tasks/2026-11/T1001/task.md", body)
		report := Check(root, false)
		if len(report.Errors) != 0 {
			t.Fatal(report.Errors)
		}
		if want := strings.Contains(text, "伝えます。"); strings.Contains(strings.Join(report.Notes, "\n"), "「このタスクを作ったわけ」が3文") != want {
			t.Fatalf("%q: %v", text, report.Notes)
		}
	}
}

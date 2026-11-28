package task

import (
	"fmt"
	"strings"
	"testing"
)

func TestContentAdviceBoundaries(t *testing.T) {
	for _, count := range []int{140, 141} {
		body := "## ひとことで\n\n1文です。2文です。3文です。\n\n## 次にすること\n\n" + strings.Repeat("あ", count) + "\n"
		if got := LintContent(body); len(got) != count-140 {
			t.Fatalf("%d characters: %v", count, got)
		}
	}
	body := "## ひとことで\n\n1。2。3。4。\n\n## いまの資料\n\n"
	for i := 0; i < 13; i++ {
		body += fmt.Sprintf("- [今回の資料%d](file%d.md)\n", i, i)
	}
	if got := LintContent(body); len(got) != 15 {
		t.Fatalf("warnings=%v", got)
	}
	if got := LintContent("## いまの資料\n\n```\n[最新](sample)\n```\n"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestRelatedDocumentAdvice(t *testing.T) {
	cases := []struct{ kind, source, want string }{
		{"notes", "# 補足\n\n## 今回 2026-11-24\n", "先頭"},
		{"notes", "# 補足\n\n## ここまでできたら完了\n\n## してよいことと、お尋ねすること\n\n### 2026-11-24\n", "日付"},
		{"notes", "# 補足\n" + strings.Repeat("行\n", 60), "60行"},
		{"project", "---\ntitle: 計画\nstatus: active\n---\n\n## 進め方\n\n- 2026-11-24に決めました。\n", "日付から"},
		{"schedule", "---\ntitle: 予約\ntask: T1001\ndone: 2026-11-24\n---\n\n## めざすこと\n\n確かめます。\n\n## ふだんの状況\n\n東京です。\n\n## すること\n\n" + strings.Repeat("手順です。\n", 6), "5行"},
		{"knowledge", "---\ntitle: 知識\nwhen: 調べるとき\nupdated: 2026-11-24\nextra: yes\nx_custom: ok\n---\n本文です。", "extra"},
		{"procedures", "---\ntitle: 手順\nwhen: 使うとき\nupdated: 2026-11-24\n---\n本文です。", "大まかな流れ"},
	}
	for _, c := range cases {
		got := LintDocument(c.kind, c.source)
		if !strings.Contains(fmt.Sprint(got), c.want) {
			t.Errorf("%s: %v", c.kind, got)
		}
		if strings.Contains(fmt.Sprint(got), "x_custom") {
			t.Fatal(got)
		}
	}
	good := "---\ntitle: 知識\nwhen: 調べるとき\nupdated: 2026-11-24\nx_custom: ok\n---\n本文です。"
	if got := LintDocument("knowledge", good); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestContentAdviceDoesNotBlockSave(t *testing.T) {
	s := newLayoutStore(t)
	task, _ := s.Create(NewTask{Area: "work", Title: "助言"})
	body := "## ひとことで\n\n1。2。3。4。\n\n## 次にすること\n\n" + strings.Repeat("あ", 141) + "\n"
	if len(Lint(body)) < 2 {
		t.Fatal("missing advice")
	}
	if _, err := s.WriteDoc(task.ID, TaskFile, body, "test"); err != nil {
		t.Fatal(err)
	}
	notes := "# 補足\n\n## 今回 2026-11-24\n\n" + strings.Repeat("行\n", 61)
	if _, err := s.WriteDoc(task.ID, NotesFile, notes, "test"); err != nil {
		t.Fatal(err)
	}
}

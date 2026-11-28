package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

const taskMD = "---\nid: T1001\ntitle: 試し\nstatus: todo\ncreated: 2026-11-24\n---\n\n## ひとことで\n\n試します。\n\n## 次にすること\n\nレイシアが進めます。\n"

func TestCleanRepository(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lacia/config.json", `{"format": 2, "areas": ["work"]}`)
	write(t, root, ".gitignore", "/local/\n/.lacia/source/\n/areas/*/tasks/*/*/files/\n/areas/*/routines/*/files/\n")
	write(t, root, "AGENTS.md", "[タスク](areas/work/tasks/2026-11/T1001/task.md)\n")
	write(t, root, "areas/work/tasks/2026-11/T1001/task.md", taskMD)
	write(t, root, "areas/work/tasks/2026-11/T1001/log.jsonl", `{"at":"2026-11-24T09:00:00+09:00","type":"created","text":"作りました。"}`+"\n")
	write(t, root, "areas/work/projects/alpha/project.md", "---\ntitle: アルファ\n---\n")
	r := Check(root, true)
	if len(r.Errors) != 0 || len(r.Notes) != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestDesignLinks(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lacia/config.json", `{"format": 2}`)
	write(t, root, "README.md", "[一覧](/design/) [有効](/design/rules#記録の側での上書き) [無効](/design/missing) [外](https://example.com/design/missing)\n")
	r := Check(root, true)
	if notes := strings.Join(r.Notes, "\n"); r.Broken != 1 || !strings.Contains(notes, "README.md:1 /design/missing") {
		t.Fatalf("broken: %d, notes: %s", r.Broken, notes)
	}
}

func TestProblems(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lacia/config.json", `{"format": 2, "areas": ["work", "memory", "gone"]}`)
	write(t, root, "work/README.md", "top level area\n")
	write(t, root, "areas/work/tasks/2026-11/T1001/task.md", taskMD)
	write(t, root, "areas/work/tasks/2026-11/T1001/log.md", "# 履歴\n")
	write(t, root, "areas/work/tasks/2026-11/T1001/memo.txt", "x\n")
	write(t, root, "areas/work/tasks/2026-11/T1001/log.jsonl", "{\"at\":\"x\",\"type\":\"note\"}\nnot json\n{\"type\":\"note\"}\n")
	write(t, root, "areas/work/projects/old/notes.md", "以前の資料\n")
	write(t, root, "areas/memory/.keep", "")
	write(t, root, "areas/extra/.keep", "")
	write(t, root, "schedules/broken.md", "no front matter\n")
	write(t, root, "scripts/run.sh", "")
	write(t, root, "rules/local.md", "[ない](missing.md) [ある](local.md) [外](https://example.com)\n```\n[囲み](none.md)\n```\n")
	write(t, root, "archive/old.md", "[ない](missing.md)\n")
	r := Check(root, true)
	text := strings.Join(r.Errors, "\n")
	for _, want := range []string{
		"領域の名前 memory は",
		"最上位に、領域と同じ名前のディレクトリー work",
		"areas/gone がありません",
		"areas/extra は、設定の areas にない領域",
		"T1001/log.md が残っています",
		"T1001/memo.txt は",
		"log.jsonl の2行目を、JSON として読めません",
		"log.jsonl の3行目に、at か type がありません",
		"areas/work/projects/old には project.md がありません",
		"予約 broken の定義を読めません",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no error containing %q in:\n%s", want, text)
		}
	}
	notes := strings.Join(r.Notes, "\n")
	for _, want := range []string{"scripts", ".gitignore に、次の行がありません: /local/、/.lacia/source/、/areas/*/tasks/*/*/files/", "rules/local.md:1 missing.md"} {
		if !strings.Contains(notes, want) {
			t.Errorf("no note containing %q in:\n%s", want, notes)
		}
	}
	if r.Broken != 1 {
		t.Errorf("broken = %d:\n%s", r.Broken, notes)
	}
}

// A repository of an older format is only told how to move to the current
// one: its shape is not checked against rules it was not written for.
func TestOldFormat(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lacia/config.json", `{"areas": ["work"]}`)
	write(t, root, "work/tasks/2026-11/T1001/task.md", taskMD)
	write(t, root, "work/tasks/2026-11/T1001/log.md", "# T1001 の履歴\n")
	write(t, root, "work/projects/old/notes.md", "以前の資料\n")
	write(t, root, "scripts/run.sh", "")
	if r := Check(root, true); len(r.Errors) != 0 || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "形式0です") || !strings.Contains(r.Notes[0], "lacia migrate") {
		t.Errorf("report = %+v", r)
	}
}

func TestAreaRoutineDocument(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lacia/config.json", `{"format": 2, "areas": ["work"], "tasks": false}`)
	write(t, root, ".gitignore", "/local/\n/.lacia/source/\n/areas/*/tasks/*/*/files/\n/areas/*/routines/*/files/\n")
	const routine = "areas/work/routines/S1001"
	write(t, root, routine+"/routine.md", "---\nid: S1001\nlegacy_name: previous\noutput: none\nweekdays: Mon\nprofile: cluster-read\ndaily: 09:00\n---\n\n## めざすこと\n\n朝の状況を確かめます。\n")
	write(t, root, routine+"/procedure.md", "この定期タスクで使う手順です。\n")
	write(t, root, "areas/work/memo.md", "あとで調べることです。\n")
	write(t, root, "scripts/shared.sh", "")
	r := Check(root, false)
	if len(r.Errors) != 0 || len(r.Notes) != 2 {
		t.Fatalf("report = %+v", r)
	}
	notes := strings.Join(r.Notes, "\n")
	for _, want := range []string{routine + "/routine.md:", "「ふだんの状況」がありません。", "scripts"} {
		if !strings.Contains(notes, want) {
			t.Errorf("no note containing %q in:\n%s", want, notes)
		}
	}
}

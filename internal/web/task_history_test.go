package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/task"
)

func TestTaskHistoryViews(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0755)
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format":1,"areas":["work"]}`), 0644)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create(task.NewTask{Area: "work", Title: "履歴の表示", Status: task.Review})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, item.Dir)
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("---\nid: "+item.ID+"\ntitle: 履歴の表示\nstatus: review\ncreated: 2026-11-24\n---\n\n## ひとことで\n\n履歴をたどれます。\n\n## 次にすること\n\nオーナーさんに結果を見ていただきます。\n\n## いまの資料\n\n[現資料](artifacts/current.md)\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "artifacts"), 0755)
	for name, body := range map[string]string{"current.md": "# 新しい結果\n", "old.md": "# 前の結果\n", "unknown.md": "# 日時のない結果\n"} {
		os.WriteFile(filepath.Join(dir, "artifacts", name), []byte(body), 0644)
	}
	os.MkdirAll(filepath.Join(dir, "files"), 0755)
	os.WriteFile(filepath.Join(dir, "files", "large.md"), []byte("# 大きな結果\n"), 0644)
	events := []task.Event{
		{Type: task.EventCreated, Status: "todo", Text: "作りました。"},
		{Type: task.EventFile, Action: "put", Path: "artifacts/old.md", Text: "前の結果を置きました。"},
		{Type: task.EventRequest, Request: "R1", Text: "依頼を受けました。"},
		{Type: task.EventHandover, Body: "[前の資料](artifacts/old.md) と [現資料](artifacts/current.md)\n\n<script>危険</script>"},
		{Type: task.EventRequest, Request: "R2", Text: "次の依頼を受けました。"},
		{Type: task.EventFile, Action: "put", Path: "artifacts/current.md", Text: "新しい結果を置きました。"},
		{Type: task.EventFile, Action: "put", Path: "files/large.md", Text: "大きな結果を置きました。"},
		{Type: task.EventHandover, Body: "[以前の資料](artifacts/old.md)"},
		{Type: task.EventStatus, From: "doing", To: "review", Text: "確認待ちになりました。"},
	}
	os.WriteFile(filepath.Join(dir, task.HistoryFile), nil, 0644)
	for i, e := range events {
		if err := task.AppendEvent(filepath.Join(dir, task.HistoryFile), time.Date(2026, 11, 24, 9, i, 0, 0, task.JST), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	h, err := (&Server{Store: store}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	code, page, _ := get(t, h, "/tasks/"+item.ID)
	for _, want := range []string{"これまでの資料", "最後に載っていた日時 2026-11-24 09:07", "確認待ちになった日時 2026-11-24 09:08", "このときの本文", "2026-11-24 09:04 の依頼", "新しい結果", "大きな結果", "日時不明", `value="large.md"`, ">今回</span>"} {
		if code != 200 || !strings.Contains(page, want) {
			t.Errorf("task lacks %q (%d)", want, code)
		}
	}
	if strings.Count(page, "最後に載っていた日時") != 1 {
		t.Error("past materials did not deduplicate or exclude current links")
	}
	if strings.Index(page, "新しい結果</a>") > strings.Index(page, "前の結果</a>") {
		t.Error("artifacts are not newest first")
	}
	if strings.Count(page, ">今回</span>") != 2 {
		t.Error("current request marker includes older or undated files")
	}
	if strings.Contains(page, "<script>危険</script>") {
		t.Error("snapshot contains unsafe HTML")
	}
	code, page, _ = get(t, h, "/tasks/"+item.ID+"/log")
	if code != 200 || !strings.Contains(page, "このときの本文") || !strings.Contains(page, "最初の依頼より前の記録") {
		t.Fatal("grouped history missing")
	}
	code, page, _ = get(t, h, "/")
	if code != 200 || !strings.Contains(page, "履歴の表示") || strings.Contains(page, "確認待ちになった日時") {
		t.Error("home should list the task without the review date")
	}
	if _, _, err := store.SetStatus(item.ID, task.Doing, "", ""); err != nil {
		t.Fatal(err)
	}
	code, page, _ = get(t, h, "/tasks/"+item.ID)
	if code != 200 || strings.Contains(page, "確認待ちになった日時") {
		t.Error("a task in progress should not show the review date")
	}
	if _, _, err := store.SetStatus(item.ID, task.Review, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, task.HistoryFile)); err != nil {
		t.Fatal(err)
	}
	code, page, _ = get(t, h, "/tasks/"+item.ID)
	if code != 200 || !strings.Contains(page, "確認待ちになった日時 日時不明") {
		t.Error("a reviewing task without history should show an unknown review date")
	}
}

func TestHistoryUnknownAndMalformedRecords(t *testing.T) {
	lines := []task.Line{{Raw: "壊れた記録"}, {OK: true, Event: task.Event{Type: task.EventLegacy, Text: "以前の記録"}}}
	groups := groupedHistory(lines, "areas/work/tasks/2026-11/T1001")
	if len(groups) != 1 || len(groups[0].Entries) != 2 || groups[0].Entries[0].Text != "壊れた記録" {
		t.Fatal("legacy or malformed event hidden")
	}
	if got := reviewSince(lines); got != "日時不明" {
		t.Errorf("review date invented: %s", got)
	}
	lines = append(lines, task.Line{OK: true, Event: task.Event{Type: task.EventStatus, From: "doing", To: "review", At: "invalid"}})
	if got := reviewSince(lines); got != "日時不明" {
		t.Errorf("invalid date = %s", got)
	}
}

func TestArtifactHeadingDoesNotReadOutsideRepository(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(outside, []byte("# 秘密の題名\n"), 0644)
	os.MkdirAll(filepath.Join(root, "artifacts"), 0755)
	os.Symlink(outside, filepath.Join(root, "artifacts", "linked.md"))
	infos := artifactInfos(root, task.Task{}, []string{"artifacts/linked.md"}, nil)
	if infos[0].Title != "artifacts/linked.md" || infos[0].Current || infos[0].At != "日時不明" {
		t.Fatalf("outside file metadata exposed: %+v", infos)
	}
}

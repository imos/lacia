package task

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newFormat1Store(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"format": 1, "areas": ["work", "personal"]}`), 0o644)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 24, 0, 15, 2, 0, time.UTC) } // 09:15:02 JST
	return s
}

func TestUnknownFormatStops(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"format": 3}`), 0o644)
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "新しい lacia") {
		t.Fatalf("format 3 was opened: %v", err)
	}
	cfg, err := LoadConfig(t.TempDir())
	if err != nil || cfg.Format != 0 {
		t.Fatalf("a repository without settings = %+v, %v", cfg, err)
	}
}

func TestFormat1Layout(t *testing.T) {
	s := newFormat1Store(t)
	got, err := s.Create(NewTask{Area: "work", Title: "形を確かめる", Slug: "layout", Actor: "local-claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != "areas/work/tasks/2026-11/T1001-layout" {
		t.Errorf("dir = %s", got.Dir)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), got.Dir, LogFile)); !os.IsNotExist(err) {
		t.Errorf("format 1 wrote log.md: %v", err)
	}
	os.WriteFile(filepath.Join(s.Root(), AliasFile1), []byte("old-01\tT1001\n"), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if again, err := s.Get("old-01"); err != nil || again.Dir != got.Dir {
		t.Errorf("reloaded = %+v, %v", again, err)
	}
	detail, _ := s.Detail(got)
	if strings.Join(detail.Files, ",") != HistoryFile {
		t.Errorf("files = %v", detail.Files)
	}
	if w := CheckFormat(got, s.Files(got), 1); len(w) != 0 {
		for _, x := range w {
			if strings.Contains(x.Message, HistoryFile) {
				t.Errorf("log.jsonl was taken for a stray file: %v", x)
			}
		}
	}
}

// The events come out in the shape of the example in the design.
func TestFormat1History(t *testing.T) {
	s := newFormat1Store(t)
	created, err := s.Create(NewTask{Area: "work", Title: "履歴を確かめる", Actor: "local-claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Claim(created.ID, Owner{Label: "local-claude", Session: "825e5bc4"}, "local-claude", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutFile(created.ID, ArtifactsDir, "plan.md", strings.NewReader("<計画> & 案\n"), "local-claude"); err != nil {
		t.Fatal(err)
	}
	if err := s.Log(created.ID, Event{Type: EventRequest, Request: "ask-d67c831146e0a99c", Via: "コマンド", Text: "依頼を受け付けました（コマンド、ask-d67c831146e0a99c）。"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Log(created.ID, Event{Type: EventNote, By: "local-claude", Text: "計画を置きました。\n\n- 根拠にした指示は、2026-11-24のものです。"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetField(created.ID, "due", "", "Web"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetField(created.ID, "priority", "high", "Web"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetStatus(created.ID, Review, "local-claude", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Root(), created.Dir, HistoryFile))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"at":"2026-11-24T09:15:02+09:00","type":"created","by":"local-claude","status":"todo","text":"タスクを作りました。状態は「未着手」です。"}`,
		`{"at":"2026-11-24T09:15:02+09:00","type":"status","by":"local-claude","from":"todo","to":"doing","owner":"local-claude","session":"825e5bc4","text":"状態を「未着手」から「進行中」に変えました。担当は local-claude です。"}`,
		`{"at":"2026-11-24T09:15:02+09:00","type":"file","by":"local-claude","action":"put","path":"artifacts/plan.md","bytes":15,"sha256":"`,
		`{"at":"2026-11-24T09:15:02+09:00","type":"request","request":"ask-d67c831146e0a99c","via":"コマンド","text":"依頼を受け付けました（コマンド、ask-d67c831146e0a99c）。"}`,
		`{"at":"2026-11-24T09:15:02+09:00","type":"note","by":"local-claude","text":"計画を置きました。\n\n- 根拠にした指示は、2026-11-24のものです。"}`,
		`{"at":"2026-11-24T09:15:02+09:00","type":"field","by":"Web","key":"priority","value":"high","text":"priority を「high」にしました。"}`,
		`{"at":"2026-11-24T09:15:02+09:00","type":"status","by":"local-claude","from":"doing","to":"review","text":"状態を「進行中」から「確認待ち」に変えました。"}`,
		`{"at":"2026-11-24T09:15:02+09:00","type":"handover","from":"doing","to":"review","body":"## ひとことで\n\n（まだ書かれていません）`,
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d events, want %d:\n%s", len(lines), len(want), data)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, want[i]) {
			t.Errorf("event %d =\n%s\nwant a line starting with\n%s", i, line, want[i])
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Errorf("event %d is not JSON: %v", i, err)
		}
	}
	if bytes.Contains(data, []byte(`\u`)) {
		t.Errorf("text was escaped:\n%s", data)
	}
	if !strings.Contains(lines[2], `"text":"artifacts/plan.md を置きました（15バイト、sha256 `) {
		t.Errorf("file event = %s", lines[2])
	}
	if line, _ := MarshalEvent(Event{Type: EventNote, Text: "<a> & b"}); !strings.Contains(string(line), `"<a> & b"`) {
		t.Errorf("marks were escaped: %s", line)
	}

	shown, err := s.HistoryLines(created, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]string{
		0: "- 2026-11-24 09:15 タスクを作りました。状態は「未着手」です。（local-claude）",
		3: "- 2026-11-24 09:15 依頼を受け付けました（コマンド、ask-d67c831146e0a99c）。",
		7: "- 2026-11-24 09:15 このときの本文を残しました。",
	} {
		if shown[i] != want {
			t.Errorf("shown[%d] = %q, want %q", i, shown[i], want)
		}
	}
	if tail, _ := s.HistoryLines(created, 2); len(tail) != 2 || tail[1] != shown[7] {
		t.Errorf("tail = %q", tail)
	}
}

// Every change that takes a task out of "doing" keeps the body, including
// the ones the server makes.
func TestFormat1Handover(t *testing.T) {
	s := newFormat1Store(t)
	created, _ := s.Create(NewTask{Area: "work", Title: "引き継ぎ", Status: Doing})
	s.Ready(created.ID, "届けたときの記録\n句点のない引用", true)
	s.SetStatus(created.ID, Doing, "Web", "")
	s.Stop(created.ID, "")
	s.SetStatus(created.ID, Todo, "Web", "")
	lines, _ := ReadEvents(filepath.Join(s.Root(), created.Dir, HistoryFile))
	var types []string
	for _, l := range lines {
		types = append(types, l.Event.Type)
	}
	if got := strings.Join(types, " "); got != "created woke handover status stopped handover status" {
		t.Fatalf("events = %s", got)
	}
	const change = "状態を「進行中」から「確認待ち」に変えました。"
	if got := lines[1].Event.Text; got != "届けたときの記録\n句点のない引用\n"+change {
		t.Errorf("woke text = %q", got)
	}
	if got := lines[4].Event.Text; got != change {
		t.Errorf("stopped text = %q", got)
	}
	if body := lines[2].Event.Body; !strings.Contains(body, "## 次にすること") || strings.Contains(body, "status:") {
		t.Errorf("handover body = %q", body)
	}
	long := handover(Doing, Review, strings.Repeat("あ", MaxHandoverBody))
	if len(long.Body) > MaxHandoverBody || !strings.HasSuffix(long.Body, "あ") {
		t.Errorf("long body kept %d bytes", len(long.Body))
	}
}

// Lines this version does not know, and lines moved from log.md, read back
// without errors.
func TestReadEventsTolerates(t *testing.T) {
	path := filepath.Join(t.TempDir(), HistoryFile)
	os.WriteFile(path, []byte(strings.Join([]string{
		`{"at":"2026-11-20T00:00:00+09:00","type":"legacy","text":"## これまでの経緯\n\n- 2026-11-01 始めました","imported":true}`,
		`{"at":"2026-11-20T10:00:00+09:00","type":"note","by":"local-claude 2","text":"調べました。（local-claude 2）","imported":true}`,
		`{"at":"2026-11-20T11:00:00+09:00","type":"someday","extra":1}`,
		`{"at":"2026-11-20T12:00:00+09:00","type":"someday","text":"新しい種類です。"}`,
		`not json`,
		``,
	}, "\n")), 0o644)
	lines, err := ReadHistory(filepath.Dir(path), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"## これまでの経緯\n\n- 2026-11-01 始めました",
		"- 2026-11-20 10:00 調べました。（local-claude 2）",
		`{"at":"2026-11-20T11:00:00+09:00","type":"someday","extra":1}`,
		"- 2026-11-20 12:00 新しい種類です。",
		"not json",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("lines =\n%s", strings.Join(lines, "\n"))
	}
}

func TestFormat1Check(t *testing.T) {
	good := Task{ID: "T1001", Status: Review, Body: "## ひとことで\n\n形を確かめます。\n\n## 次にすること\n\nオーナーさんに、結果を見ていただきます。\n\n## 今回したこと\n\n調べました。\n\n## いまの状況\n\n済んでいることがあります。\n\n## いまの資料\n\n- [結果](artifacts/a.md)\n"}
	if w := CheckFormat(good, []string{"task.md", "log.jsonl", "notes.md", "artifacts/a.md"}, 1); len(w) != 0 {
		t.Errorf("a well-formed task got warnings: %v", w)
	}
	// A task written before the switch has the state and no summary.
	old := Task{ID: "T1002", Status: Doing, Body: "## いまの状況\n\n進めています。\n\n## 次にすること\n\nレイシアが続けます。\n"}
	if w := CheckFormat(old, []string{"task.md", "log.jsonl"}, 1); len(w) != 0 {
		t.Errorf("a task of before got warnings: %v", w)
	}
	bad := Task{ID: "T1003", Status: Doing, Body: "## 見ていただくもの\n\n- a\n\n## 次にすること\n\n" + Placeholder + "\n"}
	text := ""
	for _, w := range CheckFormat(bad, []string{"task.md", "log.md"}, 1) {
		text += w.String() + "\n"
	}
	for _, want := range []string{"「見ていただくもの」という見出しは", "lacia task note", "「## ひとことで」がありません。", "「ひとことで」と「次にすること」を書いて", "log.md は"} {
		if !strings.Contains(text, want) {
			t.Errorf("no warning containing %q in:\n%s", want, text)
		}
	}
	long := strings.Repeat("行\n", MaxBodyLines)
	if w := LintFormat(long, 1); len(w) != 0 {
		t.Errorf("%d lines got warnings: %v", MaxBodyLines, w)
	}
	if w := LintFormat(long+"行\n", 0); len(w) != 1 {
		t.Errorf("%d lines got %v", MaxBodyLines+1, w)
	}
	if got := Body(1, "a", "b"); got != "## ひとことで\n\na\n\n## 次にすること\n\nb\n" {
		t.Errorf("body = %q", got)
	}
}

func TestLegacyRuntimeRequiresMigration(t *testing.T) {
	for _, config := range []string{"", `{"format":0}`} {
		root := t.TempDir()
		if config != "" {
			os.MkdirAll(filepath.Join(root, ".lacia"), 0755)
			os.WriteFile(filepath.Join(root, ConfigFile), []byte(config), 0644)
		}
		if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "lacia migrate --dry-run") {
			t.Fatalf("legacy runtime was opened: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "areas")); !os.IsNotExist(err) {
			t.Fatalf("rejected open changed the repository: %v", err)
		}
		if _, err := ReadHistory(root, 0, 0); err == nil || !strings.Contains(err.Error(), "lacia migrate") {
			t.Fatalf("legacy runtime history read: %v", err)
		}
	}
}

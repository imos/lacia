package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestResumeReminderOnlyWhenNotBeingWorkedOn(t *testing.T) {
	for status, want := range map[task.Status]bool{
		task.Todo: true, task.Waiting: true, task.Review: true,
		task.Doing: false, task.Done: false, task.Cancelled: false,
	} {
		line := resumeReminder(task.Task{ID: "T1001", Status: status})
		if (line != "") != want {
			t.Errorf("%s: reminder = %q, want shown = %v", status, line, want)
		}
		if want && !strings.Contains(line, "lacia task set T1001 doing") {
			t.Errorf("%s: reminder does not give the command: %q", status, line)
		}
	}
}

func TestTaskWriteSavesLocallyOrThroughTheServer(t *testing.T) {
	t.Setenv("LACIA_API", "")
	t.Setenv("LACIA_TASKS", "")
	writable, mirror := t.TempDir(), t.TempDir()
	writableTask := taskMirrorFixture(t, writable, false)
	mirrorTask := taskMirrorFixture(t, mirror, true)
	t.Chdir(t.TempDir())
	body := "## いまの状況\n\n新しい本文です。\n\n## 次にすること\n\nわたしが続きを書きます。\n"
	file := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without a server, the command writes the file itself.
	out, _, err := captureMirrorTask(t, "write", "--root", writable, "T1001", "task.md", file)
	if err != nil || !strings.Contains(out, "T1001 の task.md を保存しました。") || !strings.Contains(out, "lacia task set T1001 doing") {
		t.Fatalf("local write = %q, %v", out, err)
	}
	want := "---\nid: T1001\ntitle: Macの旧版\nstatus: todo\ncreated: 2026-11-18\n---\n\n" + body
	if data, _ := os.ReadFile(filepath.Join(writableTask, "task.md")); string(data) != want {
		t.Fatalf("task.md = %q", data)
	}
	if _, _, err := captureMirrorTask(t, "write", "--root", writable, "T1001", "task.md", filepath.Join(writableTask, "task.md")); err == nil || !strings.Contains(err.Error(), "冒頭の項目") {
		t.Fatalf("front matter was taken: %v", err)
	}

	// On a read-only copy, the text goes to the server and the copy stays.
	var got struct{ path, name, body string }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		got.path, got.name, got.body = r.Method+" "+r.URL.Path, r.URL.Query().Get("name"), string(data)
		json.NewEncoder(w).Encode(map[string]any{
			"task": task.Task{ID: "T1001", Status: task.Doing}, "warning": "T1001 は、いま work-codex が進めています。",
			"advice": []string{"3行目: 気になる文があります。"},
		})
	}))
	defer server.Close()
	t.Setenv("LACIA_API", server.URL)
	before, _ := os.ReadFile(filepath.Join(mirrorTask, "task.md"))
	out, warning, err := captureMirrorTask(t, "write", "--root", mirror, "T1001", "notes.md", file)
	if err != nil || got.path != "PUT /api/tasks/T1001/doc" || got.name != "notes.md" || got.body != body {
		t.Fatalf("sent = %+v, %v", got, err)
	}
	if !strings.Contains(out, "T1001 の notes.md を保存しました。") || !strings.Contains(out, "T1001: 3行目: 気になる文があります。") || !strings.Contains(warning, "いま work-codex が進めています") {
		t.Fatalf("out = %q, warning = %q", out, warning)
	}
	after, _ := os.ReadFile(filepath.Join(mirrorTask, "task.md"))
	if _, err := os.Stat(filepath.Join(mirrorTask, "notes.md")); string(after) != string(before) || !os.IsNotExist(err) {
		t.Fatalf("the read-only copy changed: %v", err)
	}
}

func TestTaskFieldAfterRunResolvesLegacyName(t *testing.T) {
	t.Setenv("LACIA_API", "")
	t.Setenv("LACIA_TASKS", "")
	root := t.TempDir()
	taskMirrorFixture(t, root, false)
	t.Chdir(t.TempDir())
	dir := filepath.Join(root, "routines", "S1001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := "---\nid: S1001\ntitle: GitHubの確認\narea: work\ndaily: \"09:00\"\nlegacy_name: github\n---\n\n確認してください。\n"
	if err := os.WriteFile(filepath.Join(dir, "routine.md"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureMirrorTask(t, "field", "--root", root, "T1001", "after_run", "no-such-job"); err == nil {
		t.Fatal("unknown routine accepted")
	}
	if _, _, err := captureMirrorTask(t, "field", "--root", root, "T1001", "after_run", "github"); err != nil {
		t.Fatal(err)
	}
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("T1001")
	if err != nil || got.AfterRun != "S1001" || got.AfterRunSince == "" {
		t.Fatalf("stored after_run = %q, registration = %q, error = %v", got.AfterRun, got.AfterRunSince, err)
	}
}

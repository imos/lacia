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

func taskMirrorFixture(t *testing.T, root string, readOnly bool) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".lacia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "areas", "work", "tasks", "2026-11", "T1001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: T1001\ntitle: Macの旧版\nstatus: todo\ncreated: 2026-11-18\n---\n\n古い本文です。\n"
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if readOnly {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		marker := []byte("[remote \"origin\"]\n\tpushurl = disabled://lacia-local-readonly\n")
		if err := os.WriteFile(filepath.Join(root, ".git", "config"), marker, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTaskExplicitRootControlsWriteRouting(t *testing.T) {
	t.Setenv("LACIA_API", "")
	t.Setenv("LACIA_TASKS", "")
	mirror, writable := t.TempDir(), t.TempDir()
	taskMirrorFixture(t, mirror, true)
	writableTask := taskMirrorFixture(t, writable, false)

	t.Chdir(writable)
	if apiBase() != "" || apiForRoot(mirror) != localAPI {
		t.Fatal("the mirror must keep the API route when selected from a writable repository")
	}
	t.Chdir(mirror)
	if apiBase() != localAPI || apiForRoot(writable) != "" {
		t.Fatal("a writable target must not inherit the mirror API from the current directory")
	}
	if err := runTask([]string{"note", "--root", writable, "T1001", "隔離環境で検証しました。"}); err != nil {
		t.Fatal(err)
	}
	mirroredLog := filepath.Join(mirror, "areas", "work", "tasks", "2026-11", "T1001", task.HistoryFile)
	if _, err := os.Stat(mirroredLog); !os.IsNotExist(err) {
		t.Fatalf("unexpected change to source mirror: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(writableTask, task.HistoryFile)); err != nil || !strings.Contains(string(b), "隔離環境で検証しました。") {
		t.Fatalf("the writable task did not receive the note: %s, %v", b, err)
	}
}

// A command can be run from one repository to check another one's tasks.
func TestTaskLintUsesTheExplicitRootForReviewers(t *testing.T) {
	t.Setenv("LACIA_USER", "")
	t.Setenv("LACIA_API", "")
	target := t.TempDir()
	dir := taskMirrorFixture(t, target, false)
	if err := os.WriteFile(filepath.Join(target, task.ConfigFile), []byte(`{"format":2,"user":"佐藤さん"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `---
id: T1001
title: 予定を確かめる
status: review
created: 2026-11-18
---

## ひとことで

予定をまとめました。

## 次にすること

佐藤さんに予定を見ていただきます。
`
	if err := os.WriteFile(filepath.Join(dir, task.TaskFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	task.SetOwner("")
	t.Cleanup(func() { task.SetOwner("") })
	out, _, err := captureMirrorTask(t, "lint", "--root", target, "T1001")
	if err != nil || out != "気になる箇所はありませんでした。\n" {
		t.Fatalf("lint did not use the target repository's reviewer: %q, %v", out, err)
	}
}

func TestTaskMirrorReadsServerAndFallsBackOnlyForUnavailableReads(t *testing.T) {
	t.Setenv("LACIA_TASKS", "")
	mirror := t.TempDir()
	localTask := taskMirrorFixture(t, mirror, true)
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tasks":
			json.NewEncoder(w).Encode([]task.Task{{ID: "T1002", Title: "サーバーの新版", Area: "work", Status: task.Doing}})
		case "/api/tasks/T1002":
			if r.URL.Query().Get("detail") != "1" {
				http.Error(w, "detail missing", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(task.Detail{
				Dir: "work/tasks/2026-11/T1002", Markdown: "新しい本文です。\n",
				Files:    []string{"notes.md", "artifacts/result.md"},
				Children: []task.Task{{ID: "T1003", Title: "子のタスク", Status: task.Todo}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"サーバーにありません。"}`)
		}
	}))
	defer server.Close()
	t.Setenv("LACIA_API", server.URL)

	if out, warning, err := captureMirrorTask(t, "list", "--root", mirror); err != nil || warning != "" || !strings.Contains(out, "サーバーの新版") || strings.Contains(out, "Macの旧版") {
		t.Fatalf("server list = %q / %q / %v", out, warning, err)
	}
	if out, warning, err := captureMirrorTask(t, "show", "--root", mirror, "T1002"); err != nil || warning != "" ||
		!strings.Contains(out, "新しい本文です。") || !strings.Contains(out, "ほかのファイル: notes.md") ||
		!strings.Contains(out, "  artifacts/result.md") || !strings.Contains(out, "T1003") {
		t.Fatalf("server show = %q / %q / %v", out, warning, err)
	}
	if out, warning, err := captureMirrorTask(t, "show", "--root", mirror, "T1001"); err == nil || out != "" || warning != "" || !strings.Contains(err.Error(), "サーバーにありません") {
		t.Fatalf("server 404 must not show a stale task: %q / %q / %v", out, warning, err)
	}

	server.Close()
	if out, warning, err := captureMirrorTask(t, "list", "--root", mirror); err != nil || !strings.Contains(out, "Macの旧版") || !strings.Contains(warning, "手元にある記録") {
		t.Fatalf("offline list = %q / %q / %v", out, warning, err)
	}
	if out, warning, err := captureMirrorTask(t, "show", "--root", mirror, "T1001"); err != nil || !strings.Contains(out, "古い本文です。") || !strings.Contains(warning, "手元にある記録") {
		t.Fatalf("offline show = %q / %q / %v", out, warning, err)
	}
	if err := runTask([]string{"note", "--root", mirror, "T1001", "書き込まない"}); err == nil {
		t.Fatal("offline write unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(localTask, task.HistoryFile)); !os.IsNotExist(err) {
		t.Fatalf("offline write changed the mirror: %v", err)
	}
}

func captureMirrorTask(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	warning, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer warning.Close()

	usualOut, usualWarning := os.Stdout, os.Stderr
	defer func() { os.Stdout, os.Stderr = usualOut, usualWarning }()
	os.Stdout, os.Stderr = out, warning
	runErr := runTask(args)
	stdout, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.ReadFile(warning.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(stdout), string(stderr), runErr
}

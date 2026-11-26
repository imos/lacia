package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestTaskJSONListAndShow(t *testing.T) {
	t.Setenv("LACIA_API", "")
	t.Setenv("LACIA_TASKS", "")
	root := t.TempDir()
	taskMirrorFixture(t, root, false)
	t.Chdir(t.TempDir())
	out, _, err := captureMirrorTask(t, "list", "--root", root, "--json", "--area", "work", "--status", "todo")
	var tasks []task.Task
	if err != nil || json.Unmarshal([]byte(out), &tasks) != nil || len(tasks) != 1 || tasks[0].ID != "T1001" {
		t.Fatalf("%s %v", out, err)
	}
	out, _, err = captureMirrorTask(t, "show", "T1001", "--root", root, "--json")
	var detail task.Detail
	if err != nil || json.Unmarshal([]byte(out), &detail) != nil || detail.Dir == "" || detail.Markdown == "" {
		t.Fatalf("%s %v", out, err)
	}
	out, _, err = captureMirrorTask(t, "list", "--root", root, "--json", "--status", "done")
	if err != nil || out != "[]\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestTaskJSONReadsServerOnMirror(t *testing.T) {
	root := t.TempDir()
	taskMirrorFixture(t, root, true)
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tasks" {
			json.NewEncoder(w).Encode([]task.Task{{ID: "T1002", Title: "サーバー", Status: task.Doing, Area: "work"}})
			return
		}
		json.NewEncoder(w).Encode(task.Detail{Dir: "areas/work/tasks/2026-11/T1002", Markdown: "---\nid: T1002\n---\n本文です。\n", Files: []string{"artifacts/result.md"}})
	}))
	defer server.Close()
	t.Setenv("LACIA_API", server.URL)
	out, warning, err := captureMirrorTask(t, "list", "--root", root, "--json")
	var tasks []task.Task
	if err != nil || warning != "" || json.Unmarshal([]byte(out), &tasks) != nil || len(tasks) != 1 || tasks[0].ID != "T1002" {
		t.Fatalf("%s %s %v", out, warning, err)
	}
	out, warning, err = captureMirrorTask(t, "show", "--root", root, "T1002", "--json")
	var detail task.Detail
	if err != nil || warning != "" || json.Unmarshal([]byte(out), &detail) != nil || len(detail.Files) != 1 {
		t.Fatalf("%s %s %v", out, warning, err)
	}
}

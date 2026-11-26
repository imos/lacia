package main

import (
	"encoding/json"
	"github.com/imos/lacia/internal/testrecords"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/web"
)

func TestAskKeepStatusEndToEnd(t *testing.T) {
	store, err := task.Open(testrecords.Init(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(task.NewTask{Area: "work", Title: "記録", Status: task.Review})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	queue, err := request.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := (&web.Server{Store: store, Requests: queue}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	t.Setenv("LACIA_API", server.URL)
	args := []string{"--task", record.ID, "--keep-status", "--id", "cli-keep", "記録を保存してください。"}
	if err := runAsk(args); err != nil {
		t.Fatal(err)
	}
	if err := runAsk(args); err != nil {
		t.Fatal(err)
	}
	queue, err = request.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := queue.Get("cli-keep")
	if !ok {
		t.Fatal("missing saved request")
	}
	encoded, _ := json.Marshal(saved)
	if saved.StatusPolicy != request.KeepStatus || saved.Was != "" || !strings.Contains(string(encoded), `"status_policy":"keep"`) || !strings.Contains(request.Prompt(saved), "処理中も終了後もタスクの状態を変えず") {
		t.Fatalf("lost policy: %+v", saved)
	}
	current, _ := store.Get(record.ID)
	if current.Status != task.Review || queue.Pending() != 1 {
		t.Fatalf("state=%s pending=%d", current.Status, queue.Pending())
	}
	if err := runAsk([]string{"--keep-status", "--id", "invalid-keep", "記録"}); err == nil {
		t.Fatal("keep without existing task was accepted")
	}
	if _, ok := queue.Get("invalid-keep"); ok {
		t.Fatal("invalid request reached API")
	}
}

func TestAskResumeEndToEnd(t *testing.T) {
	store, err := task.Open(testrecords.Init(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Create(task.NewTask{Area: "work", Title: "再開", Status: task.Review})
	q, _ := request.Open(t.TempDir())
	q.Add(request.Request{ID: "original-request", TaskID: record.ID, Text: "private-original"})
	q.Resolve("original-request", request.Unknown, "partial-result", "interrupted", false)
	handler, _ := (&web.Server{Store: store, Requests: q}).Handler()
	server := httptest.NewServer(handler)
	defer server.Close()
	t.Setenv("LACIA_API", server.URL)
	for _, args := range [][]string{{"--resume", "original-request", "--id", "cli-resume"}, {"--resume", "original-request"}} {
		if err := runAsk(args); err != nil {
			t.Fatal(err)
		}
	}
	saved, ok := q.Get("cli-resume")
	if !ok || saved.ResumeOf != "original-request" || saved.Text != "private-original" || q.Pending() != 1 {
		t.Fatalf("resume lost: %+v", saved)
	}
	for _, args := range [][]string{{"--resume", "original-request", "replacement"}, {"--resume", "original-request", "--task", record.ID}, {"--resume", "original-request", "--keep-status"}, {"--resume", "original-request", "--area", "work"}} {
		if err := runAsk(args); err == nil {
			t.Fatal("accepted incompatible resume flags")
		}
	}
}

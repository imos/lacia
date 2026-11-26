package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

// A repository that keeps no tasks takes requests without making a task for
// each, refuses to make or move one, and shows no task lists on its pages.
func TestRepositoryWithoutTasks(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.MkdirAll(filepath.Join(root, "areas", "family", "tasks"), 0o755)
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format": 1, "areas": ["family"], "id_prefix": "F", "tasks": false}`), 0o644)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	q, err := request.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := (&Server{Store: store, Requests: q, Version: "test"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	send := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(body)))
		return rec
	}
	rec := send(`{"text":"あしたの天気を教えてください。"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", rec.Code, rec.Body.String())
	}
	var saved request.Request
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.TaskID != "" || len(store.List()) != 0 {
		t.Fatalf("a task was made for the request: %+v, %d tasks", saved, len(store.List()))
	}
	if rec := send(`{"text":"続きです。","task":"F1001"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "タスクを持たない") {
		t.Errorf("a request about a task: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tasks", strings.NewReader(`{"area":"family","title":"作ってはいけないタスク"}`)))
	if rec.Code == http.StatusOK || rec.Code == http.StatusCreated || len(store.List()) != 0 {
		t.Errorf("a task was made through the API: %d %s", rec.Code, rec.Body.String())
	}

	code, page, _ := get(t, h, "/")
	if code != 200 {
		t.Fatalf("home: %d", code)
	}
	for _, gone := range []string{"新しいタスク", `href="/tasks"`, `href="/projects"`, "最近の完了と取りやめ", `<select name="area">`} {
		if strings.Contains(page, gone) {
			t.Errorf("home of a repository without tasks shows %q", gone)
		}
	}
	if !strings.Contains(page, "依頼する") {
		t.Error("the form for requests is gone")
	}
}

func TestStoreWithoutTasksRefusesChanges(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.MkdirAll(filepath.Join(root, "areas", "family", "tasks"), 0o755)
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format": 1, "areas": ["family"], "id_prefix": "F"}`), 0o644)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.Create(task.NewTask{Area: "family", Title: "以前のタスク", Status: task.Done})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format": 1, "areas": ["family"], "id_prefix": "F", "tasks": false}`), 0o644)
	store, err = task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if store.KeepsTasks() {
		t.Fatal("the setting was not read")
	}
	if _, err := store.Create(task.NewTask{Area: "family", Title: "新しいタスク"}); err != task.ErrNoTasks {
		t.Errorf("create: %v", err)
	}
	if _, _, err := store.SetStatus(old.ID, task.Todo, "test", ""); err != task.ErrNoTasks {
		t.Errorf("set status: %v", err)
	}
	if _, _, err := store.Claim(old.ID, task.Owner{Label: "home-codex", Session: "s"}, "test", ""); err != task.ErrNoTasks {
		t.Errorf("claim: %v", err)
	}
	// The tasks from before stay readable.
	if got, err := store.Get(old.ID); err != nil || got.Status != task.Done {
		t.Errorf("old task: %+v, %v", got, err)
	}
}

package web

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

// orderHeadingForTest is the section of project.md that orders the tasks.
const orderHeadingForTest = "取りかかる順"

// A repository keeps its areas under areas/ and its history in log.jsonl;
// the pages read both from there.
func TestAreaPages(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format": 2, "areas": ["work"]}`), 0o644)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, title := range []string{"定義を決める", "データを作る"} {
		created, err := store.Create(task.NewTask{Area: "work", Title: title, Actor: "Web"})
		if err != nil {
			t.Fatal(err)
		}
		store.SetField(created.ID, "project", "alpha", "")
		ids = append(ids, created.ID)
	}
	dir := filepath.Join(root, "areas", "work", "projects", "alpha")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "project.md"), []byte("---\ntitle: 試しのプロジェクト\nstatus: active\n---\n\n## "+orderHeadingForTest+"\n\n1. "+ids[0]+"\n2. "+ids[1]+"\n"), 0o644)
	h, err := (&Server{Store: store}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	code, page, _ := get(t, h, "/tasks/"+ids[0])
	for _, want := range []string{`href="/tasks/` + ids[0] + `/log"`, `log.jsonl?view=source`, "タスクを作りました。状態は「未着手」です。（Web）", "2件のうち 1番目", "試しのプロジェクト"} {
		if code != 200 || !strings.Contains(page, want) {
			t.Errorf("task page (%d) lacks %q", code, want)
		}
	}
	code, page, _ = get(t, h, "/tasks/"+ids[0]+"/log")
	if code != 200 || !strings.Contains(page, "タスクを作りました。状態は「未着手」です。（Web）") || !strings.Contains(page, "project を「alpha」にしました。") {
		t.Errorf("history page (%d):\n%s", code, page)
	}
	if code, page, _ = get(t, h, "/tasks/"+ids[0]+"/files/log.jsonl"); code != 200 || !strings.Contains(page, `&#34;type&#34;:&#34;created&#34;`) {
		t.Errorf("raw history (%d):\n%s", code, page)
	}

	req := httptest.NewRequest("POST", "/projects/work/alpha/move", strings.NewReader("id="+ids[1]+"&dir=up"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatalf("move: %d", rec.Code)
	}
	data, _ := os.ReadFile(filepath.Join(dir, task.HistoryFile))
	if !strings.Contains(string(data), `"type":"order","by":"Web","task":"`+ids[1]+`","direction":"up","text":"`+ids[1]+` を1つ前に移しました。"`) {
		t.Errorf("project history:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, task.LogFile)); !os.IsNotExist(err) {
		t.Errorf("log.md was written for the project: %v", err)
	}
	code, page, _ = get(t, h, "/projects/work/alpha")
	if code != 200 || !strings.Contains(page, `href="/projects/work/alpha/log"`) || !strings.Contains(page, "1つ前に移しました。（Web）") || strings.Contains(page, "log.jsonl</a>") {
		t.Errorf("project page (%d):\n%s", code, page)
	}
	if code, page, _ = get(t, h, "/projects/work/alpha/log"); code != 200 || !strings.Contains(page, "1つ前に移しました。（Web）") {
		t.Errorf("project history page (%d):\n%s", code, page)
	}
	// Links written from the root of the repository open the task page.
	if got := repoURL("areas/work/tasks/2026-11/T1001-x/artifacts/a.md"); got != "/tasks/T1001/files/artifacts/a.md" {
		t.Errorf("repoURL = %s", got)
	}
}

// lacia project note writes to the project's history through the server.
func TestProjectNote(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"format": 2, "areas": ["work", "personal"]}`), 0o644)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, area := range []string{"work", "personal"} {
		dir := filepath.Join(root, task.AreaDir(area), "projects", "alpha")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "project.md"), []byte("---\ntitle: アルファ\n---\n"), 0o644)
	}
	h, _ := (&Server{Store: store}).Handler()
	post := func(body string) int {
		req := httptest.NewRequest("POST", "/api/projects/notes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(`{"project": "alpha", "log": "順を変えました。"}`); code != 404 {
		t.Errorf("an ambiguous name got %d", code)
	}
	if code := post(`{"project": "work/alpha", "log": "順を変えました。\n理由です。", "actor": "local-claude"}`); code != 200 {
		t.Fatalf("note got %d", code)
	}
	data, _ := os.ReadFile(filepath.Join(root, task.AreaDir("work"), "projects", "alpha", task.HistoryFile))
	if want := `"type":"note","by":"local-claude","text":"順を変えました。\n理由です。"`; !strings.Contains(string(data), want) {
		t.Errorf("history =\n%s", data)
	}
}

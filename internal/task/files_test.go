package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskFiles(t *testing.T) {
	s := newStore(t)
	created, _ := s.Create(NewTask{Area: "work", Title: "大きな成果物のあるタスク"})
	got, err := s.PutFile(created.ID, FilesDir, "report/index.html", strings.NewReader("<h1>結果</h1>"), "Web")
	if err != nil || got.Path != "report/index.html" || got.Size != 15 {
		t.Fatalf("put: %+v %v", got, err)
	}
	for _, bad := range []string{"../../task.md", ".keep", "a/.hidden", ""} {
		if _, err := s.PutFile(created.ID, FilesDir, bad, strings.NewReader("x"), ""); err == nil {
			t.Errorf("name %q was accepted", bad)
		}
	}
	// A path that tries to climb out stays inside files/.
	if data, _ := os.ReadFile(filepath.Join(s.Root(), created.Dir, "task.md")); !strings.Contains(string(data), "id: "+created.ID) {
		t.Fatal("task.md was overwritten")
	}
	if _, err := s.PutFile(created.ID, "notes", "a.txt", strings.NewReader("x"), ""); err == nil {
		t.Error("an unknown place was accepted")
	}
	task, _ := s.Get(created.ID)
	log, _ := readTestHistory(t, filepath.Join(s.Root(), task.Dir))
	if !strings.Contains(string(log), "files/report/index.html を置きました（15バイト、sha256 ") || !strings.Contains(string(log), "このファイルは Git の管理外です。") {
		t.Errorf("log:\n%s", log)
	}
	if w := CheckFormat(task, s.Files(task), s.Format()); len(w) != 0 {
		t.Errorf("files/ should be an allowed place: %v", w)
	}

	// Open tasks keep their files; closed ones lose them after 90 days.
	if s.ExpiresOn(task) != "" {
		t.Error("an open task has an expiry")
	}
	s.SetStatus(created.ID, Done, "", "") // done on 2026-11-12
	task, _ = s.Get(created.ID)
	if got := s.ExpiresOn(task); got != "2027-02-10" {
		t.Errorf("expires on %s", got)
	}
	if cleaned := s.ExpireFiles(time.Date(2027, 2, 9, 12, 0, 0, 0, JST)); len(cleaned) != 0 {
		t.Fatal("removed early")
	}
	s.SetPinned(created.ID, true)
	if cleaned := s.ExpireFiles(time.Date(2027, 3, 1, 0, 0, 0, 0, JST)); len(cleaned) != 0 || s.ExpiresOn(task) != "" {
		t.Fatal("a pinned task was cleaned")
	}
	s.SetPinned(created.ID, false)
	if cleaned := s.ExpireFiles(time.Date(2027, 2, 10, 0, 5, 0, 0, JST)); len(cleaned) != 1 || len(s.StoredFiles(task)) != 0 {
		t.Fatalf("not removed: %v", cleaned)
	}
}

func TestLargeArtifactIsFlagged(t *testing.T) {
	s := newStore(t)
	created, _ := s.Create(NewTask{Area: "work", Title: "報告"})
	s.PutFile(created.ID, ArtifactsDir, "small.md", strings.NewReader("結果です。"), "")
	s.PutFile(created.ID, ArtifactsDir, "big.html", strings.NewReader(strings.Repeat("x", LargeArtifact+1)), "")
	task, _ := s.Get(created.ID)
	warnings := s.FileWarnings(task)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "artifacts/big.html は 200 KiB を超えています（204801バイト）。") {
		t.Errorf("warnings = %v", warnings)
	}
}

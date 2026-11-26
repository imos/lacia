package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/web"
)

func TestTaskReasonLocalAndRemoteWrite(t *testing.T) {
	t.Setenv("LACIA_API", "")
	t.Setenv("LACIA_TASKS", "")
	t.Chdir(t.TempDir())
	root, mirror := t.TempDir(), t.TempDir()
	taskMirrorFixture(t, root, false)
	mirrorDir := taskMirrorFixture(t, mirror, true)
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	h, err := (&web.Server{Store: store, Version: "test"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	before, _ := os.ReadFile(filepath.Join(mirrorDir, task.TaskFile))
	file := filepath.Join(t.TempDir(), "body.md")
	body := "## ひとことで\n\n予定を見やすくします。\n\n## このタスクを作ったわけ\n\n祝日が分からず、予定を決めにくいことがありました。\n\n## 次にすること\n\nわたしが祝日を表示します。\n"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{root, mirror} {
		if target == mirror {
			t.Setenv("LACIA_API", server.URL)
		}
		out, _, err := captureMirrorTask(t, "write", "--root", target, "T1001", "task.md", file)
		if err != nil || !strings.Contains(out, "保存しました") {
			t.Fatalf("write: %q %v", out, err)
		}
		out, _, err = captureMirrorTask(t, "lint", "--root", root, "T1001")
		if err != nil || out != "気になる箇所はありませんでした。\n" {
			t.Fatalf("lint: %q %v", out, err)
		}
	}
	got, _ := store.Get("T1001")
	if got.Body != body || got.Status != task.Todo {
		t.Fatalf("server read back: %+v", got)
	}
	after, _ := os.ReadFile(filepath.Join(mirrorDir, task.TaskFile))
	if string(after) != string(before) {
		t.Fatal("remote write changed the read-only mirror")
	}
}

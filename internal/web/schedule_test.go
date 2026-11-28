package web

import (
	"github.com/imos/lacia/internal/testrecords"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// A definition left in schedules/ does not run; the list, the trouble
// report and its page say how to move it.
func TestSchedulePage(t *testing.T) {
	root := t.TempDir()
	store, err := task.Open(testrecords.Init(t, root))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "schedules"), 0o755)
	os.WriteFile(filepath.Join(root, "schedules/github.md"), []byte("---\ntitle: GitHubの確認\ndaily: \"09:00\"\n---\n\n確認してください。\n"), 0o644)
	h, err := (&Server{Store: store, Version: "test", Schedules: func() ([]schedule.Job, map[string]string) {
		return schedule.Load(root), nil
	}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	const moved = "areas/\\u003c領域\\u003e/routines"
	if _, body, _ := get(t, h, "/routines"); !strings.Contains(body, `href="/routines/github"`) {
		t.Error("the list does not link to the page of a job")
	}
	if _, body, _ := get(t, h, "/api/schedule-troubles"); !strings.Contains(body, `"key":"schedule:github:definition"`) || !strings.Contains(body, moved) || !strings.Contains(body, `"path":"/routines/github"`) {
		t.Errorf("troubles = %s", body)
	}
	status, body, _ := get(t, h, "/routines/github")
	if status != http.StatusOK || !strings.Contains(body, "予約の設定に問題があります。形式2では schedules/ の定義は実行しません。") {
		t.Errorf("page %d:\n%s", status, body)
	}
	if status, _, _ = get(t, h, "/routines/none"); status != http.StatusNotFound {
		t.Errorf("an unknown job returned %d", status)
	}
}

func TestLegacyScheduleLinksRetainHistoryQuery(t *testing.T) {
	_, h := setup(t)
	for src, want := range map[string]string{
		"/schedules":                      "/routines",
		"/schedules/github?month=2026-10": "/routines/github?month=2026-10",
	} {
		status, _, headers := get(t, h, src)
		if status != http.StatusPermanentRedirect || headers.Get("Location") != want {
			t.Fatalf("%s: got %d, %q; want redirect to %q", src, status, headers.Get("Location"), want)
		}
	}
}

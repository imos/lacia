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

func TestSchedulePage(t *testing.T) {
	root := t.TempDir()
	store, err := task.Open(testrecords.Init(t, root))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "schedules"), 0o755)
	os.WriteFile(filepath.Join(root, "schedules/github.md"), []byte("---\ntitle: GitHubの確認\ndaily: \"09:00\"\n---\n\n確認してください。\n"), 0o644)
	for month, lines := range map[string]string{
		"2026-10": `{"at":"2026-10-31T09:00:00+09:00","finished":"2026-10-31T09:02:00+09:00","outcome":"nothing"}` + "\n",
		"2026-11": `{"at":"2026-11-22T09:00:00+09:00","finished":"2026-11-22T09:03:00+09:00","outcome":"done","summary":"未確認の変更に<b>2件</b>あります。"}` + "\n" +
			`{"at":"2026-11-23T09:00:00+09:00","finished":"2026-11-24T08:00:00+09:00","outcome":"missed","count":1}` + "\n" +
			`{"at":"2026-11-24T09:00:00+09:00","finished":"2026-11-24T09:40:00+09:00","late_minutes":35,"outcome":"failed","summary":"接続できませんでした"}` + "\n",
	} {
		dir := filepath.Join(root, "routines/github/runs", month)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "runs.jsonl"), []byte(lines), 0o644)
	}
	h, err := (&Server{Store: store, Version: "test", Schedules: func() ([]schedule.Job, map[string]string) {
		return schedule.Load(root), map[string]string{"github": "2026-11-24T09:00"}
	}}).Handler()
	if err != nil {
		t.Fatal(err)
	}

	_, body, _ := get(t, h, "/routines")
	if !strings.Contains(body, `href="/routines/github"`) {
		t.Error("the list does not link to the page of a job")
	}
	// One failure is not trouble yet; a second one is, and it is shown
	// where the user looks anyway.
	if _, body, _ = get(t, h, "/"); strings.Contains(body, "予約の不調") {
		t.Error("one failed run is shown as trouble")
	}
	file := filepath.Join(root, "routines/github/runs/2026-11/runs.jsonl")
	lines, _ := os.ReadFile(file)
	os.WriteFile(file, append(lines, []byte(`{"at":"2026-11-25T09:00:00+09:00","finished":"2026-11-25T09:01:00+09:00","outcome":"failed","summary":"接続できませんでした"}`+"\n")...), 0o644)
	if _, body, _ = get(t, h, "/"); strings.Contains(body, "予約の不調") || !strings.Contains(body, "要対応") || !strings.Contains(body, "実行されなかった回を除くと、直近2件の記録はすべて失敗でした。") || !strings.Contains(body, `href="/routines/github"`) {
		t.Error("the home page does not show a job that keeps failing")
	}
	if _, body, _ = get(t, h, "/api/schedule-troubles"); !strings.Contains(body, `"key":"schedule:github:failed"`) || !strings.Contains(body, `"path":"/routines/github"`) {
		t.Errorf("troubles = %s", body)
	}
	os.WriteFile(file, lines, 0o644)

	status, body, _ := get(t, h, "/routines/github")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{"GitHubの確認", "11月22日 09:03", "実際に動いた回だけを数えます", "続けて失敗した回数</th><td>1回", "続けて実行されなかった回数</th><td>0回",
		"予定の35分後に依頼を受け付けました。", "失敗しました", "実行されませんでした", "接続できませんでした", "未確認の変更に&lt;b&gt;2件&lt;/b&gt;あります。", `href="/routines/github?month=2026-10"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, "10月31日") {
		t.Error("the page shows another month")
	}
	if _, body, _ = get(t, h, "/routines/github?month=2026-10"); !strings.Contains(body, "10月31日 09:00") || strings.Contains(body, "接続できませんでした") {
		t.Error("the page of October is wrong")
	}
	if _, body, _ = get(t, h, "/routines/github?month=../../x"); !strings.Contains(body, "実行の記録は、まだありません。") {
		t.Error("a month that is not a month was read")
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

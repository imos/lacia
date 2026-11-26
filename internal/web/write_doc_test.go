package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/task"
)

func putDoc(h http.Handler, who caller.Caller, target, body string) (int, map[string]any) {
	req := httptest.NewRequest("PUT", target, strings.NewReader(body))
	if who.Place != "" {
		req.Header.Set(caller.Header, who.Encode())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestWriteDocSavesWithoutARequest(t *testing.T) {
	store, h := setup(t)
	changed := 0
	server := &Server{Store: store, Version: "test", Changed: func() { changed++ }}
	h, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	codex := caller.Caller{Place: "work", Host: "example-pod", Agents: []caller.Agent{{Kind: caller.Codex, PID: 10, Session: "thread-1"}}}
	claude := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}

	// T1002 is waiting for a review; saving its text leaves that alone.
	body := "## いまの状況\n\n結果をまとめました。\n\n## 次にすること\n\n佐藤さんが結果を確認。\n"
	code, out := putDoc(h, claude, "/api/tasks/T1002/doc?name=task.md", body)
	got, _ := store.Get("T1002")
	if code != 200 || got.Body != body || got.Status != task.Review || got.Parent != "T1001" || changed != 1 {
		t.Fatalf("code = %d, out = %v, task = %+v, changed = %d", code, out, got, changed)
	}
	// Wording is advice: the text is saved, and the reply says what to mend.
	if advice, _ := json.Marshal(out["advice"]); !strings.Contains(string(advice), "「確認。」で終わる文") {
		t.Errorf("advice = %s", advice)
	}
	if lines, _ := store.HistoryLines(got, 1); !strings.Contains(lines[0], "task.md を書き換えました") || !strings.Contains(lines[0], "local-claude") {
		t.Errorf("history = %q", lines)
	}

	notes := "# T1002 の補足\n\n## ここまでできたら完了\n\n結果を確認したら完了です。\n\n## してよいことと、お尋ねすること\n\nわたしが結果を整えます。\n\n## 決まったこと\n\n- 結果は表で出します。\n"
	if code, out = putDoc(h, claude, "/api/tasks/T1002/doc?name=notes.md", notes); code != 200 || len(out["advice"].([]any)) != 0 {
		t.Fatalf("notes: %d %v", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(store.Root(), got.Dir, "notes.md")); string(data) != notes {
		t.Errorf("notes.md = %q", data)
	}

	// A broken shape is refused, and nothing is saved.
	code, out = putDoc(h, claude, "/api/tasks/T1002/doc?name=task.md", "---\nstatus: done\n---\n\n"+body)
	if after, _ := store.Get("T1002"); code != 400 || !strings.Contains(out["error"].(string), "冒頭の項目") || after.Body != body || after.Status != task.Review {
		t.Errorf("front matter: %d %v", code, out)
	}
	if code, _ = putDoc(h, claude, "/api/tasks/T1002/doc?name=task.md", body+"\n## 経緯\n\n話です。\n"); code != 400 {
		t.Errorf("unknown heading: %d", code)
	}
	if code, _ = putDoc(h, claude, "/api/tasks/T1002/doc?name=log.jsonl", "{}"); code != 400 {
		t.Errorf("another file: %d", code)
	}
	if code, _ = putDoc(h, claude, "/api/tasks/T9999/doc?name=task.md", body); code != 404 {
		t.Errorf("missing task: %d", code)
	}
	if code, _ = putDoc(h, claude, "/api/tasks/T1002/doc?name=notes.md", "# 補足\n\n"+strings.Repeat("あ", task.MaxDocSize)); code != 400 {
		t.Errorf("too large: %d", code)
	}

	// Writing to a task another conversation works on is taken, with the
	// same warning as a note.
	as(t, h, codex, "POST", "/api/tasks/T1001", `{"status":"doing"}`)
	parent := "## いまの状況\n\n進めています。\n\n## 次にすること\n\nわたしが続けます。\n"
	code, out = putDoc(h, claude, "/api/tasks/T1001/doc?name=task.md", parent)
	if warning, _ := out["warning"].(string); code != 200 || !strings.Contains(warning, "いま work-codex が進めています") {
		t.Errorf("owned by another: %d %v", code, out)
	}
	if code, out = putDoc(h, codex, "/api/tasks/T1001/doc?name=task.md", parent); code != 200 || out["warning"] != "" {
		t.Errorf("owner: %d %v", code, out)
	}
	if got, _ := store.Get("T1001"); got.Owner != "work-codex" || got.Status != task.Doing {
		t.Errorf("owner changed: %+v", got)
	}
}

func TestWriteTaskReasonAndRender(t *testing.T) {
	store, h := setup(t)
	body := "## ひとことで\n\n予定の表示を直します。\n\n## このタスクを作ったわけ\n\n祝日が分からず、予定を決めにくいことがありました。\n\n## 次にすること\n\n" + task.OwnerName + "に、表示を見ていただきます。\n"
	code, out := putDoc(h, caller.Caller{}, "/api/tasks/T1002/doc?name=task.md", body)
	if code != http.StatusOK || len(out["advice"].([]any)) != 0 {
		t.Fatalf("save: %d %v", code, out)
	}
	got, _ := store.Get("T1002")
	if got.Body != body || got.Status != task.Review {
		t.Fatalf("read back: %+v", got)
	}
	code, html, _ := get(t, h, "/tasks/T1002")
	if code != http.StatusOK || !strings.Contains(html, "このタスクを作ったわけ") || !strings.Contains(html, "祝日が分からず、予定を決めにくいことがありました。") || strings.Contains(html, "という見出しは task.md では使いません") {
		t.Fatalf("render: %d %s", code, html)
	}
	misplaced := strings.Replace(body, "## ひとことで\n\n予定の表示を直します。\n\n", "", 1) + "\n## ひとことで\n\n予定の表示を直します。\n"
	code, out = putDoc(h, caller.Caller{}, "/api/tasks/T1002/doc?name=task.md", misplaced)
	after, _ := store.Get("T1002")
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "直後") || after.Body != body {
		t.Fatalf("misplaced: %d %v", code, out)
	}
}

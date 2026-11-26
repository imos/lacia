package web

import (
	"fmt"
	"github.com/imos/lacia/internal/testrecords"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/task"
)

// send is like as, but returns the status instead of failing on a refusal.
func send(t *testing.T, h http.Handler, who caller.Caller, method, target, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if who.Place != "" {
		req.Header.Set(caller.Header, who.Encode())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestALoopOfTheSameWritesIsStopped(t *testing.T) {
	root := t.TempDir()
	store, err := task.Open(testrecords.Init(t, root))
	if err != nil {
		t.Fatal(err)
	}
	doing, _ := store.Create(task.NewTask{Area: "work", Title: "回収するタスク", Status: task.Doing})
	other, _ := store.Create(task.NewTask{Area: "work", Title: "別のタスク", Status: task.Doing})
	now := time.Date(2026, 11, 19, 0, 9, 0, 0, task.JST)
	saves := 0
	server := &Server{Store: store, Version: "test", Now: func() time.Time { return now }, Changed: func() { saves++ }}
	h, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	script := caller.Caller{} // a command no conversation ran
	claude := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}
	logLines := func(id string) []string {
		t.Helper()
		got, _ := store.Get(id)
		data, _ := readTestHistory(t, filepath.Join(root, got.Dir))
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}

	// The script of 2026-11-19: the same file and the same note, every ten seconds.
	round := func() (int, int) {
		file, _ := send(t, h, script, "PUT", "/api/tasks/"+doing.ID+"/files?name=result.json", `{"sharpe":1}`)
		note, _ := send(t, h, script, "POST", "/api/tasks/"+doing.ID, `{"log":"結果を保存して読み戻しました。"}`)
		now = now.Add(10 * time.Second)
		return file, note
	}
	for i := 1; i < floodRepeats; i++ {
		if file, note := round(); file != http.StatusOK || note != http.StatusOK {
			t.Fatalf("round %d was refused: %d %d", i, file, note)
		}
	}
	// The tenth copy of the file is the last write taken.
	if file, note := round(); file != http.StatusOK || note != http.StatusTooManyRequests {
		t.Fatalf("the round that shows the loop: %d %d", file, note)
	}
	got, _ := store.Get(doing.ID)
	if got.Status != task.Review || got.StoppedAt == "" || got.Owner != "" {
		t.Fatalf("after the loop was found: status=%s stopped_at=%q owner=%q", got.Status, got.StoppedAt, got.Owner)
	}
	lines := logLines(doing.ID)
	if last := strings.Join(lines, "\n"); !strings.Contains(last, "10回繰り返されました") || !strings.Contains(last, "コマンド からこのタスクへの書き込みを、00:40 まで止めます") || !strings.Contains(last, "「確認待ち」に変えました") {
		t.Fatalf("log = %q", last)
	}
	before := len(lines)

	// From now on the script is refused, and writes nothing more.
	for i := 0; i < 50; i++ {
		file, note := round()
		if file != http.StatusTooManyRequests || note != http.StatusTooManyRequests {
			t.Fatalf("a stopped source wrote: %d %d", file, note)
		}
	}
	code, body := send(t, h, script, "POST", "/api/requests", fmt.Sprintf(`{"task":%q,"text":"終わりました。"}`, doing.ID))
	if code != http.StatusTooManyRequests || !strings.Contains(body, "00:40 まで") {
		t.Fatalf("a request from the stopped source: %d %s", code, body)
	}
	if after := len(logLines(doing.ID)); after != before {
		t.Fatalf("the log grew from %d to %d lines while stopped", before, after)
	}

	// Other tasks, conversations and the browser are not in the way.
	if code, _ := send(t, h, script, "POST", "/api/tasks/"+other.ID, `{"log":"別のタスクへの記録です。"}`); code != http.StatusOK {
		t.Errorf("the script's write to another task: %d", code)
	}
	if code, _ := send(t, h, claude, "POST", "/api/tasks/"+doing.ID, `{"log":"登録を外して止めました。"}`); code != http.StatusOK {
		t.Errorf("a conversation's write: %d", code)
	}
	form := httptest.NewRequest("POST", "/tasks/"+doing.ID+"/status", strings.NewReader("status=doing"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, form)
	if got, _ = store.Get(doing.ID); got.Status != task.Doing || got.StoppedAt != "" {
		t.Errorf("after the user took it up again: %d status=%s stopped_at=%q", rec.Code, got.Status, got.StoppedAt)
	}

	// After the pause the source may write again; a loop that still runs is stopped anew.
	now = time.Date(2026, 11, 19, 0, 41, 0, 0, task.JST)
	for i := 1; i < floodRepeats; i++ {
		if file, note := round(); file != http.StatusOK || note != http.StatusOK {
			t.Fatalf("after the pause, round %d was refused: %d %d", i, file, note)
		}
	}
	round()
	if got, _ = store.Get(doing.ID); got.Status != task.Review || got.StoppedAt == "" {
		t.Errorf("second stop: status=%s stopped_at=%q", got.Status, got.StoppedAt)
	}
	if file, _ := round(); file != http.StatusTooManyRequests {
		t.Errorf("after the second stop: %d", file)
	}
	if saves == 0 {
		t.Error("the stop was not saved")
	}
}

func TestManyDifferentWritesReportTheirTotal(t *testing.T) {
	store, _ := setup(t)
	now := time.Now()
	s := &Server{Store: store, Now: func() time.Time { return now }}
	source := floodSource("T1001", caller.Session{}, false)
	for i := 1; i < floodWrites; i++ {
		if _, _, stopped := s.flood.record(source, fmt.Sprint(i), now); stopped {
			t.Fatalf("stopped after %d writes", i)
		}
	}
	s.floodWrote("T1001", caller.Session{}, false, "履歴", "last note")
	if log := logOf(t, store, "T1001"); !strings.Contains(log, fmt.Sprintf("書き込みが%d回に達しました", floodWrites)) {
		t.Fatalf("missing total at the threshold: %s", log)
	}
}

func TestOrdinaryWorkIsNotALoop(t *testing.T) {
	store, h := setup(t)
	claude := caller.Caller{Place: "local", Host: "mac", Agents: []caller.Agent{{Kind: caller.Claude, PID: 20, Session: "s-1"}}}
	// Many different files and notes at once, as when a directory is attached.
	for i := 0; i < 300; i++ {
		if code, body := send(t, h, claude, "PUT", fmt.Sprintf("/api/tasks/T1001/files?name=out/%03d.json", i), fmt.Sprintf(`{"n":%d}`, i)); code != http.StatusOK {
			t.Fatalf("file %d: %d %s", i, code, body)
		}
		if code, body := send(t, h, claude, "POST", "/api/tasks/T1001", fmt.Sprintf(`{"log":"%d件目を確かめました。"}`, i)); code != http.StatusOK {
			t.Fatalf("note %d: %d %s", i, code, body)
		}
	}
	// The same directory attached again a few times while a report is revised.
	for round := 0; round < floodRepeats-2; round++ {
		for i := 0; i < 5; i++ {
			if code, body := send(t, h, claude, "PUT", fmt.Sprintf("/api/tasks/T1001/files?name=out/%03d.json", i), fmt.Sprintf(`{"n":%d}`, i)); code != http.StatusOK {
				t.Fatalf("round %d: %d %s", round, code, body)
			}
		}
	}
	if got, _ := store.Get("T1001"); got.Status != task.Doing || got.StoppedAt != "" {
		t.Errorf("ordinary work was stopped: status=%s stopped_at=%q", got.Status, got.StoppedAt)
	}
}

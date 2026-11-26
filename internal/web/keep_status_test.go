package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/notify"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

func TestKeepStatusThroughRequestLifecycle(t *testing.T) {
	for _, status := range []task.Status{task.Review, task.Doing, task.Todo, task.Waiting, task.Done, task.Cancelled} {
		for _, outcome := range []string{"success", "failed", "uncertain", "concurrent-change"} {
			t.Run(string(status)+"/"+outcome, func(t *testing.T) {
				store, _ := setup(t)
				if _, _, err := store.SetStatus("T1002", status, "test", ""); err != nil {
					t.Fatal(err)
				}
				before, _ := store.Get("T1002")
				logPath := filepath.Join(store.Root(), before.Dir, task.HistoryFile)
				logBefore, _ := os.ReadFile(logPath)
				dir := t.TempDir()
				q, err := request.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				h, err := (&Server{Store: store, Requests: q}).Handler()
				if err != nil {
					t.Fatal(err)
				}
				send := func(body string) *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(body)))
					return rec
				}
				body := `{"id":"keep-test","task":"T1002","text":"記録を保存してください。","status_policy":"keep"}`
				rec := send(body)
				if rec.Code != http.StatusCreated {
					t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
				}
				var saved request.Request
				if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
					t.Fatal(err)
				}
				if saved.StatusPolicy != request.KeepStatus || saved.Was != "" {
					t.Fatalf("request: %+v", saved)
				}
				current, _ := store.Get("T1002")
				if current.Status != status {
					t.Fatalf("accept changed status: %s", current.Status)
				}
				logAfter, _ := os.ReadFile(logPath)
				if strings.Contains(strings.TrimPrefix(string(logAfter), string(logBefore)), "状態を") {
					t.Fatal("accept wrote a status transition")
				}
				// Simulate restarting the worker before it processes the persisted request.
				q, err = request.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				saved, _ = q.Get(saved.ID)
				if saved.StatusPolicy != request.KeepStatus {
					t.Fatal("restart lost policy")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				q.Finished = func(request.Request) { cancel() }
				wantStatus := status
				q.Work(ctx, false, func(ctx context.Context, prompt string) (string, string, error) {
					if request.Current(ctx).StatusPolicy != request.KeepStatus || !strings.Contains(prompt, "処理中も終了後もタスクの状態を変えず") || strings.Contains(prompt, "から doing に変えて") {
						t.Errorf("policy lost in runner: %s", prompt)
					}
					if outcome == "concurrent-change" {
						wantStatus = task.Waiting
						store.SetStatus("T1002", wantStatus, "other", "")
					}
					if outcome == "failed" {
						return "", "turn", errors.New("fake failure")
					}
					if outcome == "uncertain" {
						return "", "turn", request.ErrUncertain
					}
					return "保存しました。", "turn", nil
				}, nil)
				current, _ = store.Get("T1002")
				if current.Status != wantStatus {
					t.Fatalf("completion restored stale status: %s", current.Status)
				}
				h, _ = (&Server{Store: store, Requests: q}).Handler()
				// A retry after completion must not queue work or move the current status.
				if rec := send(body); rec.Code != http.StatusOK {
					t.Fatalf("retry: %d", rec.Code)
				}
				if q.Pending() != 0 || len(q.Recent(10, "T1002", false)) != 1 {
					t.Fatal("retry created work")
				}
				q, err = request.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				saved, _ = q.Get(saved.ID)
				want := request.Done
				if outcome == "failed" {
					want = request.Failed
				}
				if outcome == "uncertain" {
					want = request.Unknown
				}
				if saved.Status != want || saved.StatusPolicy != request.KeepStatus {
					t.Fatalf("persisted outcome: %+v", saved)
				}
			})
		}
	}
}

func TestKeepStatusValidationAndNotificationGeneration(t *testing.T) {
	store, _ := setup(t)
	q, _ := request.Open(t.TempDir())
	h, _ := (&Server{Store: store, Requests: q}).Handler()
	send := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(body)))
		return rec
	}
	for _, body := range []string{`{"text":"record","status_policy":"keep"}`, `{"task":"T1002","text":"record","status_policy":"unknown"}`, `{"task":"T9999","text":"record","status_policy":"keep"}`} {
		if rec := send(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid: %d %s", rec.Code, rec.Body.String())
		}
	}
	state := notify.NewState()
	now := time.Now()
	plan := func() []notify.Notice {
		v, _ := store.Get("T1002")
		return notify.PlanTasks([]task.Task{v}, state, now, "https://example.test")
	}
	store.SetStatus("T1002", task.Doing, "test", "")
	plan()
	store.SetStatus("T1002", task.Review, "test", "")
	first := plan()
	if len(first) != 1 {
		t.Fatal("missing initial notification")
	}
	for _, policy := range []string{`"keep"`, `"keep"`} {
		rec := send(`{"id":"record","task":"T1002","text":"record","status_policy":` + policy + `}`)
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		if got := plan(); len(got) != 0 {
			t.Fatal("record-only request notified again")
		}
	}
	rec := send(`{"id":"normal-work","task":"T1002","text":"work"}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	var saved request.Request
	json.Unmarshal(rec.Body.Bytes(), &saved)
	if saved.Was != "review" || saved.StatusPolicy != "" {
		t.Fatalf("normal behavior changed: %+v", saved)
	}
	plan()
	store.SetStatus("T1002", task.Review, "test", "")
	next := plan()
	if len(next) != 1 || next[0].Generation != first[0].Generation+1 {
		t.Fatalf("normal work did not notify: %+v", next)
	}
	// Retrying an ordinary request must not undo a later transition either.
	send(`{"id":"normal-work","task":"T1002","text":"work"}`)
	current, _ := store.Get("T1002")
	if current.Status != task.Review {
		t.Fatal("retry moved task")
	}
}

package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
)

func TestRoutineReportAndNotifications(t *testing.T) {
	s, h := setupRoutine(t)
	root := s.Store.Root()
	routing, err := schedule.LoadRouting("", root, "work", "codex", "codex")
	if err != nil {
		t.Fatal(err)
	}
	d, err := schedule.OpenDispatch(filepath.Join(t.TempDir(), "dispatch.json"), routing)
	if err != nil {
		t.Fatal(err)
	}
	s.ScheduleDispatch = d
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.Local)
	job := schedule.Job{ID: "S1001", Name: "S1001", LegacyName: "github", Slot: now, Output: "matters", Title: "GitHubの確認"}
	s.Schedules = func() ([]schedule.Job, map[string]string) { return []schedule.Job{job}, nil }
	attempts := 0
	log := &schedule.RunLog{Root: root, State: filepath.Join(t.TempDir(), "pending.json"), Execution: d.Get,
		Review: func(id string, run schedule.Run, report routine.Result) error {
			attempts++
			if attempts == 1 {
				return errors.New("simulated review write failure")
			}
			_, _, err := s.Routines.Accept(id, report)
			return err
		}}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest(method, path, strings.NewReader(body)))
		return out
	}
	run := func(id string, body string, failFirst bool) {
		t.Helper()
		if err := d.Enqueue(id, job, "", now); err != nil {
			t.Fatal(err)
		}
		if err := log.Begin(job, id, now); err != nil {
			t.Fatal(err)
		}
		path := "/api/routines/S1001/runs/" + id
		if out := call(http.MethodPost, path, body); out.Code != 200 {
			t.Fatalf("report: %d %s", out.Code, out.Body)
		}
		if out := call(http.MethodPost, path, body); out.Code != 200 {
			t.Fatalf("retry: %d %s", out.Code, out.Body)
		}
		if out := call(http.MethodPost, path, `{"summary":"another result"}`); out.Code != 409 {
			t.Fatalf("conflicting result: %d", out.Code)
		}
		if err := d.SyncLocal(id, "done", schedule.Result{Settled: true, Finished: now}); err != nil {
			t.Fatal(err)
		}
		if err := log.Settle(d.Result, now); (err != nil) != failFirst {
			t.Fatalf("settle: %v", err)
		}
		if err := log.Settle(d.Result, now); err != nil {
			t.Fatal(err)
		}
	}
	batch := `{"summary":"2件です。","matters":[{"key":"github/1","revision":"a"},{"key":"github/2","revision":"b"}]}`
	run("sched-github-2026-11-25-0900", batch, true)
	job.Slot = job.Slot.Add(time.Hour)
	run("sched-github-2026-11-25-1000", batch, false)
	job.Slot = job.Slot.Add(time.Hour)
	run("sched-github-2026-11-25-1100", `{"nothing":true,"summary":"0件です。"}`, false)
	if got := schedule.ReadRuns(root, "S1001", "2026-11"); len(got) != 3 || got[0].Outcome != schedule.RunNothing {
		t.Fatalf("runs=%+v", got)
	}
	for range 2 {
		out := call(http.MethodGet, "/api/routine-notices", "")
		if out.Code != 200 || strings.Count(out.Body.String(), "routine_id") != 1 || !strings.Contains(out.Body.String(), `"count":2`) {
			t.Fatalf("outbox: %d %s", out.Code, out.Body)
		}
	}
	for range 2 {
		out := call(http.MethodPost, "/api/routine-notices/S1001/1", "")
		if out.Code != 204 {
			t.Fatalf("notice ack: %d %s", out.Code, out.Body)
		}
	}
	if out := call(http.MethodGet, "/api/routine-notices", ""); out.Code != 200 || strings.TrimSpace(out.Body.String()) != "[]" {
		t.Fatalf("acknowledged outbox: %d %s", out.Code, out.Body)
	}
	snapshot, err := routine.NewStore(root).Snapshot("S1001")
	if err != nil || len(snapshot.Pending) != 1 || !snapshot.Pending[0].Notified {
		t.Fatalf("restored=%+v %v", snapshot, err)
	}
}

func TestPartialCoverageKeepsRealFindingsReviewable(t *testing.T) {
	s, h := setupRoutine(t)
	routing, err := schedule.LoadRouting("", s.Store.Root(), "work", "codex", "codex")
	if err != nil {
		t.Fatal(err)
	}
	d, err := schedule.OpenDispatch(filepath.Join(t.TempDir(), "dispatch.json"), routing)
	if err != nil {
		t.Fatal(err)
	}
	s.ScheduleDispatch = d
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	job := schedule.Job{ID: "S1001", Name: "S1001", Output: "matters", CheckResult: true, Slot: now, Enabled: true}
	s.Schedules = func() ([]schedule.Job, map[string]string) { return []schedule.Job{job}, nil }
	log := &schedule.RunLog{Root: s.Store.Root(), State: filepath.Join(t.TempDir(), "pending.json"), Execution: d.Get, Review: func(id string, _ schedule.Run, r routine.Result) error {
		_, _, err := s.Routines.Accept(id, r)
		return err
	}}
	call := func(path, body string) int {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return out.Code
	}
	partial := `{"version":1,"check_status":"partial","summary":"読めた新着が1件あります。","checks":[{"scope":"public","status":"verified"},{"scope":"private","status":"unverified","reason":"denied"}],"matters":[{"key":"new-one","revision":"1","summary":"読めた新着です。"}]}`
	unavailable := `{"version":1,"check_status":"unavailable","nothing":true,"checks":[{"scope":"private","status":"unverified","reason":"denied"}]}`
	for n, body := range []string{partial, unavailable} {
		id := fmt.Sprintf("run-%d", n)
		job.Slot = now.Add(time.Duration(n) * time.Hour)
		if err := d.Enqueue(id, job, "", now); err != nil {
			t.Fatal(err)
		}
		if err := log.Begin(job, id, now); err != nil {
			t.Fatal(err)
		}
		path := "/api/routines/S1001/runs/" + id
		if got := call(path, body); got != 200 {
			t.Fatalf("report %d", got)
		}
		if got := call(path, body); got != 200 {
			t.Fatalf("retry %d", got)
		}
		if err := d.SyncLocal(id, "done", schedule.Result{Settled: true, Finished: now}); err != nil {
			t.Fatal(err)
		}
		if err := log.Settle(d.Result, now); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.Routines.Snapshot("S1001")
	if err != nil || len(snap.Pending) != 1 || len(snap.Pending[0].Matters) != 1 || len(snap.Pending[0].Unverified) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/routines/S1001", nil))
	if out.Code != 200 || !strings.Contains(out.Body.String(), "一部を確認できませんでした") || !strings.Contains(out.Body.String(), `href="/routines/S1001/receipts/1"`) {
		t.Fatalf("overview %d %s", out.Code, out.Body)
	}
	out = httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/routines/S1001/receipts/1", nil))
	if out.Code != 200 || !strings.Contains(out.Body.String(), "読めた新着です。") || !strings.Contains(out.Body.String(), "まだ確かめられていない範囲") {
		t.Fatalf("detail %d %s", out.Code, out.Body)
	}
	out = httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/routine-notices", nil))
	if out.Code != 200 || strings.Count(out.Body.String(), "routine_id") != 1 || !strings.Contains(out.Body.String(), `"receipt_page":true`) {
		t.Fatalf("notices %d %s", out.Code, out.Body)
	}
}

func TestCoverageForNoneOutputAndLegacyNames(t *testing.T) {
	s, h := setupRoutine(t)
	routing, _ := schedule.LoadRouting("", s.Store.Root(), "work", "codex", "codex")
	d, _ := schedule.OpenDispatch(filepath.Join(t.TempDir(), "dispatch.json"), routing)
	s.ScheduleDispatch = d
	now := time.Now()
	for _, job := range []schedule.Job{{ID: "S1001", Name: "S1001", Output: "none", CheckResult: true}, {Name: "old-check", CheckResult: true}} {
		id := "run-" + job.Name
		job.Slot = now
		if err := d.Enqueue(id, job, "", now); err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("POST", "/api/routines/"+job.Name+"/runs/"+id, strings.NewReader(`{"check_status":"unavailable","nothing":true,"checks":[{"scope":"cluster/a","status":"unverified"}]}`)))
		if out.Code != 200 {
			t.Fatalf("%s %d %s", job.Name, out.Code, out.Body)
		}
	}
}

func TestExpectedScopesCannotBeDroppedAndFullCoverageRecovers(t *testing.T) {
	s, h := setupRoutine(t)
	routing, _ := schedule.LoadRouting("", s.Store.Root(), "work", "codex", "codex")
	d, err := schedule.OpenDispatch(filepath.Join(t.TempDir(), "dispatch.json"), routing)
	if err != nil {
		t.Fatal(err)
	}
	s.ScheduleDispatch = d
	base := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	expected := []string{"cluster:site-a/auth", "cluster:site-b/auth", "cluster:site-c/auth"}
	log := &schedule.RunLog{Root: s.Store.Root(), State: filepath.Join(t.TempDir(), "pending.json"), Execution: d.Get}
	for n := 0; n < 3; n++ {
		now := base.Add(time.Duration(n) * time.Hour)
		id := fmt.Sprintf("fixed-%d", n)
		job := schedule.Job{ID: "S1001", Name: "S1001", Output: "none", CheckResult: true, CheckScopes: append([]string(nil), expected...), Slot: now}
		if err := d.Enqueue(id, job, "", now); err != nil {
			t.Fatal(err)
		}
		if err := log.Begin(job, id, now); err != nil {
			t.Fatal(err)
		}
		// A later definition edit and a caller's returned snapshot cannot change this occurrence.
		job.CheckScopes[0] = "another-scope"
		copy, _ := d.Get(id)
		copy.Job.CheckScopes[0] = "another-scope"
		report := func(body string) int {
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest("POST", "/api/routines/S1001/runs/"+id, strings.NewReader(body)))
			return out.Code
		}
		if code := report(`{"check_status":"complete","nothing":true}`); code != 400 {
			t.Fatalf("empty complete accepted: %d", code)
		}
		if code := report(`{"check_status":"complete","nothing":true,"checks":[{"scope":"cluster:site-a/auth","status":"verified"},{"scope":"cluster:site-b/auth","status":"verified"}]}`); code != 409 {
			t.Fatalf("omitted scope accepted: %d", code)
		}
		status, checkStatus := "unavailable", "unverified"
		if n == 2 {
			status, checkStatus = "complete", "verified"
		}
		checks := []routine.Check{}
		for _, scope := range expected {
			checks = append(checks, routine.Check{Scope: scope, Status: checkStatus})
		}
		body, _ := json.Marshal(routine.Result{Version: 1, CheckStatus: status, Nothing: true, Checks: checks})
		if code := report(string(body)); code != 200 {
			t.Fatalf("full fixed checklist: %d %s", code, body)
		}
		if err := d.SyncLocal(id, "done", schedule.Result{Settled: true, Finished: now}); err != nil {
			t.Fatal(err)
		}
		if err := log.Settle(d.Result, now); err != nil {
			t.Fatal(err)
		}
		health := schedule.HealthOf(s.Store.Root(), "S1001")
		if n < 2 && len(health.Unverified) != 3 {
			t.Fatalf("missing scopes: %+v", health)
		}
		if n == 2 && (len(health.Unverified) != 0 || !health.LastSuccess.Equal(now)) {
			t.Fatalf("full recovery: %+v", health)
		}
	}
}

package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/testrecords"
	"github.com/imos/lacia/internal/todo"
)

func setupRoutine(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	root := t.TempDir()
	store, err := task.Open(testrecords.Init(t, root))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "areas/work/routines/S1001")
	if err = os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "routine.md"), []byte("---\nid: S1001\ntitle: GitHubの確認\narea: work\ndaily: \"09:00\"\nlegacy_name: github\n---\n\n確認してください。"), 0644); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, Routines: routine.NewStore(root)}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return s, h
}
func acceptRoutine(t *testing.T, s *Server, id, run string) {
	t.Helper()
	_, _, err := s.Routines.Accept(id, routine.Result{RunID: run, Summary: run + "の結果です。"})
	if err != nil {
		t.Fatal(err)
	}
}
func viewToken(t *testing.T, h http.Handler, id string) string {
	t.Helper()
	code, body, _ := get(t, h, "/routines/"+id)
	if code != 200 {
		t.Fatalf("detail %d: %s", code, body)
	}
	m := regexp.MustCompile(`name="view" value="([^"]+)"`).FindStringSubmatch(body)
	if len(m) < 2 {
		t.Fatalf("missing completion form: %s", body)
	}
	return m[1]
}
func TestRoutineCompletePreservesLateResults(t *testing.T) {
	s, h := setupRoutine(t)
	acceptRoutine(t, s, "S1001", "09時")
	token := viewToken(t, h, "S1001")
	other := viewToken(t, h, "S1001")
	acceptRoutine(t, s, "S1001", "08時の遅着")
	for _, v := range []string{token, token, other} {
		rec := post(h, "/routines/S1001/complete", "", url.Values{"view": {v}})
		if rec.Code != 303 {
			t.Fatalf("complete=%d %s", rec.Code, rec.Body.String())
		}
	}
	snap, err := s.Routines.Snapshot("S1001")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pending) != 1 || snap.Pending[0].RunID != "08時の遅着" {
		t.Fatalf("late results lost: %+v", snap)
	}
	_, body, _ := get(t, h, "/")
	if strings.Count(body, `class="id" href="/routines/S1001"`) < 1 || !strings.Contains(body, "未確認の結果が1回分") {
		t.Fatalf("home lacks routine: %s", body)
	}
	token = viewToken(t, h, "S1001")
	post(h, "/routines/S1001/complete", "", url.Values{"view": {token}})
	_, body, _ = get(t, h, "/")
	if strings.Contains(body, "未確認の結果が") {
		t.Error("completed routine still in action list")
	}
	// Read history again through a new store to prove completion survives restart.
	reopened := routine.NewStore(s.Store.Root())
	snap, err = reopened.Snapshot("S1001")
	if err != nil || len(snap.Pending) != 0 {
		t.Fatalf("restart: %+v %v", snap, err)
	}
	acceptRoutine(t, s, "S1001", "次の日")
	_, body, _ = get(t, h, "/")
	if !strings.Contains(body, "未確認の結果が1回分") {
		t.Error("next run did not return to home")
	}
}
func TestRoutineBoundaryCannotBeChangedOrReusedForAnotherRoutine(t *testing.T) {
	s, h := setupRoutine(t)
	acceptRoutine(t, s, "S1001", "最初")
	token := viewToken(t, h, "S1001")
	acceptRoutine(t, s, "S1001", "新着")
	raw, sig, _ := strings.Cut(token, ".")
	data, _ := base64.RawURLEncoding.DecodeString(raw)
	var view routineView
	json.Unmarshal(data, &view)
	view.Through = 2
	data, _ = json.Marshal(view)
	forged := base64.RawURLEncoding.EncodeToString(data) + "." + sig
	if rec := post(h, "/routines/S1001/complete", "", url.Values{"view": {forged}}); rec.Code != 409 {
		t.Fatalf("tampered boundary accepted: %d", rec.Code)
	}
	if _, err := s.readRoutineToken(token, "S1002"); err == nil {
		t.Fatal("token applied to another routine")
	}
	if rec := post(h, "/routines/S1001/complete", "http://evil.example", url.Values{"view": {token}}); rec.Code != 403 {
		t.Fatal("cross-origin completion accepted")
	}
	if rec := post(h, "/routines/S1001/complete", "", url.Values{}); rec.Code != 409 {
		t.Fatal("unsigned completion accepted")
	}
}
func TestRoutineLinksAndEscaping(t *testing.T) {
	s, h := setupRoutine(t)
	_, _, err := s.Routines.Accept("S1001", routine.Result{RunID: "one", Summary: "<script>bad()</script>", Reference: "javascript:bad()"})
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := get(t, h, "/routines")
	if code != 200 || !strings.Contains(body, "S1001") || !strings.Contains(body, "1回分") {
		t.Fatalf("list: %d %s", code, body)
	}
	_, body, _ = get(t, h, "/routines/S1001")
	if strings.Contains(body, "<script>bad()") || strings.Contains(body, `href="javascript:`) {
		t.Error("external text or URL was not escaped")
	}
	if got := repoURL("routines/S1001/routine.md"); got != "/routines/S1001" {
		t.Fatal(got)
	}
	if code, _, _ := get(t, h, "/routines/S9999"); code != 404 {
		t.Fatal(code)
	}
}

func TestRoutineDefinitionLinkUsesRecordedPath(t *testing.T) {
	for _, tc := range []struct {
		name, definition, config string
	}{
		{"format 2", "areas/work/routines/S1001/routine.md", `{"format": 2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testrecords.Init(t, t.TempDir())
			if err := os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, tc.definition)
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("---\nid: S1001\ntitle: 朝の確認\ndaily: 09:00\n---\n\n確認してください。\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			store, err := task.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{Store: store, Routines: routine.NewStore(root)}
			h, err := s.Handler()
			if err != nil {
				t.Fatal(err)
			}
			code, body, _ := get(t, h, "/routines/S1001")
			link := regexp.MustCompile(`<a href="([^"]+)">定義を開く</a>`).FindStringSubmatch(body)
			if code != 200 || len(link) != 2 || link[1] != "/files/"+tc.definition {
				t.Fatalf("detail %d, definition link %v", code, link)
			}
			if code, body, _ := get(t, h, link[1]); code != 200 || !strings.Contains(body, "確認してください。") {
				t.Fatalf("definition %d: %s", code, body)
			}
			code, _, header := get(t, h, "/files/routines/S1001/routine.md?view=source")
			if code != http.StatusTemporaryRedirect || header.Get("Location") != link[1]+"?view=source" {
				t.Fatalf("old definition %d, Location %q", code, header.Get("Location"))
			}
			if code, _, _ := get(t, h, header.Get("Location")); code != 200 {
				t.Fatalf("redirect target %d", code)
			}
			for _, invalid := range []string{"S9999/routine.md", "S1001/%2e%2e%2f%2e%2e%2f.lacia/config.json"} {
				if code, _, _ := get(t, h, "/files/routines/"+invalid); code != 404 {
					t.Errorf("invalid definition path %s returned %d", invalid, code)
				}
			}
		})
	}
}

func TestRoutineLegacyURLPreservesHistoryFilter(t *testing.T) {
	_, h := setupRoutine(t)
	code, _, header := get(t, h, "/schedules/github?month=2026-10")
	if code != http.StatusPermanentRedirect || header.Get("Location") != "/routines/S1001?month=2026-10" {
		t.Fatalf("redirect=%d %s", code, header.Get("Location"))
	}
}

func TestTaskAfterRunFormAPIAndFailedResult(t *testing.T) {
	s, h := setupRoutine(t)
	tk, err := s.Store.Create(task.NewTask{Area: "work", Title: "定期タスクを待つ"})
	if err != nil {
		t.Fatal(err)
	}
	page := "/tasks/" + tk.ID
	form := page + "/after-run"
	if rec := post(h, form, "", url.Values{"after_run": {"no-such-job"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown routine form status = %d", rec.Code)
	}
	if rec := post(h, form, "", url.Values{"after_run": {"S1001"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("after-run form = %d: %s", rec.Code, rec.Body)
	}
	if rec := post(h, form, "", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("after-run clear = %d: %s", rec.Code, rec.Body)
	}
	if got, _ := s.Store.Get(tk.ID); got.AfterRun != "" || got.AfterRunSince != "" {
		t.Fatalf("after-run clear left %q and %q", got.AfterRun, got.AfterRunSince)
	}
	req := httptest.NewRequest(http.MethodPost, "/api"+page, strings.NewReader(`{"field":"after_run","value":"github"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("after_run API = %d: %s", rec.Code, rec.Body)
	}
	if rec := post(h, page+"/on-ready", "", url.Values{"start": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("on-ready form = %d: %s", rec.Code, rec.Body)
	}
	started, _ := s.Store.Get(tk.ID)
	since, err := time.Parse(time.RFC3339Nano, started.AfterRunSince)
	if started.AfterRun != "S1001" || err != nil {
		t.Fatalf("after_run = %q, registration = %q (%v)", started.AfterRun, started.AfterRunSince, err)
	}
	if _, body, _ := get(t, h, page); !strings.Contains(body, `value="S1001">GitHubの確認`) || !strings.Contains(body, `href="/routines/S1001">S1001</a>次回終了後再開`) {
		t.Fatalf("task page lacks configured routine: %s", body)
	}
	if body := todo.Render(s.Store, since); !strings.Contains(body, "定期タスク S1001次回終了後再開") {
		t.Fatalf("TODO lacks registered wait: %s", body)
	}
	loc := s.Store.Location()
	run := schedule.Run{ID: "test-run", At: since.Add(time.Minute).In(loc), Finished: since.Add(2 * time.Minute), Outcome: schedule.RunFailed, Summary: "検証用の失敗です。"}
	month := run.At.Format("2006-01")
	root := s.Store.Root()
	dir := filepath.Join(root, "areas", "work", "routines", "S1001", "runs", month)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.jsonl"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	now := run.Finished.Add(time.Minute)
	woken := s.Store.WakeWithRuns(now, func(name string, since time.Time) (task.RoutineCompletion, bool) {
		return schedule.FirstCompletion(root, name, since, now, loc)
	})
	ref := "/routines/S1001?month=" + month + "#" + run.Anchor()
	if len(woken) != 1 || woken[0].Status != task.Review || woken[0].StartsWhenReady() || woken[0].ReadyRunRef != ref {
		t.Fatalf("failed run woke %+v; want review and link %s", woken, ref)
	}
	for _, url := range []string{"/", page} {
		if _, body, _ := get(t, h, url); !strings.Contains(body, `href="`+ref+`">待っていた実行の結果`) {
			t.Fatalf("%s lacks link to %s: %s", url, ref, body)
		}
	}
	if body := todo.Render(s.Store, now); !strings.Contains(body, "[待っていた実行の結果]("+ref+")") {
		t.Fatalf("TODO lacks result link: %s", body)
	}
	if code, body, _ := get(t, h, "/routines/S1001?month="+month); code != http.StatusOK || !strings.Contains(body, `id="`+run.Anchor()+`"`) || !strings.Contains(body, run.Summary) {
		t.Fatalf("result page = %d: %s", code, body)
	}
}

func TestRoutineReceiptPageAndIndividualCompletion(t *testing.T) {
	for _, tc := range []struct {
		config, definition string
	}{
		{`{"format": 2}`, "areas/work/routines/S1001/routine.md"},
	} {
		t.Run(tc.definition, func(t *testing.T) {
			root := testrecords.Init(t, t.TempDir())
			if err := os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, tc.definition)
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("---\nid: S1001\ntitle: 朝の確認\narea: work\ndaily: 09:00\nlegacy_name: daily\n---\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			store, err := task.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			routines := routine.NewStore(root)
			routines.Now = func() time.Time { return time.Date(2026, 11, 1, 9, 0, 0, 0, task.JST) }
			s := &Server{Store: store, Routines: routines}
			h, err := s.Handler()
			if err != nil {
				t.Fatal(err)
			}
			acceptRoutine(t, s, "S1001", "first")
			_, created, err := routines.Accept("S1001", routine.Result{
				RunID: "second", Summary: "短い概要です。\n\nこの詳しい文は一覧に出しません。<script>bad()</script>",
				Reference: "/files/report.md", Matters: []routine.Matter{{Key: "one", Revision: "one", Summary: "依頼の詳しい内容です。", Reference: "javascript:bad()"}},
				Unverified: []string{"確認できなかった詳しい対象"},
			})
			if err != nil || !created {
				t.Fatalf("accept: created=%t err=%v", created, err)
			}

			run := schedule.Run{ID: "second", At: time.Date(2026, 10, 31, 9, 0, 0, 0, task.JST), Outcome: schedule.RunPartial, Summary: "実行の概要"}
			b, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			runDir := filepath.Join(filepath.Dir(file), "runs", "2026-10")
			if err := os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runDir, "runs.jsonl"), append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}

			const page = "/routines/S1001/receipts/2"
			for _, route := range []string{"/", "/routines/S1001"} {
				code, body, _ := get(t, h, route)
				if code != 200 || !strings.Contains(body, `href="`+page+`"`) || strings.Contains(body, "この詳しい文は一覧に出しません") {
					t.Fatalf("summary and link missing or long text in %s (%d)", route, code)
				}
				if route == "/routines/S1001" && (!strings.Contains(body, `id="receipt-2"`) || strings.Count(body, `href="`+page+`"`) < 2) {
					t.Fatal("missing original anchor or link from run history")
				}
			}
			for _, invalid := range []string{"/routines/S9999/receipts/2", "/routines/S1001/receipts/3", "/routines/S1001/receipts/0", "/routines/S1001/receipts/02", "/routines/S1001/receipts/no"} {
				if code, _, _ := get(t, h, invalid); code != 404 {
					t.Fatalf("GET %s = %d, want 404", invalid, code)
				}
			}
			if code, _, head := get(t, h, "/routines/daily/receipts/2"); code != http.StatusPermanentRedirect || head.Get("Location") != page {
				t.Fatalf("alias = %d %q", code, head.Get("Location"))
			}
			code, body, _ := get(t, h, page)
			for _, want := range []string{"この詳しい文は一覧に出しません", "依頼の詳しい内容", "確認できなかった詳しい対象", "未確認です", "&lt;script&gt;bad()", `href="/files/report.md"`,
				`href="/routines/S1001?month=2026-10#` + run.Anchor() + `"`, `action="` + page + `/complete"`} {
				if code != 200 || !strings.Contains(body, want) {
					t.Fatalf("detail=%d missing %q", code, want)
				}
			}
			if strings.Contains(body, "<script>bad()") || strings.Contains(body, `href="javascript:`) {
				t.Fatal("receipt includes unescaped external content")
			}
			token := regexp.MustCompile(`name="view" value="([^"]+)"`).FindStringSubmatch(body)[1]
			form := url.Values{"view": {token}}
			for _, other := range []string{"/routines/S1001/complete", "/routines/S1001/receipts/1/complete"} {
				if rec := post(h, other, "", form); rec.Code != 409 {
					t.Fatalf("wrong endpoint %s accepted the token: %d", other, rec.Code)
				}
			}
			if rec := post(h, page+"/complete", "http://other.example", form); rec.Code != 403 {
				t.Fatalf("cross-origin completion returned %d", rec.Code)
			}
			acceptRoutine(t, s, "S1001", "late")
			for range 2 {
				if rec := post(h, page+"/complete", "", form); rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), page+"?") {
					t.Fatalf("individual completion = %d %s", rec.Code, rec.Body.String())
				}
			}
			state, err := routines.Snapshot("S1001")
			if err != nil || len(state.Pending) != 2 || state.Pending[0].Sequence != 1 || state.Pending[1].Sequence != 3 {
				t.Fatalf("individual completion changed other receipts: %+v, %v", state, err)
			}
			_, body, _ = get(t, h, page)
			if !strings.Contains(body, "確認済みです") || !strings.Contains(body, "確認の履歴") || strings.Contains(body, "/complete") {
				t.Fatal("reviewed receipt lacks confirmation history or still offers a confirmation form")
			}
			_, body, _ = get(t, h, "/")
			if !strings.Contains(body, `href="`+page+`">受付2</a>を確認しました。`) {
				t.Fatal("recent individual confirmation does not link to its receipt")
			}

			s.DoorOnly = true
			req := httptest.NewRequest(http.MethodGet, page, nil)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			if out.Code != 403 {
				t.Fatalf("untrusted entry returned %d", out.Code)
			}
		})
	}
}

func TestRoutineFailureUsesExistingReview(t *testing.T) {
	s, h := setupRoutine(t)
	job := schedule.Job{
		ID: "S1001", Name: "S1001", CheckResult: true, Output: "matters", ExplicitMatters: true,
		CheckScopes: []string{"cluster:site-a/auth", "cluster:site-b/auth", "cluster:site-c/auth"},
	}
	base := time.Date(2026, 11, 27, 9, 0, 0, 0, task.JST)
	var report routine.Result
	log := &schedule.RunLog{
		Root: s.Store.Root(), State: stateFile(t, filepath.Join(t.TempDir(), "pending.json")),
		Execution: func(string) (schedule.Execution, bool) {
			return schedule.Execution{Job: job, Report: &report}, true
		},
		Review: func(id string, _ schedule.Run, result routine.Result) error {
			_, _, err := s.Routines.Accept(id, result)
			return err
		},
	}
	const failure = "3拠点とも通信エラーでPod一覧を取得できませんでした。"
	const reason = "通信が許可されませんでした"
	settle := func(id string, slot, finished time.Time, failed bool) {
		t.Helper()
		job.Slot = slot
		report = routine.Result{RunID: id, CheckStatus: "complete", Nothing: true}
		for _, scope := range job.CheckScopes {
			check := routine.Check{Scope: scope, Status: "verified"}
			if failed {
				check.Status, check.Reason = "unverified", reason
			}
			report.Checks = append(report.Checks, check)
		}
		if failed {
			report.CheckStatus, report.Summary = "unavailable", failure
		}
		if err := log.Begin(job, id, slot); err != nil {
			t.Fatal(err)
		}
		lookup := func(string) (schedule.Result, bool) { return schedule.Result{Settled: true, Finished: finished}, true }
		if err := log.Settle(lookup, finished); err != nil {
			t.Fatal(err)
		}
	}
	assertPage := func(path string, present, absent []string) {
		t.Helper()
		status, body, _ := get(t, h, path)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, status, body)
		}
		for _, want := range present {
			if !strings.Contains(body, want) {
				t.Errorf("%s does not include %q", path, want)
			}
		}
		for _, unwanted := range absent {
			if strings.Contains(body, unwanted) {
				t.Errorf("%s still includes %q", path, unwanted)
			}
		}
	}
	settle("failed-first", base, base.Add(time.Minute), true)
	assertPage("/", []string{"要対応", "未確認の結果が1回分", failure, "cluster:site-c/auth：" + reason,
		`href="/routines/S1001/receipts/1"`}, []string{"予約の不調"})
	assertPage("/routines", []string{`href="/routines/S1001#problem"`, failure}, nil)
	assertPage("/routines/S1001", []string{failure, "cluster:site-b/auth：" + reason}, nil)
	assertPage("/routines/S1001/receipts/1", []string{failure, "cluster:site-a/auth：" + reason, "この回の実行履歴を開く"}, nil)

	token := viewToken(t, h, "S1001")
	if rec := post(h, "/routines/S1001/complete", "", url.Values{"view": {token}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	assertPage("/", nil, []string{"未確認の結果が", failure})
	assertPage("/routines/S1001", []string{failure, "cluster:site-b/auth：" + reason}, nil)
	assertPage("/routines/S1001/receipts/1", []string{failure, "確認済みです。"}, nil)

	settle("same-failure", base.Add(time.Hour), base.Add(time.Hour+time.Minute), true)
	assertPage("/", nil, []string{"未確認の結果が", failure})
	settle("success", base.Add(3*time.Hour), base.Add(3*time.Hour+time.Minute), false)
	assertPage("/routines", nil, []string{failure, "#problem"})
	settle("older-failed", base.Add(2*time.Hour), base.Add(3*time.Hour+2*time.Minute), true)
	assertPage("/", nil, []string{"未確認の結果が", failure})
	assertPage("/routines/S1001", []string{failure}, []string{`id="problem"`})
	settle("failed-again", base.Add(4*time.Hour), base.Add(4*time.Hour+time.Minute), true)
	assertPage("/", []string{"未確認の結果が1回分", failure, `href="/routines/S1001/receipts/2"`}, []string{"予約の不調"})
}

func TestExistingReceiptDisplaysItsStoredFailure(t *testing.T) {
	s, h := setupRoutine(t)
	acceptRoutine(t, s, "S1001", "previously-accepted")
	path := filepath.Join(s.Store.Root(), "areas", "work", "routines", "S1001", "runs", "2026-11")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "runs.jsonl"), []byte(`{"at":"2026-11-27T09:00:00+09:00","outcome":"partial","id":"previously-accepted","summary":"別の範囲を確認できませんでした。","checks":[{"scope":"cluster","status":"unverified","reason":"通信エラー"}]}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/", "/routines/S1001/receipts/1"} {
		code, body, _ := get(t, h, path)
		if code != 200 || !strings.Contains(body, "cluster：通信エラー") {
			t.Errorf("the existing receipt lacks the error on %s: %d", path, code)
		}
	}
	snap, err := s.Routines.Snapshot("S1001")
	if err != nil || len(snap.Pending) != 1 || snap.Pending[0].Sequence != 1 || snap.Pending[0].ProblemKey != "" {
		t.Fatalf("historical receipt changed: %+v, %v", snap, err)
	}
}

package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupDispatch(t *testing.T) (*Dispatch, string, Routing) {
	t.Helper()
	dir := t.TempDir()
	secret := filepath.Join(dir, "device.secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("a", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRouting(fmt.Sprintf(`{"local":{"device":"desk","secret_file":%q}}`, secret), "", "work", "", "codex", "claude")
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDispatch(stateFile(t, filepath.Join(dir, "dispatch.json")), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d, secret, cfg
}
func testRemoteJob() Job {
	return Job{Name: "daily", To: "local-codex", Daily: "06:00", Slot: time.Date(2026, 11, 24, 6, 0, 0, 0, time.FixedZone("JST", 9*3600))}
}
func TestRoutingDeviceAuthentication(t *testing.T) {
	d, secret, cfg := setupDispatch(t)
	if !cfg.Authorized("desk", strings.Repeat("a", 40)) || cfg.Authorized("stranger", strings.Repeat("a", 40)) || cfg.Authorized("desk", "wrong") {
		t.Fatal("authentication")
	}
	if dest, device, err := cfg.Route(""); err != nil || dest.String() != "work-codex" || device != LocalDevice {
		t.Fatalf("default %s %s %v", dest, device, err)
	}
	if dest, device, err := cfg.Route("local"); err != nil || dest.String() != "local-codex" || device != "desk" {
		t.Fatalf("remote default %s %s %v", dest, device, err)
	}
	alone, err := LoadRouting(`{"server":{"device":"local","default":"claude"}}`, "", "", "", "codex", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if dest, device, err := alone.Route("server"); err != nil || dest.String() != "local-claude" || device != LocalDevice {
		t.Fatalf("single machine routing %s %s %v", dest, device, err)
	}
	if _, err := LoadRouting(fmt.Sprintf(`{"local":{"device":"desk","secret_file":%q},"other":{"device":"other","secret_file":%q}}`, secret, secret), "", "work", "", "codex"); err == nil {
		t.Fatal("shared device credential accepted")
	}
	os.Chmod(secret, 0644)
	if d.Routing.Authorized("desk", strings.Repeat("a", 40)) {
		t.Fatal("public credential accepted")
	}
}
func TestDispatchOfflineAuthAndRestart(t *testing.T) {
	d, _, cfg := setupDispatch(t)
	job := testRemoteJob()
	id := requestID(job.Name, job.Slot.Format(slotLayout))
	now := time.Now()
	if err := d.Enqueue(id, job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	if d.List("")[0].Status != WaitingConnection {
		t.Fatal("absent device must wait")
	}
	if err := d.Enqueue(id, job, "changed prompt", now); err != nil {
		t.Fatal(err)
	}
	if err := d.Enqueue("next", job, "prompt", now); err == nil {
		t.Fatal("overlapping occurrence accepted")
	}
	if _, err := d.Poll("desk", DeviceStatus{Status: WaitingAuth, At: now}); err != nil {
		t.Fatal(err)
	}
	if d.List("")[0].Status != WaitingAuth {
		t.Fatal("auth status missing")
	}
	if err := d.Start("other", id, strings.Repeat("c", 32), now); err == nil {
		t.Fatal("wrong device")
	}
	if err := d.Start("desk", id, strings.Repeat("c", 32), now); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDispatch(d.State, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.List("")[0].Status != Executing {
		t.Fatal("started occurrence was reset")
	}
	if err := reopened.Start("desk", id, strings.Repeat("c", 32), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !reopened.List("")[0].Started.Equal(now) {
		t.Fatal("idempotent start changed time")
	}
	res := Result{Settled: true, Reply: "報告はありません。", Finished: now.Add(time.Minute)}
	if err := reopened.Finish("desk", id, strings.Repeat("c", 32), res, false); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Finish("desk", id, strings.Repeat("c", 32), Result{Settled: true, Failed: true}, false); err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Result(id)
	if got.Failed || got.Reply != res.Reply {
		t.Fatal("replayed result overwrote final")
	}
	if len(reopened.Events("desk")) != 1 {
		t.Fatal("auth observation not retained")
	}
}

// This transport intentionally loses the response after the server durably
// accepted a result. A fresh worker must resend it without executing again.
func workerServer(t *testing.T, d *Dispatch, dropFinish *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.Routing.Authorized("desk", strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
			w.WriteHeader(401)
			return
		}
		var err error
		var out any = struct{}{}
		switch {
		case strings.HasSuffix(r.URL.Path, "/poll"):
			var s DeviceStatus
			json.NewDecoder(r.Body).Decode(&s)
			out, err = d.Poll("desk", s)
		case strings.HasSuffix(r.URL.Path, "/start"):
			var in StartRequest
			json.NewDecoder(r.Body).Decode(&in)
			err = d.Start("desk", "run", in.Claim, time.Now())
		case strings.HasSuffix(r.URL.Path, "/finish"):
			var in FinishRequest
			json.NewDecoder(r.Body).Decode(&in)
			err = d.Finish("desk", "run", in.Claim, in.Result, in.Unknown)
			if *dropFinish {
				*dropFinish = false
				w.WriteHeader(503)
				return
			}
		default:
			t.Errorf("unexpected URL %s", r.URL.Path)
		}
		if err != nil {
			w.WriteHeader(409)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
}
func TestWorkerResultOutboxAndAuthPrerequisite(t *testing.T) {
	d, secret, _ := setupDispatch(t)
	job := testRemoteJob()
	if err := d.Enqueue("run", job, "prompt", time.Now()); err != nil {
		t.Fatal(err)
	}
	drop := true
	server := workerServer(t, d, &drop)
	defer server.Close()
	executed := 0
	state := filepath.Join(t.TempDir(), "worker.json")
	config := WorkerConfig{URL: server.URL, Device: "desk", SecretFile: secret, Root: t.TempDir()}
	w := &Worker{Config: config, State: stateFile(t, state), RunConversation: func(context.Context, Execution, string) (string, error) { executed++; return "done", nil }}
	// Authentication is needed before the API itself can be reached.
	w.Config.AuthCheck = []string{"sh", "-c", "exit 77"}
	w.Config.URL = "http://127.0.0.1:1"
	if err := w.Tick(context.Background()); err == nil {
		t.Fatal("offline transport expected")
	}
	w.Config.URL = server.URL
	if w.data.Status != WaitingAuth || len(w.data.Events) != 1 || executed != 0 {
		t.Fatal("auth prerequisite not persisted locally")
	}
	w.Config.AuthCheck = nil
	if err := w.Tick(context.Background()); err == nil {
		t.Fatal("lost response not simulated")
	}
	if executed != 1 {
		t.Fatal("did not run once")
	}
	if w.data.Runs["run"].Phase != "finished" {
		t.Fatal("outbox not saved")
	}
	w2 := &Worker{Config: config, State: stateFile(t, state), RunConversation: w.RunConversation}
	if err := w2.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executed != 1 || w2.data.Runs["run"].Phase != "acknowledged" {
		t.Fatal("result was rerun or not acknowledged")
	}
	res, _ := d.Result("run")
	if !res.Settled || res.Reply != "done" {
		t.Fatal("missing result")
	}
	events := d.Events("desk")
	found := false
	for _, e := range events {
		found = found || e.Status == WaitingAuth
	}
	if !found {
		t.Fatal("offline auth observation not replayed")
	}
}
func TestWorkerStartedIsNeverReplayed(t *testing.T) {
	d, secret, _ := setupDispatch(t)
	job := testRemoteJob()
	d.Enqueue("run", job, "prompt", time.Now())
	d.Start("desk", "run", strings.Repeat("c", 32), time.Now())
	drop := false
	srv := workerServer(t, d, &drop)
	defer srv.Close()
	state := filepath.Join(t.TempDir(), "worker.json")
	e, _ := d.Get("run")
	stateFile(t, state).writeJSON(workerState{Runs: map[string]workerRun{"run": {Execution: e, Phase: "started", Claim: strings.Repeat("c", 32)}}})
	executed := 0
	w := &Worker{Config: WorkerConfig{URL: srv.URL, Device: "desk", SecretFile: secret, Root: t.TempDir()}, State: stateFile(t, state), RunConversation: func(context.Context, Execution, string) (string, error) { executed++; return "", nil }}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executed != 0 {
		t.Fatal("uncertain run executed again")
	}
	got, _ := d.Get("run")
	if got.Status != Unknown || !got.Result.Failed {
		t.Fatal("unknown outcome not preserved")
	}
	if err := d.Enqueue("next-day", job, "prompt", time.Now()); err != nil {
		t.Fatal("unknown older result blocks all future days", err)
	}
}
func TestWorkerPrepareResumesAfterStartResponseLost(t *testing.T) {
	d, secret, _ := setupDispatch(t)
	job := testRemoteJob()
	d.Enqueue("run", job, "prompt", time.Now())
	d.Start("desk", "run", strings.Repeat("c", 32), time.Now())
	e, _ := d.Get("run")
	state := filepath.Join(t.TempDir(), "worker.json")
	stateFile(t, state).writeJSON(workerState{Runs: map[string]workerRun{"run": {Execution: e, Phase: "prepared", Claim: strings.Repeat("c", 32)}}})
	drop := false
	srv := workerServer(t, d, &drop)
	defer srv.Close()
	count := 0
	w := &Worker{Config: WorkerConfig{URL: srv.URL, Device: "desk", SecretFile: secret, Root: t.TempDir()}, State: stateFile(t, state), RunConversation: func(context.Context, Execution, string) (string, error) { count++; return "", nil }}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("prepared run not resumed safely")
	}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("repeated poll reran")
	}
}
func TestWorkerStateCorruptionFailsClosed(t *testing.T) {
	state := filepath.Join(t.TempDir(), "worker.json")
	os.WriteFile(state, []byte("broken"), 0600)
	w := &Worker{State: stateFile(t, state)}
	if err := w.load(); err == nil {
		t.Fatal("corruption ignored")
	}
	if _, err := OpenDispatch(stateFile(t, state), Routing{}); err == nil {
		t.Fatal("central corruption ignored")
	}
	// HTTP endpoints outside a secure tunnel cannot receive the credential.
	w.Config = WorkerConfig{URL: "http://example.org", Device: "desk", Root: t.TempDir(), SecretFile: state}
	w.RunConversation = func(context.Context, Execution, string) (string, error) { return "", nil }
	if err := w.Validate(); err == nil {
		t.Fatal("insecure URL accepted")
	}
}

func TestRoutingNullPlacesKeepsServerDefault(t *testing.T) {
	cfg, err := LoadRouting("null", "", "", "claude", "codex", "claude")
	if err != nil {
		t.Fatal(err)
	}
	dest, device, err := cfg.Route("")
	if err != nil || dest.String() != "local-claude" || device != LocalDevice {
		t.Fatalf("%s %s %v", dest, device, err)
	}
}

func TestUnknownScheduleDestinationDoesNotRunAndNeedsAttention(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "schedules")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"unknown":     "to: elsewhere-claude",
		"unavailable": "to: work-claude",
		"old":         "target: mac",
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\ndaily: 06:00\n"+content+"\n---\nCheck.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	routing, err := LoadRouting("", "", "work", "", "codex")
	if err != nil {
		t.Fatal(err)
	}
	jobs := routing.Check(Load(root))
	if len(jobs) != 3 {
		t.Fatal(jobs)
	}
	for _, job := range jobs {
		if job.Err == "" || job.Enabled {
			t.Fatalf("accepted unavailable destination: %#v", job)
		}
	}
	if got := Troubles(root, jobs, nil); len(got) != 3 {
		t.Fatalf("invalid schedules missing from health: %#v", got)
	}
	runner := Runner{Root: root, State: stateFile(t, filepath.Join(t.TempDir(), "slots")), Check: routing.Check, Submit: func(id string, job Job) error {
		t.Fatalf("attempted to run %s", id)
		return nil
	}}
	if err := runner.Tick(time.Date(2026, 11, 24, 6, 0, 0, 0, time.FixedZone("JST", 9*3600))); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalAssignmentRetainsItsDeviceAndDisplayName(t *testing.T) {
	_, _, routing := setupDispatch(t)
	state := filepath.Join(t.TempDir(), "dispatch.json")
	old := `{"executions":{"old":{"id":"old","job":{"Name":"daily","Target":"mac"},"target":"mac","device":"desk","status":"waiting_connection"}}}`
	if err := os.WriteFile(state, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDispatch(stateFile(t, state), routing)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Enqueue("old", Job{Name: "daily", To: "work-claude"}, "new prompt", time.Now()); err != nil {
		t.Fatal(err)
	}
	e, ok := d.Get("old")
	if !ok || e.Device != "desk" || e.DestinationLabel() != "mac" || e.Kind != "" {
		t.Fatalf("historical assignment changed: %+v, found: %t", e, ok)
	}
}

func TestDispatchRejectsSecondWorkerClaimAndRollsBack(t *testing.T) {
	d, _, _ := setupDispatch(t)
	job := testRemoteJob()
	now := time.Now()
	if err := d.Enqueue("run", job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	claim := strings.Repeat("c", 32)
	if err := d.Start("desk", "run", claim, now); err != nil {
		t.Fatal(err)
	}
	if err := d.Start("desk", "run", strings.Repeat("d", 32), now); err == nil {
		t.Fatal("second worker claimed same occurrence")
	}
	if err := d.Finish("desk", "run", strings.Repeat("d", 32), Result{Settled: true}, false); err == nil {
		t.Fatal("other worker finished occurrence")
	}
	polled, err := d.Poll("desk", DeviceStatus{Status: "ready", At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if polled[0].Claim != "" || d.List("")[0].Claim != "" {
		t.Fatal("claim leaked through observation")
	}
	// A failed write must remove newly inserted map keys from memory as well.
	bad := filepath.Join(t.TempDir(), "file")
	os.WriteFile(bad, []byte("x"), 0600)
	d.State = StateFile{Dir: stateFile(t, bad).Dir, Name: filepath.Join("file", "state.json")}
	next := testRemoteJob()
	next.Name = "other"
	if err := d.Enqueue("not-saved", next, "prompt", now); err == nil {
		t.Fatal("storage failure expected")
	}
	if _, ok := d.Get("not-saved"); ok {
		t.Fatal("failed insert survived rollback")
	}
}
func TestCommonExecutionHistoryIncludesLocalAndRemote(t *testing.T) {
	d, _, cfg := setupDispatch(t)
	job := testRemoteJob()
	now := time.Now()
	if err := d.Enqueue("mac-waiting", job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	local := Job{Name: "server-check", To: "work-claude", Every: time.Hour, Slot: job.Slot}
	for _, id := range []string{"server-1", "server-2"} {
		if err := d.Enqueue(id, local, "local prompt", now); err != nil {
			t.Fatal(err)
		}
		if err := d.SyncLocal(id, "done", Result{Settled: true, Reply: "報告はありません。", Started: now, Finished: now}); err != nil {
			t.Fatal(err)
		}
		local.Slot = local.Slot.Add(time.Hour)
	}
	d, err := OpenDispatch(d.State, cfg)
	if err != nil {
		t.Fatal(err)
	}
	all := d.List("")
	if len(all) != 3 {
		t.Fatal("same-result intervals compressed from central history", len(all))
	}
	mac, _ := d.Get("mac-waiting")
	if mac.Status != WaitingConnection {
		t.Fatal("server waited for the local device")
	}
	// The timestamp keeps its actual slot. Daily aggregation may subtract six
	// hours in JST; the coordinator never rewrites occurrence dates or IDs.
	early := time.Date(2026, 11, 25, 5, 59, 0, 0, time.FixedZone("JST", 9*3600))
	local.Name = "early-check"
	local.Slot = early
	d.Enqueue("early", local, "prompt", now)
	e, _ := d.Get("early")
	if e.Job.Slot.Format("2006-01-02 15:04") != "2026-11-25 05:59" || e.Job.Slot.Add(-6*time.Hour).Format(time.DateOnly) != "2026-11-24" {
		t.Fatal("daily observation boundary")
	}
}
func TestAuthWaitIsSentWhenAPIIsReachable(t *testing.T) {
	d, secret, _ := setupDispatch(t)
	job := testRemoteJob()
	d.Enqueue("run", job, "prompt", time.Now())
	drop := false
	srv := workerServer(t, d, &drop)
	defer srv.Close()
	w := &Worker{Config: WorkerConfig{URL: srv.URL, Device: "desk", SecretFile: secret, Root: t.TempDir(), AuthCheck: []string{"sh", "-c", "exit 77"}}, State: stateFile(t, filepath.Join(t.TempDir(), "worker.json")), RunConversation: func(context.Context, Execution, string) (string, error) {
		t.Fatal("auth-waiting work executed")
		return "", nil
	}}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.List("")[0].Status != WaitingAuth {
		t.Fatal("reachable API did not see auth wait")
	}
}

func TestWorkerPassesThePreparedPromptAndRouting(t *testing.T) {
	w := &Worker{RunConversation: func(_ context.Context, e Execution, prompt string) (string, error) {
		if e.ID != "sched-test-2026-11-24-0600" || e.To != "desk-claude" || e.Kind != "claude" || !e.Job.Side {
			t.Fatalf("wrong conversation: %+v", e)
		}
		return prompt, nil
	}}
	e := Execution{ID: "sched-test-2026-11-24-0600", To: "desk-claude", Kind: "claude", Job: Job{Name: "test", Side: true}, Prompt: "check this"}
	reply, err := w.execute(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"システムからのお知らせです。", "check this"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

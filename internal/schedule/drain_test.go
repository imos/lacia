package schedule

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imos/lacia/internal/routine"
)

func TestDrainPreservesMigratedStateUntilRestart(t *testing.T) {
	d, _, cfg := setupDispatch(t)
	now := time.Now()
	job := testRemoteJob()
	job.ID, job.Output = "S1001", "matters"
	if err := d.Enqueue("run", job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	claim := strings.Repeat("c", 32)
	if err := d.Start("desk", "run", claim, now); err != nil {
		t.Fatal(err)
	}
	d.Paused = func() bool { return true }
	// Offline migration updates the persisted name while the old server still
	// holds the former name in memory. Poll must not restore that old snapshot.
	migrated := d.data
	e := migrated.Executions["run"]
	e.Job.Name, e.Job.LegacyName = "S1001", job.Name
	migrated.Executions = map[string]Execution{"run": e}
	if err := d.State.writeJSON(migrated); err != nil {
		t.Fatal(err)
	}
	before, _ := d.State.read()
	if _, err := d.Poll("desk", DeviceStatus{Status: "ready", At: now}); !errors.Is(err, ErrDispatchPaused) {
		t.Fatal(err)
	}
	if err := d.Start("desk", "run", claim, now); !errors.Is(err, ErrDispatchPaused) {
		t.Fatal(err)
	}
	result := Result{Settled: true, Reply: "保存した結果です。", Finished: now}
	if err := d.Finish("desk", "run", claim, result, false); !errors.Is(err, ErrDispatchPaused) {
		t.Fatal(err)
	}
	if err := d.Report("S1001", "run", routine.Result{Summary: "確認しました。"}); !errors.Is(err, ErrDispatchPaused) {
		t.Fatal(err)
	}
	after, _ := d.State.read()
	if !bytes.Equal(before, after) {
		t.Fatal("paused request overwrote migration")
	}
	if old, _ := d.Get("run"); old.Result.Settled || old.Report != nil {
		t.Fatal("paused result was acknowledged in memory")
	}
	restarted, err := OpenDispatch(d.State, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Finish("desk", "run", claim, result, false); err != nil {
		t.Fatal(err)
	}
	actual, _ := restarted.Get("run")
	if actual.Job.Name != "S1001" || !actual.Result.Settled || actual.Result.Reply != result.Reply || !actual.Result.Finished.Equal(result.Finished) {
		t.Fatalf("lost migrated identity or result: %+v", actual)
	}
}

func TestWorkerRetriesFinishedResultAcrossDrain(t *testing.T) {
	d, secret, _ := setupDispatch(t)
	if err := d.Enqueue("run", testRemoteJob(), "prompt", time.Now()); err != nil {
		t.Fatal(err)
	}
	var paused atomic.Bool
	d.Paused = paused.Load
	drop := false
	server := workerServer(t, d, &drop)
	defer server.Close()
	var executed int
	state := filepath.Join(t.TempDir(), "worker.json")
	config := WorkerConfig{URL: server.URL, Device: "desk", SecretFile: secret, Root: t.TempDir()}
	execute := func(context.Context, Execution, string) (string, error) {
		executed++
		paused.Store(true)
		return "保存した結果です。", nil
	}
	w := &Worker{Config: config, State: stateFile(t, state), RunConversation: execute}
	if err := w.Tick(context.Background()); err == nil {
		t.Fatal("paused finish was accepted")
	}
	if executed != 1 || w.data.Runs["run"].Phase != "finished" {
		t.Fatal("result was not retained for retry")
	}
	if res, _ := d.Result("run"); res.Settled {
		t.Fatal("server acknowledged paused finish")
	}
	paused.Store(false)
	// A restarted worker must send the durable finished result, without spawning
	// another conversation or mistaking a temporary pause for authentication.
	w2 := &Worker{Config: config, State: stateFile(t, state), RunConversation: execute}
	if err := w2.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executed != 1 || w2.data.Runs["run"].Phase != "acknowledged" {
		t.Fatal("result was rerun or not acknowledged")
	}
	if res, _ := d.Result("run"); !res.Settled || res.Reply != "保存した結果です。" {
		t.Fatal("finished result was lost")
	}
}

func TestRestartPausesNewClaimsButLetsExistingClaimsFinish(t *testing.T) {
	d, _, cfg := setupDispatch(t)
	now := time.Now()
	job := testRemoteJob()
	job.ID, job.Output = "S1001", "matters"
	if err := d.Enqueue("active", job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	claim := strings.Repeat("c", 32)
	if err := d.Start("desk", "active", claim, now); err != nil {
		t.Fatal(err)
	}
	job.ID, job.Name = "S1002", "next"
	if err := d.Enqueue("next", job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	d.StartsPaused = func() bool { return true }
	if !d.RunningRemotely() {
		t.Fatal("restart would miss the active claim")
	}
	if err := d.Start("desk", "next", claim, now); !errors.Is(err, ErrDispatchPaused) {
		t.Fatalf("new claim was not paused: %v", err)
	}
	if err := d.Start("desk", "active", claim, now); err != nil {
		t.Fatalf("repeat of current claim failed: %v", err)
	}
	if err := d.Report("S1001", "active", routine.Result{Summary: "確認しました。"}); err != nil {
		t.Fatal(err)
	}
	result := Result{Settled: true, Reply: "保存した結果です。", Finished: now}
	if err := d.Finish("desk", "active", claim, result, false); err != nil {
		t.Fatal(err)
	}
	if d.RunningRemotely() {
		t.Fatal("queued claim would prevent restart")
	}
	next, err := OpenDispatch(d.State, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := next.Get("active"); !ok || got.Status != Completed || got.Report == nil || got.Result.Reply != result.Reply {
		t.Fatalf("restart lost completed claim: %+v", got)
	}
	if err := next.Start("desk", "next", claim, now); err != nil {
		t.Fatalf("restart did not release paused claim: %v", err)
	}
}

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func TestScheduleRestorePinsDeviceAcrossRoutingChanges(t *testing.T) {
	for _, initial := range []string{"desk", "local"} {
		t.Run(initial, func(t *testing.T) {
			dir := t.TempDir()
			secretFile := filepath.Join(dir, "secret")
			if err := os.WriteFile(secretFile, []byte(strings.Repeat("a", 40)), 0600); err != nil {
				t.Fatal(err)
			}
			makeRouting := func(device, defaultKind string) schedule.Routing {
				t.Helper()
				conf := fmt.Sprintf(`{"mac":{"device":%q`, device)
				if device != schedule.LocalDevice {
					conf += fmt.Sprintf(`,"secret_file":%q`, secretFile)
				}
				routing, err := schedule.LoadRouting(conf+"}}", "", "work", defaultKind, "codex", "claude")
				if err != nil {
					t.Fatal(err)
				}
				return routing
			}
			routing := makeRouting(initial, "claude")
			d, err := schedule.OpenDispatch(filepath.Join(dir, "dispatch.json"), routing)
			if err != nil {
				t.Fatal(err)
			}
			slot := time.Now()
			job := schedule.Job{Name: "daily", To: "mac-claude", Daily: "06:00", Slot: slot, ReplyTo: "discord:snapshot"}
			id := "sched-daily-2026-11-24-0600"
			if err := d.Enqueue(id, job, "snapshot prompt", slot); err != nil {
				t.Fatal(err)
			}
			// Simulate a restart before the runner's slot and the local queue save.
			next := "local"
			if initial == "local" {
				next = "desk"
			}
			routing = makeRouting(next, "codex")
			d, err = schedule.OpenDispatch(d.State, routing)
			if err != nil {
				t.Fatal(err)
			}
			q, err := request.Open(filepath.Join(dir, "requests"))
			if err != nil {
				t.Fatal(err)
			}
			runs := &schedule.RunLog{Root: dir, State: filepath.Join(dir, "runs.json")}
			for range 2 {
				restored, err := restoreScheduledExecution(q, d, runs, id, time.Now())
				if !restored || err != nil {
					t.Fatal(restored, err)
				}
			}
			r, queued := q.Get(id)
			if queued != (initial == "local") {
				t.Fatalf("initial %s now %s queued %v", initial, next, queued)
			}
			if queued && (r.Text != "snapshot prompt" || r.ReplyTo != "discord:snapshot" || r.Agent != "claude") {
				t.Fatal("snapshot route or prompt changed")
			}
			e, _ := d.Get(id)
			if e.Device != initial || e.Kind != "claude" {
				t.Fatal("occurrence changed device")
			}
		})
	}
}

func TestScheduleRestoreDoesNotReplayStartedLocalWithoutQueueRecord(t *testing.T) {
	dir := t.TempDir()
	routing, err := schedule.LoadRouting("", "", "work", "", "codex")
	if err != nil {
		t.Fatal(err)
	}
	d, err := schedule.OpenDispatch(filepath.Join(dir, "dispatch.json"), routing)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id := "sched-local-2026-11-24-0600"
	job := schedule.Job{Name: "local", Daily: "06:00", Slot: now}
	if err := d.Enqueue(id, job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncLocal(id, request.Running, schedule.Result{Started: now}); err != nil {
		t.Fatal(err)
	}
	q, err := request.Open(filepath.Join(dir, "requests"))
	if err != nil {
		t.Fatal(err)
	}
	runs := &schedule.RunLog{Root: dir, State: filepath.Join(dir, "runs.json")}
	found, err := restoreScheduledExecution(q, d, runs, id, now)
	if !found || err != nil {
		t.Fatal(found, err)
	}
	if _, ok := q.Get(id); ok {
		t.Fatal("started local occurrence was queued again")
	}
	e, _ := d.Get(id)
	if e.Status != schedule.Unknown || !e.Result.Failed || !e.Result.Settled {
		t.Fatal("uncertain local result missing")
	}
	if err := runs.Settle(d.Result, now); err != nil {
		t.Fatal(err)
	}
	if got := schedule.ReadRuns(dir, "local", now.In(task.JST).Format("2006-01")); len(got) != 1 || got[0].Outcome != schedule.RunFailed {
		t.Fatalf("restore must record the uncertain outcome: %+v", got)
	}
}

func TestScheduleRunnerUsesPinnedConversationKind(t *testing.T) {
	queue, err := request.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for id, agent := range map[string]string{"default": "", "claude": "claude", "missing": "other"} {
		if _, _, err := queue.Add(request.Request{ID: id, Kind: request.System, Side: true, Agent: agent, Text: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan request.Request, 3)
	queue.Finished = func(r request.Request) { result <- r }
	go queue.Work(ctx, true, routeRunner(map[string]request.Runner{
		"codex":  func(context.Context, string) (string, string, error) { return "codex", "", nil },
		"claude": func(context.Context, string) (string, string, error) { return "claude", "", nil },
	}, "codex"), nil)
	want := map[string]string{"default": "codex", "claude": "claude", "missing": ""}
	for range want {
		select {
		case got := <-result:
			if got.Reply != want[got.ID] || (got.ID == "missing") != (got.Status == request.Failed) {
				t.Fatalf("unexpected routing result: %#v", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no result from queue")
		}
	}
}

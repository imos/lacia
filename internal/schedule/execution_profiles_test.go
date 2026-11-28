package schedule

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleProfileIsOnlyAName(t *testing.T) {
	for _, profile := range []string{"cluster-read", "../private", "name --yolo", "default"} {
		var j Job
		err := parse("---\nevery: 10m\nprofile: "+profile+"\n---\n確認します。", &j)
		if (err == nil) != (profile == "cluster-read") {
			t.Fatalf("%q: %v", profile, err)
		}
	}
}

func TestExecutionSelectionSurvivesSettlement(t *testing.T) {
	root := newRecords(t, "S1001")
	d, err := OpenDispatch(stateFile(t, root+"/dispatch.json"), Routing{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d.data.Executions["run"] = Execution{ID: "run", Device: LocalDevice, Job: Job{Name: "S1001"}, Status: QueuedLocal}
	if err := d.SelectLocal("run", "mac", "cluster-read", "work"); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncLocal("run", "done", Result{Settled: true, Finished: now}); err != nil {
		t.Fatal(err)
	}
	e, _ := d.Get("run")
	if e.Result.Profile != "cluster-read" || e.Result.Device != "mac" || e.Result.Login != "work" {
		t.Fatalf("lost selection: %+v", e.Result)
	}
	log := &RunLog{Root: root, State: stateFile(t, root+"/pending.json"), Execution: d.Get}
	if err := log.Begin(Job{Name: "S1001", Slot: now}, "run", now); err != nil {
		t.Fatal(err)
	}
	if err := log.Settle(d.Result, now); err != nil {
		t.Fatal(err)
	}
	runs := ReadRuns(root, "S1001", now.In(taskLocation()).Format("2006-01"))
	if len(runs) != 1 || runs[0].Profile != "cluster-read" || runs[0].Device != "mac" || runs[0].Login != "work" {
		t.Fatalf("runs: %+v", runs)
	}
	reopened, err := OpenDispatch(stateFile(t, root+"/dispatch.json"), Routing{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get("run")
	if got.Result.Profile != e.Result.Profile || got.Result.Login != e.Result.Login || got.Result.Device != e.Result.Device {
		t.Fatal("selection did not persist")
	}
}

func taskLocation() *time.Location { loc, _ := time.LoadLocation("Asia/Tokyo"); return loc }

func TestProfileParseRejectsCommandFragments(t *testing.T) {
	var j Job
	if err := parse("---\ndaily: 09:00\nprofile: x; touch /tmp/marker\n---\n確認します。", &j); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatal(err)
	}
}

func TestWorkerRecordsProfileAndRejectsUnknownWithoutLaunching(t *testing.T) {
	for _, known := range []bool{true, false} {
		d, secret, _ := setupDispatch(t)
		job := testRemoteJob()
		job.Profile = "restricted"
		if err := d.Enqueue("run", job, "prompt", time.Now()); err != nil {
			t.Fatal(err)
		}
		drop := false
		server := workerServer(t, d, &drop)
		count := 0
		w := &Worker{Config: WorkerConfig{URL: server.URL, Device: "desk", SecretFile: secret, Root: t.TempDir()}, State: stateFile(t, filepath.Join(t.TempDir(), "worker.json")),
			SelectProfile: func(e Execution) (string, string, error) {
				if e.Job.Profile != "restricted" {
					t.Fatal("lost profile during delivery")
				}
				if !known {
					return "", "", errors.New("実行プロファイルが端末にありません")
				}
				return "restricted", "work", nil
			},
			RunConversation: func(context.Context, Execution, string) (string, error) { count++; return "done", nil },
		}
		if err := w.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		e, _ := d.Get("run")
		if known {
			if count != 1 || e.Result.Profile != "restricted" || e.Result.Login != "work" || e.Result.Device != "desk" {
				t.Fatalf("%d %+v", count, e.Result)
			}
		} else {
			if count != 0 || !e.Result.Failed || !strings.Contains(e.Result.Error, "プロファイル") || e.Result.Profile != "" {
				t.Fatalf("fallback: %d %+v", count, e.Result)
			}
		}
		server.Close()
	}
}

func TestOldDeviceCannotSilentlyIgnoreProfile(t *testing.T) {
	d, _, _ := setupDispatch(t)
	job := testRemoteJob()
	job.Profile = "restricted"
	now := time.Now()
	if err := d.Enqueue("run", job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	runs, err := d.Poll("desk", DeviceStatus{Status: "ready", At: now})
	if err != nil || len(runs) != 0 {
		t.Fatalf("old device received profile: %+v %v", runs, err)
	}
	if got := d.List(""); len(got) != 1 || got[0].Status != "waiting_profile" {
		t.Fatalf("waiting reason not visible: %+v", got)
	}
	runs, err = d.Poll("desk", DeviceStatus{Status: "ready", At: now.Add(time.Second), ExecutionProfiles: true})
	if err != nil || len(runs) != 1 || runs[0].Job.Profile != "restricted" {
		t.Fatalf("new device did not receive profile: %+v %v", runs, err)
	}
}

func TestIntervalHistoryKeepsProfileChangesAndUnverifiedRuns(t *testing.T) {
	for _, field := range []string{"profile", "login", "device", "checks", "failed"} {
		t.Run(field, func(t *testing.T) {
			now := time.Now()
			l := &RunLog{Root: newRecords(t, "S1001")}
			first := Run{ID: "one", At: now, Finished: now, Outcome: RunNothing, Profile: "default", Login: "work", Device: "desk"}
			next := first
			next.ID = "two"
			next.At = now.Add(time.Second)
			next.Finished = next.At
			switch field {
			case "profile":
				next.Profile = "restricted"
			case "login":
				next.Login = "personal"
			case "device":
				next.Device = "other"
			case "checks":
				next.CheckStatus = "partial"
				next.Unverified = []string{"pod/home"}
			case "failed":
				first.Outcome = RunFailed
				next.Outcome = RunFailed
			}
			if err := l.write("S1001", first, true); err != nil {
				t.Fatal(err)
			}
			if err := l.write("S1001", next, true); err != nil {
				t.Fatal(err)
			}
			runs := ReadRuns(l.Root, "S1001", now.In(taskLocation()).Format("2006-01"))
			if len(runs) != 2 {
				t.Fatalf("suppressed %s: %+v", field, runs)
			}
		})
	}
}

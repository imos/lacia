package schedule

import (
	"encoding/json"
	"fmt"
	"github.com/imos/lacia/internal/notify"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/routine"
)

func TestStructuredCheckResults(t *testing.T) {
	for _, tc := range []struct {
		name, status                              string
		nothing, executionFailed, missing, legacy bool
		want                                      string
		review                                    int
	}{
		{name: "normal zero", status: "complete", nothing: true, want: RunNothing},
		{name: "all unavailable", status: "unavailable", nothing: true, want: RunFailed, review: 1},
		{name: "partial findings", status: "partial", want: RunPartial, review: 1},
		{name: "partial without findings", status: "partial", nothing: true, want: RunPartial, review: 1},
		{name: "execution failure overrides report", status: "complete", executionFailed: true, want: RunFailed, review: 1},
		{name: "required report missing", missing: true, want: RunFailed, review: 1},
		{name: "legacy report", legacy: true, want: RunDone, review: 1},
		{name: "legacy partial", status: "partial", legacy: true, nothing: true, want: RunPartial, review: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
			job := Job{Name: "S1001", ID: "S1001", Output: "matters", CheckResult: !tc.legacy, Slot: now}
			report := &routine.Result{CheckStatus: tc.status, Nothing: tc.nothing, Summary: "結果です。"}
			switch tc.status {
			case "complete":
				report.Checks = []routine.Check{{Scope: "one", Status: "verified"}}
			case "unavailable":
				report.Checks = []routine.Check{{Scope: "one", Status: "unverified", Reason: "connection_failed"}}
			case "partial":
				report.Checks = []routine.Check{{Scope: "one", Status: "unverified"}, {Scope: "two", Status: "verified"}}
			}
			if !tc.nothing {
				report.Matters = []routine.Matter{{Key: "new", Revision: "1"}}
			}
			if tc.missing {
				report = nil
			}
			root := t.TempDir()
			reviews := 0
			log := &RunLog{Root: root, State: filepath.Join(t.TempDir(), "pending.json"), Execution: func(string) (Execution, bool) { return Execution{Job: job, Report: report}, true }, Review: func(string, Run, routine.Result) error { reviews++; return nil }}
			if err := log.Begin(job, "run-one", now); err != nil {
				t.Fatal(err)
			}
			lookup := func(string) (Result, bool) {
				return Result{Settled: true, Reply: "報告はありません。でも接続できませんでした。", Failed: tc.executionFailed, Finished: now}, true
			}
			for range 2 {
				if err := log.Settle(lookup, now); err != nil {
					t.Fatal(err)
				}
			}
			runs := ReadRuns(root, job.Name, "2026-11")
			if len(runs) != 1 || runs[0].Outcome != tc.want || reviews != tc.review {
				t.Fatalf("runs=%+v reviews=%d", runs, reviews)
			}
			if tc.status == "partial" && len(runs[0].Unverified) != 1 {
				t.Fatalf("scope missing: %+v", runs[0])
			}
		})
	}
}

func TestFrequentCoverageAndRecovery(t *testing.T) {
	root := t.TempDir()
	log := &RunLog{Root: root}
	base := time.Date(2026, 11, 30, 14, 0, 0, 0, time.UTC)
	bad := func(n int, scope, outcome string) Run {
		return Run{ID: fmt.Sprint(n), At: base.Add(time.Duration(n) * time.Hour), Finished: base.Add(time.Duration(n) * time.Hour), Outcome: outcome, Device: "desk", CheckStatus: "partial", Checks: []routine.Check{{Scope: scope, Status: "unverified", Reason: "denied", CauseKey: "verified-shared-cause"}}}
	}
	// Frequent failures are never compressed, and the month boundary is transparent.
	for n := 0; n < 3; n++ {
		run := bad(n, "cluster/a", RunPartial)
		if err := log.write("S1001", run, true); err != nil {
			t.Fatal(err)
		}
		if err := log.write("S1001", run, true); err != nil {
			t.Fatal(err)
		}
	}
	h := HealthOf(root, "S1001")
	if h.Unverified["cluster/a"].Count != 3 || !h.LastSuccess.IsZero() {
		t.Fatalf("health=%+v", h)
	}
	job := Job{Name: "S1001", Enabled: true}
	if got := Troubles(root, []Job{job}, nil); len(got) != 1 {
		t.Fatalf("troubles=%+v", got)
	}
	// A late success predating the failures cannot clear the current scope trouble.
	old := Run{ID: "old", At: base.Add(-time.Hour), Finished: base, Outcome: RunNothing, Checks: []routine.Check{{Scope: "cluster/a", Status: "verified"}}}
	if err := log.write("S1001", old, false); err != nil {
		t.Fatal(err)
	}
	if h := HealthOf(root, "S1001"); h.Unverified["cluster/a"].Count != 3 {
		t.Fatalf("late recovery: %+v", h)
	}
	// A new partial result may recover one scope while leaving another unread.
	recovery := bad(3, "cluster/b", RunPartial)
	recovery.Checks = append(recovery.Checks, routine.Check{Scope: "cluster/a", Status: "verified"})
	if err := log.write("S1001", recovery, true); err != nil {
		t.Fatal(err)
	}
	if h := HealthOf(root, "S1001"); h.Unverified["cluster/a"].Count != 0 || h.Unverified["cluster/b"].Count != 1 || !h.LastSuccess.Equal(old.Finished) {
		t.Fatalf("recovery=%+v", h)
	}
	if got := Troubles(root, []Job{job}, nil); len(got) != 0 {
		t.Fatalf("recovered=%+v", got)
	}
}

func TestCommonCauseGroupsOnlyAfterEachJobsThreshold(t *testing.T) {
	root := t.TempDir()
	log := &RunLog{Root: root}
	base := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	jobs := []Job{{Name: "first", Title: "最初の確認", Enabled: true}, {Name: "second", Title: "次の確認", Enabled: true}}
	for _, job := range jobs {
		for n := 0; n < 2; n++ {
			run := Run{ID: fmt.Sprintf("%s-%d", job.Name, n), At: base.Add(time.Duration(n) * time.Hour), Outcome: RunFailed, Device: "desk", Checks: []routine.Check{{Scope: "cluster/a", Status: "unverified", Reason: "denied", CauseKey: "one-cause"}}}
			if err := log.write(job.Name, run, true); err != nil {
				t.Fatal(err)
			}
			if n == 0 && job.Name == "first" {
				if got := Troubles(root, jobs, nil); len(got) != 0 {
					t.Fatal(got)
				}
			}
		}
	}
	got := Troubles(root, jobs, nil)
	if len(got) != 1 || got[0].Name != "" || got[0].Path != "/routines" {
		t.Fatalf("group=%+v", got)
	}
	// Identical prose/reason without a confirmed shared cause stays separate.
	for _, job := range jobs {
		run := Run{ID: job.Name + "-new", At: base.Add(3 * time.Hour), Outcome: RunFailed, Device: "desk", Checks: []routine.Check{{Scope: "cluster/a", Status: "unverified", Reason: "denied"}}}
		if err := log.write(job.Name, run, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := Troubles(root, jobs, nil); len(got) != 2 {
		t.Fatalf("unproven cause grouped: %+v", got)
	}
}

func TestCheckResultDefinitionAndPrompt(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		root := t.TempDir()
		path := "routines/S1001/routine.md"
		header := "id: S1001\noutput: none\n"
		if legacy {
			path = "schedules/check.md"
			header = ""
		}
		putDefinition(t, root, path, "---\n"+header+"check_result: true\ndaily: 09:00\n---\n確認します。")
		jobs := Load(root)
		if len(jobs) != 1 || jobs[0].Err != "" || !jobs[0].CheckResult {
			t.Fatalf("definitions: %+v", jobs)
		}
		prompt := jobs[0].ReportInstruction("run-id", false)
		for _, text := range []string{"lacia run report", "run-id", "check_status", "unavailable", "nothing", "cause_key"} {
			if !strings.Contains(prompt, text) {
				t.Errorf("missing %s in %s", text, prompt)
			}
		}
	}
}

func TestCoverageAlertRecoveryAndRestart(t *testing.T) {
	root := t.TempDir()
	log := &RunLog{Root: root}
	base := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	jobs := []Job{{Name: "check", Enabled: true}}
	state := notify.NewState()
	notify.PlanTasks(nil, state, base, "https://example.test")
	poll := func() []notify.Notice {
		alerts := []notify.Alert{}
		for _, trouble := range Troubles(root, jobs, nil) {
			alerts = append(alerts, notify.Alert{Key: trouble.Key, Name: trouble.Name, Title: trouble.Title, Text: trouble.Text, Path: trouble.Path})
		}
		return notify.PlanAlerts(alerts, state, base, "https://example.test")
	}
	write := func(n int, status string) {
		outcome := RunFailed
		if status == "verified" {
			outcome = RunNothing
		}
		run := Run{ID: fmt.Sprint(n), At: base.Add(time.Duration(n) * time.Minute), Outcome: outcome, Device: "desk", Checks: []routine.Check{{Scope: "one", Status: status, Reason: "denied", CauseKey: "same"}}}
		if err := log.write("check", run, true); err != nil {
			t.Fatal(err)
		}
	}
	write(1, "unverified")
	if got := poll(); len(got) != 0 {
		t.Fatal(got)
	}
	write(2, "unverified")
	first := poll()
	if len(first) != 1 {
		t.Fatal(first)
	}
	// State survives notification-client restart; neither polling nor another failure repeats it.
	data, _ := json.Marshal(state)
	state = notify.NewState()
	if err := json.Unmarshal(data, state); err != nil {
		t.Fatal(err)
	}
	write(3, "unverified")
	if got := poll(); len(got) != 0 {
		t.Fatal(got)
	}
	write(4, "verified")
	if got := poll(); len(got) != 0 {
		t.Fatal(got)
	}
	write(5, "unverified")
	write(6, "unverified")
	back := poll()
	if len(back) != 1 || first[0].ID == back[0].ID {
		t.Fatalf("recurrence: %+v %+v", first, back)
	}
}

func TestHistoricalUnverifiedResultIsNotFullSuccess(t *testing.T) {
	root := t.TempDir()
	log := &RunLog{Root: root}
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	for n := 0; n < 3; n++ {
		if err := log.write("old", Run{ID: fmt.Sprint(n), At: now.Add(time.Duration(n) * time.Hour), Finished: now, Outcome: RunDone, Unverified: []string{"private"}}, false); err != nil {
			t.Fatal(err)
		}
	}
	h := HealthOf(root, "old")
	if !h.LastSuccess.IsZero() || h.Unverified["private"].Count != 3 {
		t.Fatalf("old health=%+v", h)
	}
}

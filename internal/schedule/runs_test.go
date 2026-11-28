package schedule

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirstCompletionAfterRegistration(t *testing.T) {
	root := newRecords(t, "S1001")
	log := &RunLog{Root: root}
	const name = "S1001"
	since := at("2026-11-30 23:55")
	now := at("2026-12-01 02:00")
	runs := []Run{
		{ID: "later", At: at("2026-12-01 01:00"), Finished: at("2026-12-01 01:05"), Outcome: RunDone},
		{ID: "before", At: at("2026-11-30 23:54"), Finished: now, Outcome: RunDone},
		{ID: "equal", At: since, Finished: now, Outcome: RunDone},
		{ID: "skipped", At: at("2026-11-30 23:56"), Finished: now, Outcome: RunSkipped},
		{ID: "missed", At: at("2026-11-30 23:57"), Finished: now, Outcome: RunMissed},
		{ID: "unfinished", At: at("2026-11-30 23:58"), Outcome: RunDone},
		{ID: "future", At: at("2026-11-30 23:59"), Finished: now.Add(time.Minute), Outcome: RunFailed},
		{ID: "first", At: at("2026-12-01 00:00"), Finished: now, Outcome: RunPartial},
	}
	for _, run := range runs {
		if err := log.write(name, run, false); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := FirstCompletion(root, name, since, now, nil)
	wantRef := "/routines/S1001?month=2026-12#" + runs[len(runs)-1].Anchor()
	if !ok || got.ID != "first" || got.Outcome != RunPartial || !got.Failed || got.Reference != wantRef {
		t.Fatalf("completion = %+v, %t; want first partial at %s", got, ok, wantRef)
	}
	if got, ok := FirstCompletion(root, name, now, now, nil); ok {
		t.Fatalf("completion at registration = %+v", got)
	}
}

func TestRunLogKeepsRepeatedSuccessForWaitingTask(t *testing.T) {
	root := newRecords(t, "S1001", "S1002")
	since := at("2026-11-25 08:45")
	log := &RunLog{Root: root, KeepSuccess: func(name string, at time.Time) bool {
		return name == "S1001" && at.After(since)
	}}
	for _, name := range []string{"S1001", "S1002"} {
		for _, when := range []string{"2026-11-25 08:00", "2026-11-25 08:30", "2026-11-25 09:00"} {
			due := at(when)
			run := Run{ID: when, At: due, Finished: due.Add(time.Minute), Outcome: RunNothing}
			for range 2 {
				if err := log.write(name, run, true); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if runs := ReadRuns(root, "S1001", "2026-11"); len(runs) != 2 {
		t.Fatalf("waiting job: recorded %d runs, want 2", len(runs))
	}
	if runs := ReadRuns(root, "S1002", "2026-11"); len(runs) != 1 {
		t.Fatalf("unwatched job: recorded %d runs, want 1", len(runs))
	}
	if got, ok := FirstCompletion(root, "S1001", since, since.Add(time.Hour), nil); !ok || got.ID != "2026-11-25 09:00" || got.Failed {
		t.Fatalf("completion for repeated success = %+v, %t", got, ok)
	}
}

func TestRunLog(t *testing.T) {
	// The routines keep the names of the schedules they replaced, which the
	// IDs of their requests still use.
	const github, calendar = "S1001", "S1002"
	root := newRecords(t)
	putDefinition(t, root, "areas/work/routines/S1001/routine.md", "---\nid: S1001\nlegacy_name: github\ndaily: \"09:00\"\n---\n\nGitHubを確認してください。\n")
	putDefinition(t, root, "areas/work/routines/S1002/routine.md", "---\nid: S1002\nlegacy_name: calendar\noutput: none\nevery: 30m\n---\n\n予定を取得してください。\n")

	state := t.TempDir()
	log := &RunLog{Root: root, State: stateFile(t, filepath.Join(state, "schedule-runs.json"))}
	results := map[string]Result{}
	busy := map[string]bool{}
	var now time.Time
	r := &Runner{Root: root, State: stateFile(t, filepath.Join(state, "schedules.json")), Submit: func(id string, job Job) error {
		if busy[job.Name] {
			return log.Skip(job, now)
		}
		results[id] = Result{}
		return log.Begin(job, id, now)
	}}
	lookup := func(id string) (Result, bool) {
		res, ok := results[id]
		return res, ok
	}
	step := func(when string) {
		t.Helper()
		now = at(when)
		if err := log.Settle(lookup, now); err != nil {
			t.Fatal(err)
		}
		if err := r.Tick(now); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(id, reply string, failed bool) {
		t.Helper()
		if _, ok := results[id]; !ok {
			t.Fatalf("no request %s; have %v", id, results)
		}
		results[id] = Result{Settled: true, Failed: failed, Reply: reply, Started: now, Finished: now.Add(time.Minute)}
		if failed {
			results[id] = Result{Settled: true, Failed: true, Error: reply, Started: now, Finished: now.Add(time.Minute)}
		}
	}
	outcomes := func(name, month string) string {
		var out []string
		for _, run := range ReadRuns(root, name, month) {
			out = append(out, run.At.Format("02T15:04")+"="+run.Outcome)
		}
		return strings.Join(out, " ")
	}

	// A run is written once its request has finished, not before.
	step("2026-11-24 09:00")
	if got := outcomes(github, "2026-11"); got != "" {
		t.Errorf("an unfinished run was written: %s", got)
	}
	if p := log.Pending(github); len(p) != 1 || p[0].Request != "sched-github-2026-11-24-0900" {
		t.Errorf("pending = %+v", p)
	}
	finish("sched-github-2026-11-24-0900", "報告はありません。", false)
	finish("sched-calendar-2026-11-24-0900", "報告はありません。", false)
	step("2026-11-24 09:05")
	if got := outcomes(github, "2026-11"); got != "24T09:00=nothing" {
		t.Errorf("github = %s", got)
	}
	if len(log.Pending(github)) != 0 {
		t.Error("a finished run is still pending")
	}
	// The run in progress survives a restart of the server.
	step("2026-11-24 09:30")
	log = &RunLog{Root: root, State: log.State}
	if p := log.Pending(calendar); len(p) != 1 {
		t.Errorf("pending after a restart = %+v", p)
	}

	// An interval job keeps one line while the outcome stays the same.
	finish("sched-calendar-2026-11-24-0930", "報告はありません。", false)
	step("2026-11-24 10:00")
	finish("sched-calendar-2026-11-24-1000", "接続できませんでした", true)
	step("2026-11-24 10:30")
	finish("sched-calendar-2026-11-24-1030", "報告はありません。", false)
	// The run before it is unfinished: the next occurrence is missed.
	busy[calendar] = true
	step("2026-11-24 11:00")
	busy[calendar] = false
	step("2026-11-24 11:30")
	if got := outcomes(calendar, "2026-11"); got != "24T11:00=missed 24T10:30=nothing 24T10:00=failed 24T09:00=nothing" {
		t.Errorf("calendar = %s", got)
	}
	if runs := ReadRuns(root, calendar, "2026-11"); runs[2].Summary != "接続できませんでした" {
		t.Errorf("failed run = %+v", runs[2])
	}

	// Days the server was down are written as missed, in the month they
	// were due, and the run that stands for them says how late it was.
	finish("sched-calendar-2026-11-24-1130", "報告はありません。", false)
	step("2026-12-02 12:00")
	finish("sched-github-2026-12-02-0900", "# 新着\n\n未確認の変更に2件あります。", false)
	finish("sched-calendar-2026-12-02-1200", "報告はありません。", false)
	step("2026-12-02 12:10")
	if got := outcomes(github, "2026-11"); got != "25T09:00=missed 24T09:00=nothing" {
		t.Errorf("github in November = %s", got)
	}
	runs := ReadRuns(root, github, "2026-12")
	if len(runs) != 1 || runs[0].Outcome != RunDone || runs[0].Summary != "新着" || runs[0].LateMinutes != 180 {
		t.Errorf("github in December = %+v", runs)
	}
	if runs := ReadRuns(root, github, "2026-11"); runs[0].Count != 7 {
		t.Errorf("missed days = %+v", runs[0])
	}
	if got := Months(root, github); strings.Join(got, " ") != "2026-12 2026-11" {
		t.Errorf("months = %v", got)
	}

	h := HealthOf(root, github)
	if !h.LastSuccess.Equal(at("2026-12-02 12:01")) || h.Failed != 0 || h.Missed != 0 {
		t.Errorf("health = %+v", h)
	}
	// Failures and misses in a row are counted up to the latest success.
	step("2026-12-03 09:00")
	finish("sched-github-2026-12-03-0900", "止まりました", true)
	step("2026-12-04 09:00")
	finish("sched-github-2026-12-04-0900", "止まりました", true)
	busy[github] = true
	step("2026-12-05 09:00")
	if h := HealthOf(root, github); h.Failed != 2 || h.Missed != 1 || !h.LastSuccess.Equal(at("2026-12-02 12:01")) {
		t.Errorf("health = %+v", h)
	}

	// A request that disappeared is given up on after a day.
	busy[github] = false
	step("2026-12-06 09:00")
	delete(results, "sched-github-2026-12-06-0900")
	step("2026-12-06 12:00")
	if len(log.Pending(github)) != 1 {
		t.Error("a run was given up on too early")
	}
	step("2026-12-07 08:59")
	if len(log.Pending(github)) != 1 {
		t.Error("a run was given up on too early")
	}
	step("2026-12-07 09:00")
	if runs := ReadRuns(root, github, "2026-12"); runs[0].Outcome != RunFailed || !runs[0].At.Equal(at("2026-12-06 09:00")) {
		t.Errorf("lost run = %+v", runs[0])
	}

	// Names and months that are not ours are not read.
	if ReadRuns(root, "../x", "2026-12") != nil || ReadRuns(root, github, "..") != nil {
		t.Error("read outside the log")
	}
}

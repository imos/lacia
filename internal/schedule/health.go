package schedule

import (
	"sort"
	"time"

	"github.com/imos/lacia/internal/routine"
)

type ScopeHealth struct {
	Count                    int
	Since, Latest            time.Time
	Reason, CauseKey, Device string
}

// Health keeps full success separate from success of an individual scope.
type Health struct {
	LastSuccess    time.Time
	Failed, Missed int
	Unverified     map[string]ScopeHealth
}

const healthMonths = 3

func HealthOf(root, name string) Health {
	h := Health{Unverified: map[string]ScopeHealth{}}
	months := Months(root, name)
	if len(months) > healthMonths {
		months = months[:healthMonths]
	}
	var runs []Run
	for _, month := range months {
		runs = append(runs, ReadRuns(root, name, month)...)
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if !runs[i].At.Equal(runs[j].At) {
			return runs[i].At.After(runs[j].At)
		}
		return runs[i].ID > runs[j].ID
	})
	seen := map[string]bool{}
	resolved := map[string]bool{}
	missing, failing := true, true
	for _, run := range runs {
		id := run.ID
		if id == "" {
			id = run.Request
		}
		if id != "" {
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		outcome := run.Outcome
		if (outcome == RunDone || outcome == RunNothing) && len(run.Unverified) > 0 {
			outcome = RunPartial
		}
		switch outcome {
		case RunMissed:
			if missing {
				h.Missed += max(run.Count, 1)
			}
		case RunFailed:
			missing = false
			if failing {
				h.Failed++
			}
		case RunPartial:
			missing, failing = false, false
		case RunDone, RunNothing:
			missing, failing = false, false
			if h.LastSuccess.IsZero() {
				h.LastSuccess = run.Finished
			}
		}
		if run.Outcome == RunSkipped || run.Outcome == RunMissed {
			continue
		}
		checks := run.Checks
		if len(checks) == 0 {
			for _, scope := range run.Unverified {
				checks = append(checks, routine.Check{Scope: scope, Status: "unverified"})
			}
		}
		if len(run.UnverifiedHandoffs) > 0 {
			checks = append(append([]routine.Check(nil), checks...), routine.Check{
				Scope: "task/handoff", Status: "unverified", Reason: "引き継ぎ先のタスクを確認できませんでした",
			})
		} else if len(run.Handoffs) > 0 || outcome == RunDone || outcome == RunNothing {
			resolved["task/handoff"] = true
		}
		for _, check := range checks {
			if resolved[check.Scope] {
				continue
			}
			if check.Status == "verified" {
				resolved[check.Scope] = true
				continue
			}
			if check.Status != "unverified" {
				continue
			}
			item := h.Unverified[check.Scope]
			if item.Count == 0 {
				item.Latest, item.Reason, item.CauseKey, item.Device = run.At, check.Reason, check.CauseKey, run.Device
			}
			item.Count++
			item.Since = run.At
			h.Unverified[check.Scope] = item
		}
	}
	return h
}

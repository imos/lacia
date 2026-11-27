package schedule

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/imos/lacia/internal/routine"
)

// HasProblem reports a failed or incomplete execution.
func (r Run) HasProblem() bool {
	return r.Outcome == RunFailed || r.Outcome == RunPartial || len(r.Unverified) > 0
}

// ProblemReport opens one review per kind of failure until a complete run
// succeeds. The reason can change while the same routine remains unhealthy.
func (r Run) ProblemReport(lastSuccessAt time.Time) routine.Result {
	reasons := r.ProblemReasons()
	fingerprint := append([]string{r.Outcome, r.FailureKind}, reasons...)
	if len(reasons) == 0 || (r.FailureKind == "execution" && r.Summary != "") {
		fingerprint = append(fingerprint, r.Summary)
	}
	data, _ := json.Marshal(fingerprint)
	key := fmt.Sprintf("%x", sha256.Sum256(data))

	return routine.Result{
		RunID: r.OccurrenceID(), Summary: r.ProblemSummary(), Reference: r.Reference,
		Unverified: reasons, ProblemKey: key, ProblemAt: r.At, ProblemSince: lastSuccessAt,
	}
}

// ProblemLabel also recognizes older partial reports stored as a success.
func (r Run) ProblemLabel() string {
	if (r.Outcome == RunDone || r.Outcome == RunNothing) && len(r.Unverified) > 0 {
		r.Outcome = RunPartial
	}
	return r.OutcomeLabel()
}

// ProblemSummary puts the outcome before any detail supplied by the run.
func (r Run) ProblemSummary() string {
	summary := r.ProblemLabel()
	if r.Summary != "" {
		summary += " " + r.Summary
	}
	return summary
}

// ProblemReasons includes the stored reason when a run could not read a scope.
func (r Run) ProblemReasons() []string {
	byScope := map[string]string{}
	for _, scope := range r.Unverified {
		byScope[scope] = ""
	}
	for _, check := range r.Checks {
		if check.Status == "unverified" {
			byScope[check.Scope] = check.Reason
		}
	}
	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	for i, scope := range scopes {
		if reason := strings.TrimSpace(byScope[scope]); reason != "" {
			scopes[i] += "：" + reason
		}
	}
	if len(r.UnverifiedHandoffs) > 0 {
		scopes = append(scopes, "引き継ぎ先のタスクを確認できませんでした。")
	}
	return scopes
}

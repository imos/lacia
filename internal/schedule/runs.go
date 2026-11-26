package schedule

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/task"
)

// Outcomes of one run.
const (
	RunDone    = "done"    // ran and had something to say
	RunNothing = "nothing" // ran and had nothing to report
	RunPartial = "partial" // completed only part of the expected checks
	RunFailed  = "failed"  // could not be processed
	RunMissed  = "missed"
	RunSkipped = "skipped" // its time came, but it never ran
)

// nothingReply is how a conversation says that there is nothing to report.
const nothingReply = "報告はありません"

// Run is one line of routines/<name>/runs/<YYYY-MM>/runs.jsonl: one
// occurrence of a repeating job. Lines are only ever appended.
type Run struct {
	Profile  string    `json:"profile,omitempty"`
	Login    string    `json:"login,omitempty"`
	To       string    `json:"to,omitempty"`
	Target   string    `json:"target,omitempty"`
	Device   string    `json:"device,omitempty"`
	At       time.Time `json:"at"` // when it was due
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Outcome  string    `json:"outcome"`
	Summary  string    `json:"summary,omitempty"`
	// LateMinutes is how long after At the run was queued, when that is
	// half an hour or more.
	LateMinutes int `json:"late_minutes,omitempty"`
	// Count is how many occurrences a missed line stands for, At being the
	// first of them.
	Count       int              `json:"count,omitempty"`
	Request     string           `json:"request,omitempty"`
	ID          string           `json:"id,omitempty"`
	Reference   string           `json:"reference,omitempty"`
	Matters     []routine.Matter `json:"matters,omitempty"`
	Unverified  []string         `json:"unverified,omitempty"`
	Checks      []routine.Check  `json:"checks,omitempty"`
	CheckStatus string           `json:"check_status,omitempty"`
	FailureKind string           `json:"failure_kind,omitempty"`

	Handoffs           []routine.Handoff `json:"handoffs,omitempty"`
	UnverifiedHandoffs []string          `json:"unverified_handoffs,omitempty"`
}

// DestinationLabel preserves the name shown for historical run records.
func (r Run) DestinationLabel() string {
	if r.To != "" {
		return r.To
	}
	return r.Target
}

func (r Run) occurrenceID() string {
	if r.ID != "" {
		return r.ID
	}
	if r.Request != "" {
		return r.Request
	}
	return r.At.UTC().Format(time.RFC3339Nano)
}

// Anchor identifies this occurrence in its monthly run history.
func (r Run) Anchor() string {
	digest := sha256.Sum256([]byte(r.occurrenceID()))
	return fmt.Sprintf("run-%x", digest[:12])
}

// RoutineURL links to a fixed routine ID or a legacy schedule name.
func RoutineURL(name string) string {
	return "/routines/" + url.PathEscape(name)
}

// OutcomeLabel is the Japanese name of the outcome.
func (r Run) OutcomeLabel() string {
	switch r.Outcome {
	case RunDone:
		return "済みました"
	case RunNothing:
		return "何もありませんでした"
	case RunPartial:
		return "一部を確認できませんでした"
	case RunFailed:
		return "失敗しました"
	case RunMissed:
		return "実行されませんでした"
	case RunSkipped:
		return "停止中のため見送りました"
	case "":
		return "実行中です"
	}
	return r.Outcome
}

// Result is what became of the request of a run.
type Result struct {
	Profile           string
	Login             string
	Device            string
	Settled           bool // false while it is queued or running
	Failed            bool
	Reply, Error      string
	Started, Finished time.Time
}

// pendingRun is a run that was queued and has not been written yet.
type pendingRun struct {
	Name    string    `json:"name"`
	At      time.Time `json:"at"`
	Queued  time.Time `json:"queued"`
	Request string    `json:"request"`
	// Repeat marks an interval job: while its outcome stays the same within
	// a day, only the first run is written.
	To     string `json:"to,omitempty"`
	Target string `json:"target,omitempty"`
	Repeat bool   `json:"repeat,omitempty"`
}

// RunLog writes the runs of repeating jobs into the task repository. A run
// in progress is kept in State, outside the repository, so that the log
// itself holds one finished line per run and is never rewritten.
type RunLog struct {
	Root        string // task repository
	Location    *time.Location
	State       string // file listing the runs in progress
	Execution   func(string) (Execution, bool)
	ResolveName func(string) string
	Review      func(string, Run, routine.Result) error
	// KeepSuccess retains a repeated success that a registered task may need.
	KeepSuccess func(name string, at time.Time) bool

	mu      sync.Mutex
	loaded  bool
	pending []pendingRun
}

// recorded reports whether the runs of the job are logged.
func recorded(job Job) bool { return ValidName(job.Name) }

func (l *RunLog) name(name string) string {
	if l.ResolveName != nil {
		return l.ResolveName(name)
	}
	return name
}

func (l *RunLog) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	if data, err := os.ReadFile(l.State); err == nil {
		json.Unmarshal(data, &l.pending)
	}
}

func (l *RunLog) save() error { return atomicJSON(l.State, l.pending) }

// first returns the first of the occurrences that job skipped before Slot.
func first(job Job, n int) time.Time {
	if job.Every > 0 {
		at := job.Slot
		if job.WeekdayMask != 0 {
			for n > 0 {
				at = at.Add(-job.Every)
				if job.onDay(at) {
					n--
				}
			}
			return at
		}
		return at.Add(-time.Duration(n) * job.Every)
	}
	if n == 0 || job.Daily == "" {
		return job.Slot.AddDate(0, 0, -n)
	}

	clocks := strings.Split(job.Daily, ", ")
	for date := time.Date(job.Slot.Year(), job.Slot.Month(), job.Slot.Day(), 12, 0, 0, 0, job.zone()); ; date = date.AddDate(0, 0, -1) {
		if !job.onDay(date) {
			continue
		}
		for i := len(clocks) - 1; i >= 0; i-- {
			slot := date.Format(time.DateOnly) + "T" + clocks[i]
			at, err := time.ParseInLocation(slotLayout, slot, job.zone())
			if err != nil || at.Format(slotLayout) != slot || !at.Before(job.Slot) {
				continue
			}
			n--
			if n == 0 {
				return at
			}
		}
	}
}

// Begin notes that the occurrence job.Slot was queued as request. The
// occurrences that were skipped before it are written as missed.
func (l *RunLog) Begin(job Job, request string, now time.Time) error {
	if !recorded(job) {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	for _, p := range l.pending {
		if p.Request == request {
			return nil
		}
	}
	if job.Skipped > 0 {
		err := l.write(l.name(job.Name), Run{ID: "missed-before-" + request, At: first(job, job.Skipped), Finished: now, Outcome: RunMissed, Count: job.Skipped,
			Summary: "サーバーが動いていなかったあいだの回です。次の回にまとめました。"}, false)
		if err != nil {
			return err
		}
	}
	l.pending = append(l.pending, pendingRun{Name: job.Name, At: job.Slot, Queued: now, Request: request, Repeat: job.Every > 0 && (job.ID == "" || job.Output == "none"), To: job.To})
	if err := l.save(); err != nil {
		l.pending = l.pending[:len(l.pending)-1]
		return err
	}
	return nil
}

// Skip writes the occurrence job.Slot, and those skipped before it, as
// missed: it was not queued because the run before it had not finished.
func (l *RunLog) Skip(job Job, now time.Time) error {
	if !recorded(job) {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	id := requestID(job.OccurrenceName(), job.Slot.Format(slotLayout))
	return l.write(l.name(job.Name), Run{ID: id, At: first(job, job.Skipped), Finished: now, Outcome: RunMissed, Count: job.Skipped + 1,
		Summary: "前の回が終わっていなかったので、今回は見送りました。"}, false)

}

// lost is how long a run whose request has disappeared is waited for.
const lost = 24 * time.Hour

// Settle writes the runs whose request has finished. lookup returns what
// became of a request, and false when there is no such request.
func (l *RunLog) Settle(lookup func(id string) (Result, bool), now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	var keep []pendingRun
	var firstErr error
	for _, p := range l.pending {
		res, ok := lookup(p.Request)
		run := Run{ID: p.Request, At: p.At, Started: res.Started, Finished: res.Finished, Request: p.Request, To: p.To, Target: p.Target}
		var report *routine.Result
		var job Job
		var stopped bool
		var unverifiedHandoffs []string
		if l.Execution != nil {
			if e, ok := l.Execution(p.Request); ok {
				run.Device = e.Device
				run.Profile, run.Login = e.Result.Profile, e.Result.Login
				if e.Result.Device != "" {
					run.Device = e.Result.Device
				}
				report = e.Report
				job = e.Job
				unverifiedHandoffs = e.UnverifiedHandoffs
				stopped = e.Status == Skipped
				if e.To != "" {
					run.To = e.To
				} else if run.Target == "" {
					run.Target = e.Target
				}
			}
		}
		switch {
		case !ok && now.Sub(p.Queued) < lost, ok && !res.Settled:
			keep = append(keep, p)
			continue
		case !ok:
			run.Finished, run.Outcome, run.Summary = now, RunFailed, "依頼の記録が見つからないので、結果が分かりません。"
		case stopped:
			run.Outcome, run.Summary = RunSkipped, summarize(res.Error)
		case res.Failed:
			run.Outcome, run.Summary = RunFailed, summarize(res.Error)
			if run.Summary == "" {
				run.Summary = summarize(res.Reply)
			}
		case strings.HasPrefix(strings.TrimSpace(res.Reply), nothingReply):
			run.Outcome = RunNothing
		default:
			run.Outcome, run.Summary = RunDone, summarize(res.Reply)
		}
		if run.Outcome == RunFailed {
			run.FailureKind = "execution"
		}
		if report != nil && !stopped {
			normalized := *report
			if err := job.NormalizeCoverage(&normalized); err != nil || normalized.RequireScopes(job.CheckScopes) != nil || job.CheckResult && normalized.CheckStatus == "" {
				run.Outcome, run.FailureKind, run.Summary = RunFailed, "result_invalid", "確認結果の形式を読めませんでした。"
				report = nil
			} else {
				report = &normalized
				run.Reference = report.Reference
				run.Unverified = report.Unverified
				run.Handoffs, report.Matters = settleHandoffs(report.Matters, report.Handoffs, unverifiedHandoffs)
				run.Matters, run.UnverifiedHandoffs = report.Matters, unverifiedHandoffs
				run.Checks, run.CheckStatus = report.Checks, report.CheckStatus
				if !res.Failed && ok {
					run.Summary = report.Summary
					switch report.CheckStatus {
					case "unavailable":
						run.Outcome, run.FailureKind = RunFailed, "check_unavailable"
					case "partial":
						run.Outcome = RunPartial
					default:
						run.Outcome = RunDone
						if report.Nothing || job.ExplicitMatters && strings.TrimSpace(report.Summary) == "" &&
							report.Reference == "" && len(report.Matters) == 0 && len(run.Handoffs) == 0 {
							run.Outcome = RunNothing
						}
					}
				}
			}
			if len(run.UnverifiedHandoffs) > 0 && (run.Outcome == RunDone || run.Outcome == RunNothing) {
				run.Outcome, run.FailureKind = RunPartial, "handoff_unverified"
			}
		} else if !stopped && !res.Failed && ok && job.CheckResult {
			run.Outcome, run.FailureKind, run.Summary = RunFailed, "result_missing", "確認結果の形式が届いていないため、確認できた範囲が分かりません。"
		}

		if run.Finished.IsZero() {
			run.Finished = now
		}
		if late := p.Queued.Sub(p.At); late >= 30*time.Minute {
			run.LateMinutes = int(late.Minutes())
		}
		name := l.name(p.Name)
		compress := p.Repeat && !(job.ExplicitMatters && report != nil)
		err := l.write(name, run, compress)
		if err == nil && !stopped && report != nil && report.HasFindings(job.ExplicitMatters) && job.ID != "" && job.Output == "matters" && l.Review != nil {
			if len(run.UnverifiedHandoffs) > 0 {
				report.Unverified = append(append([]string(nil), report.Unverified...), "引き継ぎ先のタスクを確認できませんでした。")
			}
			err = l.Review(name, run, *report)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			keep = append(keep, p)
		}
	}
	if len(keep) != len(l.pending) {
		old := l.pending
		l.pending = keep
		if err := l.save(); err != nil {
			l.pending = old
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// settleHandoffs keeps the findings in one place. If the task was not found
// when the report arrived, the finding remains eligible for a receipt.
func settleHandoffs(matters []routine.Matter, handoffs []routine.Handoff, unverified []string) ([]routine.Handoff, []routine.Matter) {
	if len(handoffs) == 0 {
		return nil, matters
	}
	missing := map[string]bool{}
	for _, id := range unverified {
		missing[id] = true
	}
	var saved []routine.Handoff
	findings := append([]routine.Matter(nil), matters...)
	explicit := map[[2]string]bool{}
	for _, matter := range matters {
		explicit[[2]string{matter.Key, matter.Revision}] = true
	}
	for _, handoff := range handoffs {
		// An explicit request for a decision takes priority over a claim
		// that the same revision was already delivered through a task.
		if explicit[[2]string{handoff.Key, handoff.Revision}] {
			continue
		}
		if missing[handoff.TaskID] {
			findings = append(findings, handoff.Matter)
		} else {
			saved = append(saved, handoff)
		}
	}
	return saved, findings
}

// Pending returns the runs of the job that are queued or running.
func (l *RunLog) Pending(name string) []Run {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	var out []Run
	for _, p := range l.pending {
		if p.Name == name {
			if l.Execution != nil {
				if _, ok := l.Execution(p.Request); ok {
					continue
				}
			}
			out = append(out, Run{At: p.At, Started: p.Queued, Request: p.Request, To: p.To, Target: p.Target})
		}
	}
	return out
}

// summaryLimit keeps the log to a line per run; the reply itself stays with
// the request.
const summaryLimit = 120

// summarize returns the first line of text that says something.
func summarize(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > summaryLimit {
			return string(r[:summaryLimit]) + "…"
		}
		return line
	}
	return ""
}

// monthLayout names the directory of a month, like the task directories.
const monthLayout = "2006-01"

func runsFile(root, name, month string) (string, error) {
	dir, err := task.RoutineDir(root, name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runs", month, "runs.jsonl"), nil
}

// write appends one run to the file of the month it was due in. With
// repeat set, a run with the same outcome as the line before it, on the
// same day, is left out: a job that runs every few minutes would otherwise
// fill the log with identical lines.
func (l *RunLog) write(name string, run Run, repeat bool) error {
	loc := l.Location
	if loc == nil {
		loc = task.JST
	}
	run.At = run.At.In(loc)
	month := run.At.Format(monthLayout)
	if run.ID != "" || run.Request != "" || repeat {
		runs := ReadRuns(l.Root, name, month)
		for _, previous := range runs {
			if (run.ID != "" && previous.ID == run.ID) || (run.Request != "" && previous.Request == run.Request) {
				return nil
			}
		}
		if repeat && run.CheckStatus == "" && len(run.Unverified) == 0 && len(run.Handoffs) == 0 && (run.Outcome == RunDone || run.Outcome == RunNothing) && len(runs) > 0 && runs[0].Outcome == run.Outcome && runs[0].Profile == run.Profile && runs[0].Login == run.Login && runs[0].Device == run.Device && runs[0].At.In(loc).Format(time.DateOnly) == run.At.Format(time.DateOnly) && (l.KeepSuccess == nil || !l.KeepSuccess(name, run.At)) {
			return nil
		}
	}
	path, err := runsFile(l.Root, name, month)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(run)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Months lists the months that have runs of the job, newest first.
func Months(root, name string) []string {
	if !ValidName(name) {
		return nil
	}
	pattern, err := runsFile(root, name, "*")
	if err != nil {
		return nil
	}
	paths, _ := filepath.Glob(pattern)
	var out []string
	for _, p := range paths {
		month := filepath.Base(filepath.Dir(p))
		if _, err := time.Parse(monthLayout, month); err == nil {
			out = append(out, month)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// ReadRuns returns the runs of the job in one month, newest first. A line
// that cannot be read is left out.
func ReadRuns(root, name, month string) []Run {
	if !ValidName(name) {
		return nil
	}
	if _, err := time.Parse(monthLayout, month); err != nil {
		return nil
	}
	filename, err := runsFile(root, name, month)
	if err != nil {
		return nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil
	}
	defer f.Close()
	var runs []Run
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var run Run
		if json.Unmarshal(sc.Bytes(), &run) == nil && !run.At.IsZero() {
			runs = append(runs, run)
		}
	}
	// Lines are appended as runs finish; show them in the order they were due.
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].At.After(runs[j].At) })
	return runs
}

// FirstCompletion finds the earliest eligible occurrence already on record.
// Compare the scheduled time to registration, since an earlier occurrence can
// finish late and a later occurrence can finish before an earlier one.
func FirstCompletion(root, name string, since, now time.Time, loc *time.Location) (task.RoutineCompletion, bool) {
	if loc == nil {
		loc = task.JST
	}
	firstMonth := since.In(loc).Format(monthLayout)
	lastMonth := now.In(loc).Format(monthLayout)
	months := Months(root, name)
	for i := len(months) - 1; i >= 0; i-- {
		month := months[i]
		if month < firstMonth || month > lastMonth {
			continue
		}
		runs := ReadRuns(root, name, month)
		for j := len(runs) - 1; j >= 0; j-- {
			run := runs[j]
			if !run.At.After(since) || run.At.After(now) || run.Finished.IsZero() || run.Finished.After(now) {
				continue
			}
			switch run.Outcome {
			case RunDone, RunNothing, RunFailed, RunPartial:
			default:
				continue
			}
			ref := RoutineURL(name) + "?month=" + month + "#" + run.Anchor()
			return task.RoutineCompletion{ID: run.occurrenceID(), Reference: ref, Outcome: run.Outcome, Failed: run.Outcome == RunFailed || run.Outcome == RunPartial}, true
		}
	}
	return task.RoutineCompletion{}, false
}

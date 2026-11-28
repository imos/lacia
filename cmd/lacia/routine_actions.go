package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// The work server first prepares tasks while the existing screen is still
// visible. Each request sees the same mode as the run-settlement loop.
type routineActionRuntime struct {
	state    *disk.Dir
	tasks    *task.Store
	receipts *routine.Store
	queue    *request.Queue
}

func (a routineActionRuntime) mode() string {
	raw, err := a.state.ReadFile("routine-actions-mode")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (a routineActionRuntime) enabled() bool { return a.mode() == "tasks" }

// collects reports whether the server gathers this routine's matters into a
// routine's task, which is then where a run's findings belong by default.
func (a routineActionRuntime) collects(job schedule.Job) bool {
	return a.enabled() && a.tasks.KeepsTasks() && task.ValidRoutineID(job.ID) && job.Output == "matters"
}

// instruction always names where the matters of a run end up, also for a
// routine that has no task.md yet: without it the conversation is only told
// about ordinary tasks and opens a new one for each finding.
func (a routineActionRuntime) instruction(job schedule.Job) string {
	if !a.enabled() || !a.tasks.KeepsTasks() {
		return ""
	}
	const receipt = "\n\nサーバーが実行結果とその根拠を受付に保存します。受付に確認済みの印を付ける必要はありません。"
	const resolved = "以前の用件が解決したら、結果の JSON の resolved に、その用件を matters に書いたときの key を指定できます。"
	if !a.collects(job) {
		return receipt + "対応が続く用件は、この定期タスクの task.md で追ってください。同じ用件をすでに追っているタスクがあれば、そちらへ引き継いでください。" + resolved
	}
	where := fmt.Sprintf("サーバーが %s の task.md の「実行から分かった対応」にまとめます。", job.ID)
	if target := job.MattersTask; target != "" {
		where = fmt.Sprintf("この定期タスクでは、定期タスク %s が用件の引き継ぎ先です。サーバーがその task.md の「実行から分かった対応」にまとめます。引き継ぎ先の定期タスクが取り上げた用件と同じなら、同じ key を使ってください。", target)
		if _, err := a.tasks.Get(target); err == nil {
			where += fmt.Sprintf("いま追っている用件は `lacia task show %s` で読めます。", target)
		}
	}
	return receipt + "\n実行のあとも対応が必要な用件は、結果の JSON の matters に書いてください。" + where +
		"\n通常のタスクを新しく作るのは、この定期タスクの「すること」で、どのタスクを作るか指定されているときだけです。そのタスクへ引き継ぐときや、同じ用件をすでに追っている通常のタスクへ引き継ぐときは、引き継ぎ先を結果の JSON の handoffs に書いてください。" + resolved
}

// mattersTask names the routine whose task collects the matters of id, when
// its definition asks for another one and is valid as it stands now.
func (a routineActionRuntime) mattersTask(id string) string {
	for _, job := range schedule.LoadIn(a.tasks.Root(), a.tasks.Location()) {
		if job.ID == id && job.Err == "" {
			return job.MattersTask
		}
	}
	return ""
}

// splitShared moves what the owner follows (matters and handoffs) out of an
// observation. What says how the run itself went stays, because that is about
// the routine that ran. Resolutions go to both tasks, so a matter the routine
// kept in its own task before it named another one is still closed there.
func splitShared(observation *task.RoutineObservation) task.RoutineObservation {
	shared := task.RoutineObservation{At: observation.At, Shared: true}
	followed := func(key string) bool {
		return strings.HasPrefix(key, "matter:") || strings.HasPrefix(key, "handoff:")
	}
	actions := observation.Actions[:0]
	for _, item := range observation.Actions {
		if followed(item.Key) {
			shared.Actions = append(shared.Actions, item)
		} else {
			actions = append(actions, item)
		}
	}
	observation.Actions = actions
	for _, key := range observation.Resolved {
		if followed(key) {
			shared.Resolved = append(shared.Resolved, key)
		}
	}
	return shared
}

type routineBaselineItem struct {
	ID      string
	At      time.Time
	Actions []task.RoutineAction
	Silent  bool
}

// A migration plan is an operator-checked list. It is applied through the
// same store lock as live results and is retryable if a write fails.
func (a routineActionRuntime) applyBaseline() error {
	if !a.tasks.KeepsTasks() || a.mode() != "prepare" {
		return nil
	}
	const baseline = "routine-actions-baseline.json"
	data, err := a.state.ReadFile(baseline)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var plan []routineBaselineItem
	if err := json.Unmarshal(data, &plan); err != nil {
		return err
	}
	for _, item := range plan {
		if _, err := a.tasks.MergeRoutineActions(item.ID, task.RoutineObservation{At: item.At, Check: true, Actions: item.Actions, Silent: item.Silent}); err != nil {
			return err
		}
	}
	// Remove the input atomically and retain it beside the runtime state.
	return a.state.Rename(baseline, baseline+".applied")
}

func runReference(id string, run schedule.Run, loc *time.Location) string {
	return schedule.RoutineURL(id) + "?month=" + run.At.In(loc).Format("2006-01") + "#" + run.Anchor()
}

func shortAction(text string) string {
	parts := []rune(strings.Join(strings.Fields(text), " "))
	if len(parts) > 220 {
		return string(parts[:220]) + "…"
	}
	return string(parts)
}

func runActions(run schedule.Run, ref string) task.RoutineObservation {
	observation := task.RoutineObservation{At: run.At}
	complete := run.Outcome == schedule.RunDone || run.Outcome == schedule.RunNothing
	observation.ClearTechnical = complete && !run.HasProblem()
	if observation.ClearTechnical {
		observation.Resolved = append(observation.Resolved, "run:missed")
	}
	for _, check := range run.Checks {
		if check.Status == "verified" {
			observation.Verified = append(observation.Verified, check.Scope)
		}
	}
	if run.HasProblem() {
		for _, check := range run.Checks {
			if check.Status != "unverified" {
				continue
			}
			text := "前回確かめられなかった「" + check.Scope + "」を、もう1度確かめます。"
			if reason := strings.TrimSpace(check.Reason); reason != "" {
				text += "前回の理由は「" + reason + "」です。"
			}
			cause := check.CauseKey
			if cause == "" {
				cause = run.FailureKind
			}
			observation.Actions = append(observation.Actions, task.RoutineAction{
				Key: "run:scope:" + check.Scope, Scope: check.Scope, Revision: cause,
				Text: shortAction(text), Reference: ref, Technical: true,
			})
		}
		if len(observation.Actions) == 0 {
			reason := shortAction(run.Summary)
			const generic = "失敗した回の原因と、再開に必要な操作を確かめます。"
			text := generic
			if reason != "" {
				text += "前回の結果は「" + reason + "」です。"
			}
			observation.Actions = append(observation.Actions, task.RoutineAction{
				Key: "run:execution", Revision: run.FailureKind, Text: shortAction(text), Reference: ref, Technical: true,
			})
		}
	}
	if run.Outcome == schedule.RunMissed {
		observation.Actions = append(observation.Actions, task.RoutineAction{
			Key: "run:missed", Revision: "missed", Text: "定期タスクが実行されなかった理由と、次に実行できる条件を確かめます。", Reference: ref, Technical: true,
			Count: max(run.Count, 1),
		})
	}
	for _, matter := range run.Matters {
		text := shortAction(matter.Summary)
		if text == "" {
			text = task.OwnerName + "に、受付に記録した用件を見て、どう対応するか決めていただきます。"
		}
		reference := matter.Reference
		if reference == "" {
			reference = ref
		}
		observation.Actions = append(observation.Actions, task.RoutineAction{
			Key: "matter:" + matter.Key, Revision: matter.Revision, Text: text, Reference: reference, Owner: true,
		})
	}
	for _, handoff := range run.Handoffs {
		observation.Resolved = append(observation.Resolved, "matter:"+handoff.Key)
		observation.Actions = append(observation.Actions, task.RoutineAction{
			Key: "handoff:" + handoff.Key, Revision: handoff.Revision,
			Text: "引き継いだ用件は " + handoff.TaskID + " で追います。", Reference: "/tasks/" + handoff.TaskID,
			LinkedTask: handoff.TaskID,
		})
	}
	for _, key := range run.Resolved {
		observation.Resolved = append(observation.Resolved, "matter:"+key, "handoff:"+key)
	}
	return observation
}

func (a routineActionRuntime) observe(id string, run schedule.Run) error {
	mode := a.mode()
	if mode != "prepare" && mode != "tasks" {
		return nil
	}
	ref := runReference(id, run, a.tasks.Location())
	if mode == "tasks" {
		result := routine.Result{RunID: run.OccurrenceID(), Summary: run.Summary, Reference: run.Reference, Matters: run.Matters}
		if run.HasProblem() {
			problem := run.ProblemReport(time.Time{})
			result.ProblemKey, result.ProblemAt, result.Unverified = problem.ProblemKey, run.At, problem.Unverified
		}
		if _, err := a.receipts.Record(id, run.Outcome, result); err != nil {
			return err
		}
	}
	if !a.tasks.KeepsTasks() || run.Outcome == schedule.RunSkipped {
		return nil
	}
	// Screen and task state must not regress if a delayed run is saved after
	// a newer successful run. Missed records use the planned slot as their time.
	health := schedule.HealthOf(a.tasks.Root(), id)
	if run.At.Before(health.LatestExecution.At) {
		return nil
	}
	observation := runActions(run, ref)
	if target := a.mattersTask(id); target != "" {
		shared := splitShared(&observation)
		a.keepHandoffs(target, &shared)
		if _, err := a.tasks.MergeRoutineActions(target, shared); err != nil {
			return err
		}
	}
	a.keepHandoffs(id, &observation)
	current, err := a.tasks.MergeRoutineActions(id, observation)
	if err != nil {
		return err
	}
	return a.escalate(current)
}

func (a routineActionRuntime) keepHandoffs(id string, observation *task.RoutineObservation) {
	t, err := a.tasks.Get(id)
	if err != nil {
		return
	}
	open := map[string]bool{}
	for _, item := range t.RoutineActions() {
		if item.LinkedTask == "" {
			continue
		}
		linked, err := a.tasks.Get(item.LinkedTask)
		if err == nil && linked.Status.Closed() {
			observation.Resolved = append(observation.Resolved, item.Key)
			continue
		}
		open[strings.TrimPrefix(item.Key, "handoff:")] = true
	}
	for _, item := range observation.Actions {
		if item.LinkedTask != "" {
			open[strings.TrimPrefix(item.Key, "handoff:")] = true
		}
	}
	items := observation.Actions[:0]
	for _, item := range observation.Actions {
		key, matter := strings.CutPrefix(item.Key, "matter:")
		if !matter || !open[key] {
			items = append(items, item)
		}
	}
	observation.Actions = items
}

func routineEscalationID(id string, item task.RoutineAction) string {
	// One request for each spell of the same technical problem. Its
	// durable ID also makes retries of the schedule commit idempotent.
	sum := sha256.Sum256([]byte(item.Key + "\n" + item.Revision + "\n" + item.First.UTC().Format(time.RFC3339Nano)))
	return fmt.Sprintf("routine-action-%s-%x", id, sum[:10])
}

func (a routineActionRuntime) escalate(t task.Task) error {
	if a.queue == nil || t.ID == "" {
		return nil
	}
	for _, item := range t.RoutineActions() {
		if !item.Technical || item.Owner || item.Count < 2 {
			continue
		}
		text := fmt.Sprintf("定期タスク「%s」（%s）で、同じ問題が続いています。\n\n%s\n\nこの定期タスクの task.md と実行の記録を読んで、原因を調べてください。本文には、だれが何をするのかと、どんな条件で次に確かめるのかを書いてください。%sの操作が必要だと分かったら、その操作を書き、この定期タスクを「確認待ち」にしてください。", t.Title, t.ID, item.Text, task.OwnerName)
		if _, _, err := a.queue.Add(request.Request{ID: routineEscalationID(t.ID, item), Kind: request.System, Source: "定期タスクの対応", Text: text}); err != nil {
			return err
		}
	}
	return nil
}

func (a routineActionRuntime) syncTroubles(jobs []schedule.Job, devices map[string]schedule.DeviceStatus, now time.Time) error {
	mode := a.mode()
	if !a.tasks.KeepsTasks() || mode != "prepare" && mode != "tasks" {
		return nil
	}
	var result error
	active := map[string]map[string]bool{}
	operations := map[string]bool{}
	for _, trouble := range schedule.Troubles(a.tasks.Root(), jobs, devices) {
		id := trouble.Name
		if task.ValidRoutineID(id) {
			if active[id] == nil {
				active[id] = map[string]bool{}
			}
			key := "health:" + trouble.Key
			active[id][key] = true
			t, err := a.tasks.MergeRoutineActions(id, task.RoutineObservation{At: now, Check: true, Actions: []task.RoutineAction{{
				Key: key, Revision: trouble.Text, Text: "設定や実行環境を確かめます。" + shortAction(trouble.Text),
				Reference: schedule.RoutineURL(id), Technical: true,
			}}})
			if err == nil {
				for _, item := range t.RoutineActions() {
					if item.Key == key {
						err = a.askTrouble(routineEscalationID(id, item), id, trouble)
						break
					}
				}
			}
			if !errors.Is(err, task.ErrNotFound) {
				result = errors.Join(result, err)
				continue
			}
		}
		slug := routineOperationSlug(trouble.Key)
		operations[slug] = true
		result = errors.Join(result, a.operationalTask(trouble, slug))
	}
	for _, item := range a.tasks.List() {
		_, slug, found := strings.Cut(item.Dir, "-routine-operation-")
		if !found || operations["routine-operation-"+slug] || item.Status != task.Review ||
			!strings.HasPrefix(item.Next(), task.OwnerName+"に、対象の端末で認証を済ませていただきます。") {
			continue
		}
		_, _, err := a.tasks.SetStatus(item.ID, task.Closing, "定期タスクの実行結果", "端末の認証を待つ必要がなくなりました。")
		result = errors.Join(result, err)
	}
	for _, job := range jobs {
		if !task.ValidRoutineID(job.ID) || !job.Enabled && job.Err == "" {
			continue
		}
		t, err := a.tasks.Get(job.ID)
		if err != nil {
			continue
		}
		var resolved []string
		for _, item := range t.RoutineActions() {
			if strings.HasPrefix(item.Key, "health:") && !active[job.ID][item.Key] {
				resolved = append(resolved, item.Key)
			}
		}
		if len(resolved) > 0 {
			_, err = a.tasks.MergeRoutineActions(job.ID, task.RoutineObservation{At: now, Check: true, Resolved: resolved})
			result = errors.Join(result, err)
		}
	}
	return result
}

func (a routineActionRuntime) askTrouble(requestID, id string, trouble schedule.Trouble) error {
	if a.queue == nil {
		return nil
	}
	text := fmt.Sprintf("タスク %s の定期実行で問題が起きています。%s\n\nタスクの本文で現状と実行の記録を確かめて、対応をお願いします。", id, trouble.Text)
	_, _, err := a.queue.Add(request.Request{ID: requestID, Kind: request.System, Source: "定期タスクの対応", Text: text})
	return err
}

func routineOperationSlug(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("routine-operation-%x", sum[:8])
}

func (a routineActionRuntime) operationalTask(trouble schedule.Trouble, slug string) error {
	areas := a.tasks.Areas()
	if len(areas) == 0 {
		return nil
	}
	for _, item := range a.tasks.List() {
		if !item.Status.Closed() && strings.HasSuffix(item.Dir, "-"+slug) {
			return nil
		}
	}
	device := strings.HasPrefix(trouble.Key, "device:")
	status := task.Doing
	next := "わたしが定期タスクの定義と実行の条件を確認します。"
	if device {
		status = task.Review
		next = task.OwnerName + "に、対象の端末で認証を済ませていただきます。"
	}
	link := "/routines"
	if trouble.Name != "" {
		link += "/" + url.PathEscape(trouble.Name)
	}
	body := "## ひとことで\n\n" + shortAction(trouble.Text) + "\n\n## 次にすること\n\n" + next + "\n\n[定期タスクの記録](" + link + ")\n"
	t, err := a.tasks.Create(task.NewTask{Area: areas[0], Title: trouble.Title + "の実行条件を確かめる", Status: status, Slug: slug, Body: body, Actor: "定期タスクの実行結果"})
	if err != nil || device {
		return err
	}
	return a.askTrouble("routine-health-"+t.ID, t.ID, trouble)
}

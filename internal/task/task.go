package task

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // some deployment images lack zoneinfo

	"github.com/imos/lacia/internal/caller"
)

// RevisionHeader announces the published Git revision of the task mirror.
const RevisionHeader = "X-Lacia-Tasks-Revision"

// Status says whose turn it is.
type Status string

const (
	Todo      Status = "todo"      // not started
	Doing     Status = "doing"     // Lacia is working on it
	Waiting   Status = "waiting"   // waiting on something outside
	Review    Status = "review"    // waiting on the user to decide, answer or act
	Closing   Status = "closing"   // the work is finished; the user only confirms it
	Done      Status = "done"      // finished
	Cancelled Status = "cancelled" // dropped
)

// Statuses lists every status in display order.
var Statuses = []Status{Review, Closing, Doing, Waiting, Todo, Done, Cancelled}

// StatusNames lists the statuses the way messages name them.
const StatusNames = "todo, doing, waiting, review, closing, done, cancelled"

func (s Status) Valid() bool {
	for _, v := range Statuses {
		if s == v {
			return true
		}
	}
	return false
}

// Closed reports whether the task needs no further attention.
func (s Status) Closed() bool { return s == Done || s == Cancelled }

// AwaitsUser reports whether it is the user's turn: to decide, answer or
// act (Review), or only to confirm that finished work may be closed
// (Closing).
func (s Status) AwaitsUser() bool { return s == Review || s == Closing }

// Label is the Japanese name shown to the user.
func (s Status) Label() string {
	switch s {
	case Todo:
		return "未着手"
	case Doing:
		return "進行中"
	case Waiting:
		return "外部待ち"
	case Review:
		return "確認待ち"
	case Closing:
		return "完了確認待ち"
	case Done:
		return "完了"
	case Cancelled:
		return "取りやめ"
	}
	return string(s)
}

// JST is the default time zone for every date the system writes.
var JST = mustZone("Asia/Tokyo")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// Task is one task.md, plus where it lives.
type Task struct {
	ID         string
	Title      string
	Status     Status
	Created    string // YYYY-MM-DD
	Done       string // YYYY-MM-DD, set when the task is closed
	Due        string
	Parent     string
	Project    string // directory name of the project the task belongs to
	Priority   string
	ResumeOn   string   // hide from the active lists until this date or time, then ask for review
	ReviewAt   string   // ask for review at this date or time, staying in the lists until then
	After      []string // tasks that have to be closed before this one can start
	OnReady    string   // "start" or "start <destination>"
	ReadySince string   // when the server moved the task to review because its conditions were met
	StoppedAt  string   // when the server stopped taking writes because the same one kept coming

	AfterRun      string // routine whose next finished run this task waits for
	AfterRunSince string // UTC RFC3339Nano timestamp of the registration
	ReadyRunRef   string // local link to the run that released the wait

	// A waiting task that is looked at again and again (recheck.go). The
	// next round is ResumeOn. The server writes the last three.
	RecheckEvery  string // how often: "1d", "3h"
	RecheckUntil  string // the date or time at which the owner is asked how to go on
	RecheckAsked  string // the round a conversation was asked to look at and has not answered
	RecheckMissed int    // rounds in a row that ended without a look that worked
	RecheckLast   string // when a look last found nothing new
	// Owner is the conversation working on the task while it is "doing",
	// such as "local-claude". OwnerSession is its key in the registry of
	// conversations.
	Owner        string
	OwnerSession string

	// Blockers is the part of After that is still open. The store fills it
	// in; it is not written to the file.
	Blockers []string

	Timezone       string // resolved repository zone, supplied by the store, not persisted
	Routine        bool   // the task of a routine, kept in its directory (see routine_task.go)
	routineActions routineActionState
	Area           string    // "work", "personal", ...
	Dir            string    // directory relative to the repository root
	Body           string    // Markdown after the front matter
	ModTime        time.Time // task.md modification time
	Unknown        []string  // front matter keys this version does not know
}

// OnReadyStart is the value of on_ready that makes Lacia begin the task
// when its conditions are met.
const OnReadyStart = "start"

// ReadyDestination reads an optional conversation address from on_ready.
func ReadyDestination(value string) (string, error) {
	if value == "" || value == OnReadyStart {
		return "", nil
	}
	command, to, ok := strings.Cut(value, " ")
	if ok && command == OnReadyStart && to != "" {
		if _, err := caller.ParseDestination(to); err == nil {
			return to, nil
		}
	}
	return "", fmt.Errorf("on_ready には start か start <宛先> を指定してください")
}

func (t Task) ReadyTarget() string {
	to, _ := ReadyDestination(t.OnReady)
	return to
}

func (t Task) StartsWhenReady() bool {
	if t.OnReady == "" {
		return false
	}
	_, err := ReadyDestination(t.OnReady)
	return err == nil
}

var knownKeys = []string{"id", "title", "status", "created", "done", "due", "parent", "priority", "project", "resume_on", "review_at", "after", "after_run", "after_run_since", "on_ready", "ready_since", "ready_run_ref", "stopped_at", "owner", "owner_session", "recheck_every", "recheck_until", "recheck_asked", "recheck_missed", "recheck_last"}

var (
	idPattern    = regexp.MustCompile(`^[A-Z][0-9]{1,6}$`)
	datePattern  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	afterRunSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func validAfterRun(value string) bool {
	return ValidRoutineID(value) || afterRunSlug.MatchString(value)
}

// ValidID reports whether s looks like a task ID such as T1001 or L24.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// parseTask reads a task.md. The task of a routine takes its title from
// routine.md and may have only the statuses in RoutineStatuses.
func parseTask(d Doc, routine bool) (Task, error) {
	t := Task{
		Routine:      routine,
		ID:           d.Get("id"),
		Title:        d.Get("title"),
		Status:       Status(d.Get("status")),
		Created:      d.Get("created"),
		Done:         d.Get("done"),
		Due:          d.Get("due"),
		Parent:       d.Get("parent"),
		Project:      d.Get("project"),
		Priority:     d.Get("priority"),
		ResumeOn:     d.Get("resume_on"),
		ReviewAt:     d.Get("review_at"),
		After:        ParseIDs(d.Get("after")),
		OnReady:      d.Get("on_ready"),
		ReadySince:   d.Get("ready_since"),
		StoppedAt:    d.Get("stopped_at"),
		Owner:        d.Get("owner"),
		OwnerSession: d.Get("owner_session"),
		Body:         d.Body,

		AfterRun:      d.Get("after_run"),
		AfterRunSince: d.Get("after_run_since"),
		ReadyRunRef:   readyRunReference(d.Get("ready_run_ref")),

		RecheckEvery: d.Get("recheck_every"),
		RecheckUntil: d.Get("recheck_until"),
		RecheckAsked: d.Get("recheck_asked"),
		RecheckLast:  d.Get("recheck_last"),
	}
	// What the server wrote is read leniently: a count nobody can read is none.
	t.RecheckMissed, _ = strconv.Atoi(d.Get("recheck_missed"))
	if routine {
		state, err := readRoutineActions(d)
		if err != nil {
			return t, err
		}
		t.routineActions = state
	}
	for _, f := range d.Fields {
		known := routine && f.Key == routineActionsField
		for _, k := range knownKeys {
			known = known || f.Key == k
		}
		if !known {
			t.Unknown = append(t.Unknown, f.Key)
		}
	}
	switch {
	case !ValidID(t.ID):
		return t, fmt.Errorf("id %q がタスクのIDの形になっていません", t.ID)
	case routine && !ValidRoutineID(t.ID):
		return t, fmt.Errorf("id %q が定期タスクのIDの形になっていません", t.ID)
	case routine && !t.Status.RoutineStatus():
		return t, fmt.Errorf("%s: status %q は定期タスクのタスクでは使えません。%s のどれかにしてください", t.ID, t.Status, RoutineStatusNames)
	case t.Title == "" && !routine:
		return t, fmt.Errorf("%s: title が空です", t.ID)
	case !t.Status.Valid():
		return t, fmt.Errorf("%s: status %q は使えません。%s のどれかにしてください", t.ID, t.Status, StatusNames)
	case !datePattern.MatchString(t.Created):
		return t, fmt.Errorf("%s: created %q が YYYY-MM-DD の形になっていません", t.ID, t.Created)
	}
	if t.Done != "" && !datePattern.MatchString(t.Done) {
		return t, fmt.Errorf("%s: done %q が YYYY-MM-DD の形になっていません", t.ID, t.Done)
	}
	for name, v := range map[string]string{"resume_on": t.ResumeOn, "review_at": t.ReviewAt} {
		if _, err := ParseWhen(v); v != "" && err != nil {
			return t, fmt.Errorf("%s: %s %w", t.ID, name, err)
		}
	}
	for _, id := range strings.FieldsFunc(d.Get("after"), func(r rune) bool { return r == ',' || r == ' ' || r == '、' }) {
		if !ValidID(id) {
			return t, fmt.Errorf("%s: after の %q がタスクのIDの形になっていません", t.ID, id)
		}
	}
	if t.RecheckEvery != "" {
		if _, err := ParseEvery(t.RecheckEvery); err != nil {
			return t, fmt.Errorf("%s: recheck_every: %w", t.ID, err)
		}
		if _, err := ParseWhen(t.RecheckUntil); err != nil {
			return t, fmt.Errorf("%s: recheck_until %w", t.ID, err)
		}
	}
	if t.AfterRun != "" && !validAfterRun(t.AfterRun) {
		return t, fmt.Errorf("%s: after_run には定期タスクのID（例 S1001）か従来の名前を指定してください", t.ID)
	}
	if _, err := ReadyDestination(t.OnReady); err != nil {
		return t, fmt.Errorf("%s: %w", t.ID, err)
	}
	if t.Parent != "" && !ValidID(t.Parent) {
		return t, fmt.Errorf("%s: parent %q がタスクのIDの形になっていません", t.ID, t.Parent)
	}
	return t, nil
}

func runRegisteredAt(value string) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, value)
	return at, err == nil && !at.IsZero() && strings.HasSuffix(value, "Z")
}

// readyRunReference accepts only local links to routine pages.
func readyRunReference(value string) string {
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\<>() \t\r\n") {
		return ""
	}
	link, err := url.Parse(value)
	if err != nil || link.Host != "" {
		return ""
	}
	if name, ok := strings.CutPrefix(link.Path, "/routines/"); ok && validAfterRun(name) {
		return value
	}
	return ""
}

// Next returns the "次にすること" section, the one thing lists show besides
// the title.
func (t Task) Next() string {
	if text := t.RoutineActionSummary(); text != "" {
		return text
	}
	return Section(withoutRoutineActionBlock(t.Body), "次にすること")
}

// HasReadyConditions reports the scheduling fields the server can act on.
func (t Task) HasReadyConditions() bool {
	return t.ResumeOn != "" || t.ReviewAt != "" || len(t.After) > 0 || t.AfterRun != ""
}

// MissingWaitingCondition reports a stored wait with no registered task or live condition.
// The task of a routine waits for the next run, which the routine itself
// schedules.
func (t Task) MissingWaitingCondition(hasLiveCondition bool) bool {
	return t.Status == Waiting && !t.Routine && !t.HasReadyConditions() && !hasLiveCondition
}

const WaitingConditionWarning = "再開条件が未登録です。いつ、何がそろえば戻るのかを登録してください。確かめようのない待ちには、「要対応」へ出す日時（review_at）を入れます。"

// ErrWaitingNeedsCondition is returned when a task would wait for something
// outside with nothing registered that brings it back. Such a task stops
// without anybody noticing.
var ErrWaitingNeedsCondition = errors.New("外部待ちには、再開の条件が要ります")

// conditionKeys are the front matter fields that bring a waiting task back.
var conditionKeys = []string{"resume_on", "review_at", "after", "after_run"}

// docHasReadyConditions is HasReadyConditions for a task.md being changed.
func docHasReadyConditions(d *Doc) bool {
	for _, key := range conditionKeys {
		if d.Get(key) != "" {
			return true
		}
	}
	return false
}

// waitingConditionHelp lists the ways to register a condition. A
// conversation that has never seen the rule has to be able to act on it.
func waitingConditionHelp(id string) string {
	return recheckHelp(id) + fmt.Sprintf("- 決めた日時に「要対応」へ出す: lacia task field %[1]s review_at \"2026-12-10 15:00\"（その日時まで一覧から外すときは resume_on）\n"+
		"- ほかのタスクの完了を待つ: lacia task field %[1]s after T1002\n"+
		"- 定期タスクの次の実行を待つ: lacia task field %[1]s after_run S1001\n"+
		"- クラスタの Job の終了を待つ: lacia wait add --task %[1]s ...（登録すると、外部待ちになります）\n"+
		"Web では、タスクのページの「着手の条件」で同じ指定ができます。\n"+
		"%[2]sの判断や操作を待つときは、外部待ちではなく確認待ちにします: lacia task set %[1]s review", id, OwnerName)
}

// WaitingNeedsCondition says why a task cannot wait yet and how to fix it.
func WaitingNeedsCondition(id string) error {
	return fmt.Errorf("%w。%s を外部待ち（waiting）にする前に、いつ、何がそろえば戻るのかを登録してください。条件のない外部待ちは、だれにも気づかれないまま止まるためです。確かめようのない待ちにも、「要対応」へ出す日時を入れます。\n%s\n登録したら、もう1度 lacia task set %s waiting を実行してください。",
		ErrWaitingNeedsCondition, id, waitingConditionHelp(id), id)
}

// lastConditionError refuses to take the only condition from a waiting task.
func lastConditionError(id, key string) error {
	return fmt.Errorf("%w。%s は外部待ちで、%s が最後の条件です。外すと、だれにも気づかれないまま止まります。先に別の条件を登録するか、lacia task set %s doing などで状態を変えてから外してください。\n%s",
		ErrWaitingNeedsCondition, id, key, id, waitingConditionHelp(id))
}

// ReadyLabel is a compact description of a task's readiness conditions.
// Keeping IDs separate lets the web view link each task and routine.
type ReadyLabel struct {
	Prefix        string
	Blockers      []string
	RoutinePrefix string
	Routine       string
	Suffix        string
}

func (l ReadyLabel) String() string {
	return l.Prefix + strings.Join(l.Blockers, "、") + l.RoutinePrefix + l.Routine + l.Suffix
}

// ReadyLabel describes when the task becomes ready and what happens then.
func (t Task) ReadyLabel(checking bool) ReadyLabel {
	if !t.HasReadyConditions() {
		return ReadyLabel{}
	}
	// The next round is no "ready" date: RecheckLabel describes it.
	if t.Rechecks() && t.Status == Waiting {
		return ReadyLabel{Prefix: t.RecheckLabel()}
	}
	label := ReadyLabel{Blockers: t.Blockers, Suffix: "要対応"}
	if t.StartsWhenReady() {
		label.Suffix = "再開"
	}

	when := t.ResumeOn
	if t.ReviewAt != "" {
		loc := (Config{Timezone: t.Timezone}).Location()
		resume, resumeErr := ParseWhenIn(t.ResumeOn, loc)
		review, reviewErr := ParseWhenIn(t.ReviewAt, loc)
		if t.ResumeOn == "" || reviewErr == nil && (resumeErr != nil || !review.Before(resume)) {
			when = t.ReviewAt
			if checking {
				label.Prefix = "次の確認日時 "
			}
		}
	}
	if when != "" {
		label.Prefix += strings.Replace(when, "T", " ", 1) + "以降"
	}
	if len(label.Blockers) > 0 {
		if when != "" {
			label.Prefix += "かつ"
		}
	}
	if t.AfterRun != "" {
		label.Routine = t.AfterRun
		label.RoutinePrefix = "定期タスク "
		if len(label.Blockers) > 0 {
			label.RoutinePrefix = "終了後かつ" + label.RoutinePrefix
		} else if when != "" {
			label.RoutinePrefix = "かつ" + label.RoutinePrefix
		}
		if _, ok := runRegisteredAt(t.AfterRunSince); ok {
			label.Suffix = "次回終了後" + label.Suffix
		} else {
			label.Suffix = "の待機条件が未登録"
		}
	} else if len(label.Blockers) > 0 {
		label.Suffix = "終了後" + label.Suffix
	} else if when == "" {
		label.Prefix = "依存先終了後"
	}
	return label
}

// Snoozed reports whether the task asked to stay out of the active lists
// until a later date.
func (t Task) Snoozed(now time.Time) bool {
	if t.Status.Closed() {
		return false
	}
	if (len(t.Blockers) > 0 || t.AfterRun != "") && !t.Status.AwaitsUser() {
		return true
	}
	loc := JST
	if t.Timezone != "" {
		if configured, err := time.LoadLocation(t.Timezone); err == nil {
			loc = configured
		}
	}
	when, err := parseWhenIn(t.ResumeOn, loc)
	return err == nil && t.ResumeOn != "" && when.After(now)
}

// ListStatus places a task in the dashboard without changing its stored status.
func (t Task) ListStatus(now time.Time, jobRunning, requestQueued bool) Status {
	if (t.Status == Waiting || t.Status == Doing) && jobRunning {
		return Doing
	}
	if t.Snoozed(now) || (t.Status == Doing && t.Owner == "" && requestQueued) {
		return Waiting
	}
	return t.Status
}

// ParseIDs reads a list of task IDs separated by commas or spaces.
func ParseIDs(s string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '、' }) {
		if id = strings.ToUpper(id); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// ParseWhen reads a date ("2026-11-19", meaning the start of that day) or a
// date and time ("2026-11-19 15:00"), in Japan time.
func ParseWhen(s string) (time.Time, error) {
	return ParseWhenIn(s, JST)
}

func parseWhenIn(s string, loc *time.Location) (time.Time, error) { return ParseWhenIn(s, loc) }

// ParseWhenIn reads a repository-local date or date and time.
func ParseWhenIn(s string, loc *time.Location) (time.Time, error) {
	s = strings.Replace(strings.TrimSpace(s), "T", " ", 1)
	for _, layout := range []string{time.DateOnly, "2006-01-02 15:04"} {
		if when, err := time.ParseInLocation(layout, s, loc); err == nil && when.Format(layout) == s {
			return when, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q を日付や日時として読めません。2026-11-19 か 2026-11-19 15:00 の形で書いてください", s)
}

func formatWhenIn(when time.Time, loc *time.Location) string { return FormatWhenIn(when, loc) }

// FormatWhenIn writes a date or date and time in the repository zone.
func FormatWhenIn(when time.Time, loc *time.Location) string {
	when = when.In(loc)
	if when.Hour() == 0 && when.Minute() == 0 {
		return when.Format(time.DateOnly)
	}
	return when.Format("2006-01-02 15:04")
}

// Slug returns the part of the directory name after the ID, if any.
func (t Task) Slug() string {
	name := t.Dir[strings.LastIndex(t.Dir, "/")+1:]
	return strings.TrimPrefix(strings.TrimPrefix(name, t.ID), "-")
}

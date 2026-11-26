// Package schedule runs definitions from the record repository at the right
// times. One job is one file, however often it runs.
package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/task"
)

// Job is a fixed-ID routine definition or a legacy schedule.
type Job struct {
	ID          string   // stable S-series identity; empty for legacy schedules
	Area        string   // optional display grouping
	Output      string   // matters, task, or none
	CheckResult bool     // require an explicit structured coverage report
	CheckScopes []string // expected scopes, captured with each execution
	LegacyName  string   // old slug; keeps queued request IDs and boundaries stable
	WeekdayMask uint8    // zero means every day
	Path        string   // repository-relative definition path
	Profile     string   // machine-local execution profile; empty preserves existing behavior
	To          string   // destination; omitted for the server default
	Name        string
	Title       string
	Every       time.Duration // run every interval, aligned to local midnight
	Daily       string        // local times, in ascending order
	Task        string        // the task the job belongs to; it stops when the task is closed
	location    *time.Location
	Side        bool   // use the side conversation, not the main one
	ReplyTo     string // where the result is posted, e.g. "discord:<channel>"
	Enabled     bool
	Prompt      string
	Err         string // why the definition cannot be used
	// Late is set on a job handed to Submit when it runs well after its
	// time: a note for the prompt, saying when it was due and what it
	// stands for.
	Late string
	// Slot is the occurrence a job handed to Submit stands for, and Skipped
	// is how many occurrences before it never ran. The run log uses both.
	Slot    time.Time
	Skipped int

	ExplicitMatters bool `json:",omitempty"` // review policy captured at enqueue
}

func (j Job) zone() *time.Location {
	if j.location == nil {
		return task.JST
	}
	return j.location
}

// When describes the timing in Japanese.
func (j Job) When() string {
	switch {
	case j.Every > 0:
		if j.Every%time.Minute == 0 {
			return fmt.Sprintf("%d分ごと", j.Every/time.Minute)
		}
		return fmt.Sprintf("%g秒ごと", j.Every.Seconds())
	case j.Daily != "":
		return "毎日 " + j.Daily
	}
	return "未設定"
}

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	clockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// Load reads every definition. A broken file becomes a Job with Err set, so
// the problem is visible instead of the job silently not running.
func Load(root string) []Job {
	cfg, err := task.LoadConfig(root)
	if err != nil {
		return []Job{{Err: err.Error()}}
	}
	return loadIn(root, cfg, cfg.Location())
}

// LoadIn reads the definitions using a repository's already resolved zone.
func LoadIn(root string, loc *time.Location) []Job {
	cfg, err := task.LoadConfig(root)
	if err != nil {
		return []Job{{Err: err.Error()}}
	}
	return loadIn(root, cfg, loc)
}

func loadIn(root string, cfg task.Config, loc *time.Location) []Job {
	paths := map[string]string{}
	areas := cfg.Areas
	if cfg.Format < task.AreaRoutineFormat {
		areas = []string{""}
	}
	for _, area := range areas {
		matches, _ := filepath.Glob(filepath.Join(root, task.RoutineBase(cfg.Format, area), "*", "routine.md"))
		for _, filename := range matches {
			paths[filename] = area
		}
	}
	files := make([]string, 0, len(paths))
	for filename := range paths {
		files = append(files, filename)
	}
	sort.Strings(files)
	var jobs []Job
	ids := map[string]int{}
	aliases := map[string]int{}
	for _, path := range files {
		id := filepath.Base(filepath.Dir(path))
		job := Job{Name: id, ID: id, Enabled: true, location: loc}
		job.Path, _ = filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err == nil {
			err = parse(string(data), &job)
		}
		if err == nil && !task.ValidRoutineID(id) {
			err = fmt.Errorf("定期タスクのディレクトリー名には S1001 のような固定IDを使ってください")
		}
		if area := paths[path]; area != "" {
			if job.Area != "" && job.Area != area {
				err = fmt.Errorf("area %q が定期タスクの領域 %q と一致しません", job.Area, area)
			}
			job.Area = area
		}
		if previous, exists := ids[id]; exists {
			err = fmt.Errorf("定期タスクID %s が重複しています", id)
			jobs[previous].Err, jobs[previous].Enabled = err.Error(), false
		} else {
			ids[id] = len(jobs)
		}
		if job.LegacyName != "" {
			if previous, exists := aliases[job.LegacyName]; exists {
				err = fmt.Errorf("legacy_name %q が重複しています", job.LegacyName)
				jobs[previous].Err, jobs[previous].Enabled = err.Error(), false
			} else {
				aliases[job.LegacyName] = len(jobs)
			}
		}
		if err != nil {
			job.Err, job.Enabled = err.Error(), false
		}
		jobs = append(jobs, job)
	}
	legacyPaths, _ := filepath.Glob(filepath.Join(root, "schedules", "*.md"))
	sort.Strings(legacyPaths)
	for _, path := range legacyPaths {
		name := strings.TrimSuffix(filepath.Base(path), ".md")
		if name == "README" {
			continue
		}
		if cfg.Format >= task.AreaRoutineFormat {
			rel, _ := filepath.Rel(root, path)
			jobs = append(jobs, Job{Name: name, Path: rel, Err: "形式2では schedules/ の定義は実行しません。areas/<領域>/routines/<ID>/routine.md へ移してください"})
			continue
		}
		// Even an invalid replacement suppresses the old definition: an error
		// must not accidentally reactivate the previous schedule.
		if _, replaced := aliases[name]; replaced {
			continue
		}
		job := Job{Name: name, Enabled: true, location: loc}
		job.Path, _ = filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err == nil {
			err = parse(string(data), &job)
		}
		if err == nil && !namePattern.MatchString(name) {
			err = fmt.Errorf("ファイル名には英小文字、数字、ハイフンだけを使えます")
		}
		if err != nil {
			job.Err, job.Enabled = err.Error(), false
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// ValidName accepts both fixed routine IDs and historical schedule slugs.
func ValidName(name string) bool {
	return task.ValidRoutineID(name) || namePattern.MatchString(name)
}

// Resolve finds a definition by its stable ID or historical slug.
func Resolve(root, name string) (Job, bool) {
	for _, job := range Load(root) {
		if job.Name == name || job.LegacyName != "" && job.LegacyName == name {
			return job, true
		}
	}
	return Job{}, false
}

// ResolveWait accepts an empty value to clear a wait or resolves a schedule
// name to the fixed ID used in its execution history.
func ResolveWait(root, value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", nil
	}
	if job, ok := Resolve(root, name); ok && ValidName(job.Name) {
		return job.Name, nil
	}
	return "", fmt.Errorf("%q という定期タスクは見つかりません。定期タスクのIDを指定してください", name)
}

func parse(src string, job *Job) error {
	doc, err := task.ParseDoc(src)
	if err != nil {
		return err
	}
	job.Title, job.Prompt = doc.Get("title"), strings.TrimSpace(doc.Body)
	job.Area, job.Output, job.LegacyName = doc.Get("area"), doc.Get("output"), doc.Get("legacy_name")
	switch doc.Get("check_result") {
	case "true":
		job.CheckResult = true
	case "", "false":
	default:
		return fmt.Errorf("check_result は true か false を指定してください")
	}
	if value := doc.Get("weekdays"); value != "" {
		for _, word := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(word)) {
			case "sun", "sunday", "日":
				job.WeekdayMask |= 1 << time.Sunday
			case "mon", "monday", "月":
				job.WeekdayMask |= 1 << time.Monday
			case "tue", "tuesday", "火":
				job.WeekdayMask |= 1 << time.Tuesday
			case "wed", "wednesday", "水":
				job.WeekdayMask |= 1 << time.Wednesday
			case "thu", "thursday", "木":
				job.WeekdayMask |= 1 << time.Thursday
			case "fri", "friday", "金":
				job.WeekdayMask |= 1 << time.Friday
			case "sat", "saturday", "土":
				job.WeekdayMask |= 1 << time.Saturday
			default:
				return fmt.Errorf("weekdays %q は Mon, Wed のように曜日を指定してください", value)
			}
		}
	}
	if job.LegacyName != "" && !namePattern.MatchString(job.LegacyName) {
		return fmt.Errorf("legacy_name は旧予約の名前で指定してください")
	}
	if job.ID != "" && doc.Get("id") != job.ID {
		return fmt.Errorf("id はディレクトリー名 %s とそろえてください", job.ID)
	}
	switch job.Output {
	case "":
		if job.ID != "" {
			job.Output = "matters"
		}
	case "matters", "task", "none":
	default:
		return fmt.Errorf("output は matters、task、none のどれかです")
	}
	if value := doc.Get("check_scopes"); value != "" {
		if !job.CheckResult {
			return fmt.Errorf("check_scopes を使うときは check_result: true を指定してください")
		}
		seen := map[string]bool{}
		for _, scope := range strings.Split(value, ",") {
			scope = strings.TrimSpace(scope)
			if scope == "" || len(scope) > 1024 || seen[scope] {
				return fmt.Errorf("check_scopes には重複しない確認範囲を指定してください")
			}
			seen[scope] = true
			job.CheckScopes = append(job.CheckScopes, scope)
		}
		if len(job.CheckScopes) > 500 {
			return fmt.Errorf("check_scopes が多すぎます")
		}
	}

	timings := 0
	if v := doc.Get("every"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute || d > 24*time.Hour || (24*time.Hour)%d != 0 {
			return fmt.Errorf("every %q には、30m や 2h のように、1分以上で24時間を割り切れる間隔を指定してください", v)
		}
		job.Every, timings = d, timings+1
	}
	if v := doc.Get("daily"); v != "" {
		times := strings.Split(v, ",")
		for i, clock := range times {
			times[i] = strings.TrimSpace(clock)
			if !clockPattern.MatchString(times[i]) {
				return fmt.Errorf("daily %q は HH:MM の形で、複数あるときはコンマで区切って書いてください", v)
			}
		}
		sort.Strings(times)
		for i := 1; i < len(times); i++ {
			if times[i] == times[i-1] {
				return fmt.Errorf("daily の時刻 %q が重複しています", times[i])
			}
		}
		job.Daily, timings = strings.Join(times, ", "), timings+1
	}
	// A job that runs once is not a schedule: it is a task that Lacia
	// begins at a time (resume_on or review_at, with on_ready).
	if doc.Get("at") != "" {
		return fmt.Errorf("1回だけの仕事には at を使えません。タスクに着手する日時を設定し、lacia task field <ID> on_ready start を実行してください")
	}
	if timings != 1 {
		return fmt.Errorf("every か daily のどちらか1つを書いてください")
	}
	switch doc.Get("conversation") {
	case "", "main":
	case "side":
		job.Side = true
	default:
		return fmt.Errorf("conversation は main か side です")
	}
	if doc.Has("target") {
		return fmt.Errorf("予約の target は廃止しました。to に場所か「場所-種類」を指定してください")
	}
	job.Profile = doc.Get("profile")
	if job.Profile != "" && !login.ValidExecutionProfile(job.Profile) {
		return fmt.Errorf("profile は端末に登録した名前で指定してください")
	}
	job.To = doc.Get("to")
	if _, err := caller.ParseDestination(job.To); err != nil {
		return err
	}
	job.ReplyTo = doc.Get("reply")
	job.Task = doc.Get("task")
	if job.Task != "" && !task.ValidID(job.Task) {
		return fmt.Errorf("task %q はタスクのIDで指定してください", job.Task)
	}
	switch doc.Get("enabled") {
	case "", "true":
	case "false":
		job.Enabled = false
	default:
		return fmt.Errorf("enabled は true か false です")
	}
	if job.Prompt == "" {
		return fmt.Errorf("依頼の文章がありません")
	}
	return nil
}

// due returns the occurrence that should run at now, and whether there is
// one. A job runs at most once per occurrence. Occurrences missed while the
// server was not running, such as during sleep or a restart, are not
// replayed one by one: only the latest one runs, once the server is back.
// since is when the server last looked; an occurrence it slept through runs
// however late it is. Without that, a daily job more than grace late is
// skipped, so that a job newly written with a time in the past does not
// start at once.
func (j Job) due(now, since time.Time, grace time.Duration) (time.Time, bool) {
	now = now.In(j.zone())
	missed := func(at time.Time) bool { return !since.IsZero() && at.After(since) }
	if !j.Enabled {
		return time.Time{}, false
	}
	lookback := 1
	if j.WeekdayMask != 0 {
		lookback = 7
	}
	for offset := 0; offset >= -lookback; offset-- {
		day := now.AddDate(0, 0, offset)
		if !j.onDay(day) {
			continue
		}
		date := day.Format(time.DateOnly)
		var latest time.Time
		if j.Every > 0 {
			if offset != 0 {
				return time.Time{}, false
			}
			midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, j.zone())
			return midnight.Add(now.Sub(midnight) / j.Every * j.Every), true
		}
		for _, clock := range strings.Split(j.Daily, ", ") {
			slot := date + "T" + clock
			at, err := time.ParseInLocation(slotLayout, slot, j.zone())
			if err == nil && at.Format(slotLayout) == slot && !now.Before(at) && at.After(latest) {
				latest = at
			}
		}
		if !latest.IsZero() {
			return latest, missed(latest) || offset == 0 && now.Sub(latest) <= grace
		}
	}
	return time.Time{}, false
}

const slotLayout = "2006-01-02T15:04"

// skipped counts the occurrences between last, the one that ran before, and
// at that never ran.
func (j Job) skipped(at time.Time, last string) int {
	prev, err := time.ParseInLocation(slotLayout, last, j.zone())
	if err != nil || !at.After(prev) {
		return 0
	}
	n := 0
	switch {
	case j.Every > 0:
		if j.WeekdayMask == 0 {
			n = int(at.Sub(prev)/j.Every) - 1
		} else {
			for slot := prev.Add(j.Every); slot.Before(at); slot = slot.Add(j.Every) {
				if j.onDay(slot) {
					n++
				}
			}
		}
	case j.Daily != "":

		clocks := strings.Split(j.Daily, ", ")
		for date := time.Date(prev.Year(), prev.Month(), prev.Day(), 12, 0, 0, 0, j.zone()); date.Format(time.DateOnly) <= at.Format(time.DateOnly); date = date.AddDate(0, 0, 1) {
			if !j.onDay(date) {
				continue
			}
			for _, clock := range clocks {
				slot := date.Format(time.DateOnly) + "T" + clock
				occurrence, err := time.ParseInLocation(slotLayout, slot, j.zone())
				if err == nil && occurrence.Format(slotLayout) == slot && occurrence.After(prev) && occurrence.Before(at) {
					n++
				}
			}
		}
	}
	return max(n, 0)
}

// lateNote tells a job that runs well after its time what happened, so that
// it can fit what it does to the hour it actually runs at. last is the
// occurrence that ran before this one.
func (j Job) lateNote(at, now time.Time, last string) string {
	late := now.Sub(at)
	skipped := j.skipped(at, last)
	// An interval job is always a little into its slot; that is not late.
	if (j.Every > 0 || late < 30*time.Minute) && skipped <= 0 {
		return ""
	}
	note := "\n\n## 遅れて実行しています\n\n"
	if late >= 30*time.Minute && j.Every == 0 {
		note += fmt.Sprintf("この回の予定は%sでした。いまは%sです。", at.Format("1月2日 15:04"), now.In(j.zone()).Format("1月2日 15:04"))
	}
	if skipped > 0 {
		note += fmt.Sprintf("予定どおりに実行できなかった%d回分は今回にまとめ、個別にはやり直しません。", skipped)
	}
	return note + "この時刻に相手が必要としていることを考えてください。いまから行っても役に立たない回は見送り、そのことだけを短く書いてください。\n"
}

// Runner starts the jobs that are due.
type Runner struct {
	Root       string // task repository
	Location   *time.Location
	TaskClosed func(string) bool
	State      string        // file remembering the last slot of each job
	Grace      time.Duration // how late a daily job may still run
	// Submit queues one occurrence. id is stable for the slot, so a
	// repeated submit is harmless.
	Submit func(id string, job Job) error
	Check  func([]Job) []Job

	mu   sync.Mutex
	last map[string]string
	// seen is when Tick last ran, kept in the state file under tickKey.
	seen time.Time
}

// tickKey is not a job: it keeps the time of the last look.
const tickKey = "_tick"

// Last returns the slot each job last ran in.
func (r *Runner) Last() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.load()
	out := map[string]string{}
	for k, v := range r.last {
		if k != tickKey {
			out[k] = v
		}
	}
	return out
}

func (r *Runner) load() {
	if r.last != nil {
		return
	}
	r.last = map[string]string{}
	if data, err := os.ReadFile(r.State); err == nil {
		json.Unmarshal(data, &r.last)
	}
	r.seen, _ = time.Parse(time.RFC3339, r.last[tickKey])
}

// Tick submits every job whose latest occurrence has not run yet.
func (r *Runner) Tick(now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.load()
	grace := r.Grace
	if grace == 0 {
		grace = 6 * time.Hour
	}
	changed := false
	var firstErr error
	jobs := Load(r.Root)
	if r.Location != nil {
		jobs = LoadIn(r.Root, r.Location)
	}
	if r.Check != nil {
		jobs = r.Check(jobs)
	}
	for _, job := range jobs {
		if job.Task != "" && r.TaskClosed != nil && r.TaskClosed(job.Task) {
			continue
		}
		if job.ID != "" && !job.Enabled && job.Err == "" {
			at, _ := withEnabled(job).due(now, r.seen, grace)
			if !at.IsZero() {
				slot := at.Format(slotLayout)
				if max(r.last[job.Name], r.last[job.OccurrenceName()]) < slot {
					r.last[job.Name], r.last[job.OccurrenceName()], changed = slot, slot, true
				}
			}
			continue
		}
		at, ok := job.due(now, r.seen, grace)
		slot := at.Format(slotLayout)
		previous := max(r.last[job.Name], r.last[job.OccurrenceName()])
		if !ok || previous >= slot {
			continue
		}
		id := requestID(job.OccurrenceName(), slot)
		job.Late = job.lateNote(at, now, previous)
		job.Slot, job.Skipped = at, job.skipped(at, previous)
		if err := r.Submit(id, job); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", job.Name, err)
			}
			continue
		}
		r.last[job.Name], r.last[job.OccurrenceName()], changed = slot, slot, true
	}
	// The time of this look is written about once a minute: it only has to
	// tell a sleep of hours from a server that kept running.
	if changed || now.Sub(r.seen) >= time.Minute {
		r.seen = now
		r.last[tickKey] = now.UTC().Format(time.RFC3339)
		data, _ := json.MarshalIndent(r.last, "", "  ")
		if err := os.MkdirAll(filepath.Dir(r.State), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(r.State, append(data, '\n'), 0o600); err != nil {
			return err
		}
	}
	return firstErr
}

func requestID(name, slot string) string {
	return "sched-" + name + "-" + strings.NewReplacer(":", "", "T", "-").Replace(slot)
}

// situationLimit keeps a runaway situation file from crowding out the job.
const situationLimit = 3000

var weekdays = [...]string{"日", "月", "火", "水", "木", "金", "土"}

// Situation is appended to the prompt of every scheduled job. A job runs in
// a conversation that has not seen what people or the owner said, and
// its definition assumes an ordinary day. The file at rel (a short document
// the main conversation keeps) tells it when the day is not ordinary, so it
// can serve the purpose of the job instead of its letter.
func Situation(root, rel string, now time.Time) string {
	cfg, _ := task.LoadConfig(root)
	return SituationIn(root, rel, now, cfg.Location())
}

var situationPeriod = regexp.MustCompile(`^\s*-\s+(\d{4}-\d{2}-\d{2})\s*[〜～]\s*(\d{4}-\d{2}-\d{2})(?:\s|[：:]|$)`)

// SituationIn uses the time zone of the repository that owns the request.
func SituationIn(root, rel string, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = task.JST
	}
	now = now.In(loc)
	var b strings.Builder
	b.WriteString("\n\n---\n\nここからは、きょうの日付と「いまの状況」です。\n\n")
	fmt.Fprintf(&b, "きょうは %s（%s）です。\n\n", now.Format("2006-01-02"), weekdays[now.Weekday()])
	text := ""
	if rel != "" {
		data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		today := now.Format(time.DateOnly)
		var lines []string
		for _, line := range strings.Split(string(data), "\n") {
			period := situationPeriod.FindStringSubmatch(line)
			if period != nil && period[2] < today {
				start, startErr := time.Parse(time.DateOnly, period[1])
				end, endErr := time.Parse(time.DateOnly, period[2])
				if startErr == nil && endErr == nil && !end.Before(start) {
					continue
				}
			}
			lines = append(lines, line)
		}
		text = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	if text == "" {
		b.WriteString("ふだんと違う状況について、ここに添えられる記録はありません。\n\n")
	} else {
		if r := []rune(text); len(r) > situationLimit {
			text = string(r[:situationLimit]) + "\n（長いので、ここで切りました。続きは `" + rel + "` にあります。）"
		}
		fmt.Fprintf(&b, "`%s` の内容：\n\n%s\n\n", rel, text)
	}
	b.WriteString("この仕事はだれのためか、その方がきょう何を必要としているかを確かめてください。すでに終わった期間の記述は、判断に使わないでください。状況がふだんと違うときは、`AGENTS.md` が案内する「目的に合わせる」に従って仕事の内容を調整し、変えた点と理由を報告に1行で添えてください。")
	return b.String()
}

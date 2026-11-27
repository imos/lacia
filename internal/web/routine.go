package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// A signed view binds completion to the receipts shown on that page.
// Keys live for one server lifetime: an old page must be refreshed after restart.
type routineView struct {
	ID          string
	Through     uint64
	Sequence    uint64
	OperationID string
}

func (s *Server) initRoutineKey() error {
	if len(s.routineKey) > 0 {
		return nil
	}
	s.routineKey = make([]byte, 32)
	_, err := rand.Read(s.routineKey)
	return err
}
func (s *Server) routineToken(id string, through, sequence uint64) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	b, err := json.Marshal(routineView{ID: id, Through: through, Sequence: sequence, OperationID: base64.RawURLEncoding.EncodeToString(nonce)})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.routineKey)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (s *Server) readRoutineToken(token, id string) (routineView, error) {
	var view routineView
	a, b, ok := strings.Cut(token, ".")
	if !ok {
		return view, fmt.Errorf("この画面からは確認済みにできません。ページを開き直して、もう1度確認してください。")
	}
	raw, e := base64.RawURLEncoding.DecodeString(a)
	sig, e2 := base64.RawURLEncoding.DecodeString(b)
	mac := hmac.New(sha256.New, s.routineKey)
	mac.Write(raw)
	if e != nil || e2 != nil || !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(raw, &view) != nil || view.ID != id || view.OperationID == "" {
		return routineView{}, fmt.Errorf("この画面からは確認済みにできません。ページを開き直して、もう1度確認してください。")
	}
	return view, nil
}
func routineQuery(r *http.Request) string {
	if r.URL.RawQuery != "" {
		return "?" + r.URL.RawQuery
	}
	return ""
}
func (s *Server) routineJobs() []schedule.Job {
	if s.Schedules != nil {
		jobs, _ := s.Schedules()
		return jobs
	}
	return schedule.LoadIn(s.Store.Root(), s.Store.Location())
}
func (s *Server) findRoutine(id string) (schedule.Job, bool) {
	for _, j := range s.routineJobs() {
		if j.ID != "" && (j.Name == id || j.LegacyName == id) {
			return j, true
		}
	}
	return schedule.Job{}, false
}

// oldRoutineID resolves fixed IDs left in existing task and file URLs.
func (s *Server) oldRoutineID(id string) (schedule.Job, bool) {
	if !task.ValidRoutineID(id) {
		return schedule.Job{}, false
	}
	job, found := s.findRoutine(id)
	return job, found && job.ID != id && job.Err == ""
}

type routineRow struct {
	schedule.Job
	Snapshot   routine.Snapshot
	Problem    schedule.Run
	Missed     int
	Next       time.Time
	TaskStatus string
}

func receiptBrief(receipt routine.Receipt) string {
	if text := summary(receipt.Summary, 90); text != "" {
		return text
	}
	for _, matter := range receipt.Matters {
		if text := summary(matter.Summary, 90); text != "" {
			return text
		}
	}
	if len(receipt.Unverified) > 0 {
		return "まだ確かめられていない範囲があります。"
	}
	return ""
}

type routineRunRow struct {
	schedule.Run
	Receipt uint64
}

type routineReceiptRow struct {
	routine.Receipt
	OutcomeLabel string
	RunURL       string
	RunAt        time.Time
}

func receiptOutcomeLabel(receipt routine.Receipt, run schedule.Run) string {
	if receipt.Outcome != "" {
		return (schedule.Run{Outcome: receipt.Outcome}).OutcomeLabel()
	}
	if run.Outcome != "" {
		return run.ProblemLabel()
	}
	return "結果の区分は記録されていません"
}

type routinePage struct {
	routineRow
	ActionsEnabled bool
	Token          string
	Flash          string
	Months         []string
	Month          string
	Runs           []routineRunRow
	Receipts       []routineReceiptRow
	Health         schedule.Health
	Executions     []schedule.Execution
	PromptHTML     template.HTML
	DefinitionURL  string
	// RoutineTask is the task of the routine (task.md in its directory),
	// shown at the top of the page. nil when the routine has none.
	RoutineTask *routineTaskPart
	// History is the routine's log.jsonl: changes to its task and to its
	// definition. HistoryURL opens all of it.
	History    []historyGroup
	HistoryURL string
}

// routineTaskPart is the part of a routine's page that shows its task, with
// the same pieces as a task page.
type routineTaskPart struct {
	Task        task.Task
	StatusLabel string
	Since       string // when the task entered its status
	Body        template.HTML
	Notes       template.HTML
	Artifacts   []artifactInfo
	Stored      []artifactInfo
	Warnings    []task.Warning
	Actions     []Action
}

// routineActions are the status buttons of a routine's task: it only moves
// between doing, waiting and review.
func routineActions(st task.Status) []Action {
	switch st {
	case task.Review:
		return []Action{{task.Waiting, "待機に戻す", true}, {task.Doing, "進行中にする", false}}
	case task.Doing:
		return []Action{{task.Waiting, "待機に戻す", false}, {task.Review, "確認待ちにする", false}}
	}
	return []Action{{task.Review, "確認待ちにする", false}, {task.Doing, "進行中にする", false}}
}

// routineStatusLabel names the status of a routine's task. Waiting is the
// usual state between runs, so it is called 待機 rather than 外部待ち.
func routineStatusLabel(st task.Status) string {
	if st == task.Waiting {
		return "待機"
	}
	return st.Label()
}

// routineTask reads the task of a routine, if it has one. A task.md that a
// conversation on the server wrote directly is picked up here.
func (s *Server) routineTask(id string) (task.Task, bool) {
	if err := s.Store.Refresh(id); err != nil && !errors.Is(err, task.ErrNotFound) {
		slog.Warn("refresh", "routine", id, "error", err)
	}
	t, err := s.Store.Get(id)
	return t, err == nil && t.Routine
}

func (s *Server) routineTaskPart(t task.Task, lines []task.Line) *routineTaskPart {
	root, loc := s.Store.Root(), s.Store.Location()
	part := &routineTaskPart{Task: t, StatusLabel: routineStatusLabel(t.Status), Body: renderMarkdown(t.Body, t.Dir), Actions: routineActions(t.Status)}
	if s.Store.Format() >= 1 {
		if since := statusSince(lines, t.Status, loc); since != "日時不明" {
			part.Since = since
		}
	}
	dir := filepath.Join(root, t.Dir)
	var artifacts, stored []string
	filepath.WalkDir(filepath.Join(dir, task.ArtifactsDir), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			rel, _ := filepath.Rel(dir, p)
			artifacts = append(artifacts, filepath.ToSlash(rel))
		}
		return nil
	})
	for _, f := range s.Store.StoredFiles(t) {
		stored = append(stored, task.FilesDir+"/"+f.Path)
	}
	base := schedule.RoutineURL(t.ID)
	part.Artifacts = artifactInfos(root, t, artifacts, lines, loc)
	part.Stored = artifactInfos(root, t, stored, lines, loc)
	setMaterialURLs(part.Artifacts, base)
	setMaterialURLs(part.Stored, base)
	if notes, err := os.ReadFile(filepath.Join(dir, task.NotesFile)); err == nil {
		part.Notes = renderMarkdown(string(notes), t.Dir)
	}
	part.Warnings = append(task.CheckFormat(t, s.Store.Files(t), s.Store.Format()), task.LintFormat(t.Body, s.Store.Format())...)
	part.Warnings = append(part.Warnings, s.Store.FileWarnings(t)...)
	return part
}

type routineList struct {
	ActionsEnabled bool
	Rows           []routineRow
}

func (s *Server) routines(w http.ResponseWriter, r *http.Request) {
	list := routineList{ActionsEnabled: s.routineActions()}
	var rows []routineRow
	for _, job := range s.routineJobs() {
		if job.ID == "" {
			rows = append(rows, routineRow{Job: job, Next: job.Next(s.Now())})
			continue
		}
		health := schedule.HealthOf(s.Store.Root(), job.Name)
		row := routineRow{Job: job, Problem: health.LatestExecution, Missed: health.Missed, Next: job.Next(s.Now())}
		if list.ActionsEnabled {
			if s.Store.KeepsTasks() {
				if t, ok := s.routineTask(job.Name); ok {
					row.TaskStatus = routineStatusLabel(t.Status)
				}
			}
		} else {
			snapshot, err := s.Routines.Snapshot(job.Name)
			if err != nil {
				s.fail(w, 500, "定期タスクの結果を読めませんでした。")
				return
			}
			row.Snapshot = snapshot
		}
		rows = append(rows, row)
	}
	list.Rows = rows
	s.render(w, 200, "routines", "定期タスク", list)
}
func addReviewRow(sec *Section, row Row) {
	for i := range sec.Blocks {
		if sec.Blocks[i].Area == row.Area {
			sec.Blocks[i].Rows = append(sec.Blocks[i].Rows, row)
			sec.Count++
			return
		}
	}
	sec.Blocks = append(sec.Blocks, Block{Area: row.Area, Rows: []Row{row}})
	sec.Count++
}

func (s *Server) addRoutineTaskReviews(sec *Section) {
	seen := map[string]bool{}
	for _, job := range s.routineJobs() {
		if !task.ValidRoutineID(job.ID) || seen[job.ID] {
			continue
		}
		seen[job.ID] = true
		t, ok := s.routineTask(job.ID)
		if !ok || t.Status != task.Review {
			continue
		}
		next := strings.TrimSpace(t.RoutineActionSummary())
		if next == "" {
			next = t.Next()
		}
		next = summary(next, 110)
		if next == "" {
			next = "タスクの本文で、お願いしたいことを確かめてください。"
		}
		addReviewRow(sec, Row{Task: t, Routine: true, URL: schedule.RoutineURL(t.ID), RoutineTaskNext: next})
	}
}

func (s *Server) addRoutineReview(sec *Section, troubles []schedule.Trouble) error {
	byRoutine := map[string][]string{}
	for _, trouble := range troubles {
		if task.ValidRoutineID(trouble.Name) {
			byRoutine[trouble.Name] = append(byRoutine[trouble.Name], trouble.Text)
			continue
		}
		link := "/routines"
		if trouble.Name != "" {
			link = schedule.RoutineURL(trouble.Name)
		}
		addReviewRow(sec, Row{Task: task.Task{Title: trouble.Title, Status: task.Review}, URL: link, Next: trouble.Text})
	}
	seen := map[string]bool{}
	for _, job := range s.routineJobs() {
		if !task.ValidRoutineID(job.ID) || seen[job.ID] {
			continue
		}
		seen[job.ID] = true
		snap, err := s.Routines.Snapshot(job.ID)
		if err != nil {
			if job.Err == "" {
				return err
			}
		}
		issues := byRoutine[job.ID]
		// A routine's task in review asks the owner for something; it shares
		// the routine's one row with the receipts.
		routineTask, hasTask := s.routineTask(job.ID)
		asks := hasTask && routineTask.Status == task.Review
		if len(snap.Pending) == 0 && len(issues) == 0 && !asks {
			continue
		}
		title := job.Title
		if title == "" {
			title = job.ID
		}
		row := Row{Task: task.Task{ID: job.ID, Title: title, Area: job.Area, Status: task.Review}, Routine: true,
			URL: schedule.RoutineURL(job.ID), RoutineIssues: issues}
		if asks {
			row.RoutineTaskNext = summary(routineTask.Next(), 110)
			if row.RoutineTaskNext == "" {
				row.RoutineTaskNext = "タスクの本文で、お願いしたいことを確かめてください。"
			}
		}
		if len(snap.Pending) > 0 {
			latest := snap.Pending[len(snap.Pending)-1]
			row.Next = fmt.Sprintf("未確認の結果が%d回分あります。", len(snap.Pending))
			if latest.ProblemKey == "" {
				row.Next += receiptBrief(latest)
			}
			row.RoutineReceiptURL = routine.ReceiptURL(job.ID, latest.Sequence)
			health := schedule.HealthOf(s.Store.Root(), job.ID)
			problem := health.LatestExecution
			var problemKey string
			if problem.HasProblem() {
				problemKey = problem.ProblemReport(health.LastSuccessAt).ProblemKey
			}
			for i := len(snap.Pending) - 1; i >= 0; i-- {
				receipt := snap.Pending[i]
				sameIssue := receipt.ProblemKey == problemKey && receipt.ProblemAt.After(health.LastSuccessAt)
				if problemKey != "" && (sameIssue || receipt.RunID == problem.OccurrenceID() || receipt.RunID == problem.Request) {
					row.RoutineIssues = append(row.RoutineIssues, problem.ProblemSummary())
					row.RoutineIssues = append(row.RoutineIssues, problem.ProblemReasons()...)
					break
				}
				if receipt.ProblemKey != "" {
					row.RoutineIssues = append(row.RoutineIssues, receipt.Summary)
					break
				}
			}
		}
		addReviewRow(sec, row)
	}
	return nil
}
func (s *Server) routine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.findRoutine(id)
	if !ok {
		r.SetPathValue("name", id)
		s.schedule(w, r)
		return
	}
	if r.PathValue("id") != job.Name {
		http.Redirect(w, r, "/routines/"+job.Name+routineQuery(r), 303)
		return
	}
	snap, err := s.Routines.Snapshot(job.Name)
	if err != nil {
		s.fail(w, 500, "定期タスクの結果を読めませんでした。")
		return
	}
	health := schedule.HealthOf(s.Store.Root(), job.Name)
	page := routinePage{routineRow: routineRow{Job: job, Snapshot: snap, Problem: health.LatestExecution, Next: job.Next(s.Now())}, ActionsEnabled: s.routineActions(), Flash: r.URL.Query().Get("flash"), Months: schedule.Months(s.Store.Root(), job.Name), Health: health}
	if !page.ActionsEnabled {
		page.Token, err = s.routineToken(job.Name, snap.LatestSequence, 0)
		if err != nil {
			s.fail(w, 500, "受付を確認済みにするための画面を用意できませんでした。")
			return
		}
	}
	definition := filepath.ToSlash(job.Path)
	page.PromptHTML = renderMarkdown(job.Prompt, path.Dir(definition))
	if definition != "" {
		page.DefinitionURL = "/files/" + escapePath(definition)
	}
	if t, ok := s.routineTask(job.Name); ok && (!page.ActionsEnabled || s.Store.KeepsTasks()) {
		lines := taskEvents(s.Store.Root(), t)
		page.RoutineTask = s.routineTaskPart(t, lines)
		page.HistoryURL = "/tasks/" + t.ID + "/log"
	}
	if definition != "" {
		dir := path.Dir(definition)
		if lines, err := task.ReadEvents(filepath.Join(s.Store.Root(), filepath.FromSlash(dir), task.HistoryFile)); err == nil && len(lines) > 0 {
			page.History = groupedHistory(lines, dir, s.Store.Location())
		}
	}
	page.Month = r.URL.Query().Get("month")
	if page.Month == "" && len(page.Months) > 0 {
		page.Month = page.Months[0]
	}
	receipts := make(map[string]uint64, len(snap.Receipts))
	for _, receipt := range snap.Receipts {
		receipts[receipt.RunID] = receipt.Sequence
	}
	monthRuns := schedule.ReadRuns(s.Store.Root(), job.Name, page.Month)
	if page.ActionsEnabled {
		page.Receipts = s.routineReceiptRows(job.Name, page.Months, snap.Receipts)
	} else {
		for _, reviewed := range []bool{false, true} {
			for i := len(snap.Receipts) - 1; i >= 0; i-- {
				if receipt := snap.Receipts[i]; receipt.Reviewed == reviewed {
					page.Receipts = append(page.Receipts, routineReceiptRow{Receipt: receipt})
				}
			}
		}
	}
	for _, run := range monthRuns {
		sequence := receipts[run.OccurrenceID()]
		if sequence == 0 {
			sequence = receipts[run.Request]
		}
		page.Runs = append(page.Runs, routineRunRow{Run: run, Receipt: sequence})
	}
	if s.ScheduleDispatch != nil {
		name := job.Name
		if job.LegacyName != "" {
			name = job.LegacyName
		}
		page.Executions = s.ScheduleDispatch.List(name)
	}
	s.render(w, 200, "routine", job.Title, page)
}

// routineReceiptRows links accepted reports to their original run even when a
// delayed report was filed in a different month. Missing legacy runs are left
// unspecified rather than guessing the outcome from the review state.
func (s *Server) routineReceiptRows(name string, months []string, receipts []routine.Receipt) []routineReceiptRow {
	rows := make([]routineReceiptRow, 0, len(receipts))
	byRun := make(map[string]int, len(receipts))
	for _, receipt := range receipts {
		byRun[receipt.RunID] = len(rows)
		rows = append(rows, routineReceiptRow{Receipt: receipt, OutcomeLabel: receiptOutcomeLabel(receipt, schedule.Run{})})
	}
	for _, month := range months {
		if len(byRun) == 0 {
			break
		}
		for _, run := range schedule.ReadRuns(s.Store.Root(), name, month) {
			for _, id := range []string{run.OccurrenceID(), run.Request} {
				index, found := byRun[id]
				if id == "" || !found {
					continue
				}
				row := &rows[index]
				row.RunURL = schedule.RoutineURL(name) + "?month=" + month + "#" + run.Anchor()
				row.RunAt = run.At
				row.OutcomeLabel = receiptOutcomeLabel(row.Receipt, run)
				if row.Summary == "" {
					row.Summary = run.Summary
				}
				delete(byRun, id)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].AcceptedAt.Equal(rows[j].AcceptedAt) {
			return rows[i].Sequence > rows[j].Sequence
		}
		return rows[i].AcceptedAt.After(rows[j].AcceptedAt)
	})
	return rows
}

// setRoutineStatus moves the routine's task between doing, waiting and
// review from its page. Completing receipts does not touch it.
func (s *Server) setRoutineStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if job, ok := s.findRoutine(id); !ok || id != job.Name {
		s.fail(w, http.StatusNotFound, "その定期タスクはありません。")
		return
	}
	t, changed, err := s.Store.SetStatus(id, task.Status(r.FormValue("status")), "Web", r.FormValue("note"))
	switch {
	case errors.Is(err, task.ErrNotFound):
		s.fail(w, http.StatusNotFound, "この定期タスクには、状態を変えるための task.md がまだありません。")
		return
	case err != nil:
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if changed && s.Changed != nil {
		s.Changed()
	}
	flash := "タスクを「" + routineStatusLabel(t.Status) + "」にしました。"
	http.Redirect(w, r, schedule.RoutineURL(id)+"?flash="+url.QueryEscape(flash)+"#task", http.StatusSeeOther)
}

// routineFile shows a file in the routine's directory: the task's
// artifacts, notes and the like, linked from the task on the routine's page.
func (s *Server) routineFile(w http.ResponseWriter, r *http.Request) {
	job, ok := s.findRoutine(r.PathValue("id"))
	if !ok || job.Path == "" {
		s.fail(w, http.StatusNotFound, "その定期タスクはありません。")
		return
	}
	if r.PathValue("id") != job.Name {
		http.Redirect(w, r, schedule.RoutineURL(job.Name)+"/files/"+escapePath(r.PathValue("path"))+routineQuery(r), http.StatusFound)
		return
	}
	dir := path.Dir(filepath.ToSlash(job.Path))
	file := path.Join(dir, r.PathValue("path"))
	if !strings.HasPrefix(file, dir+"/") {
		s.fail(w, http.StatusNotFound, "そのファイルは表示できません。")
		return
	}
	var owner *task.Task
	if t, ok := s.routineTask(job.Name); ok {
		owner = &t
	}
	s.serveFile(w, r, file, owner)
}

type routineReceiptPage struct {
	schedule.Job
	// RoutineTaskStatus names the status of the routine's task, which the
	// page links back to; empty when the routine has no task.
	RoutineTaskStatus string
	ActionsEnabled    bool
	Receipt           routine.Receipt
	OutcomeLabel      string
	History           []routine.Event
	RunURL            string
	RunAt             time.Time
	Problem           schedule.Run
	Token             string
	Flash             string
}

func receiptSequence(r *http.Request) (uint64, bool) {
	value := r.PathValue("sequence")
	sequence, err := strconv.ParseUint(value, 10, 64)
	return sequence, err == nil && sequence > 0 && strconv.FormatUint(sequence, 10) == value
}

func (s *Server) routineReceipt(w http.ResponseWriter, r *http.Request) {
	job, found := s.findRoutine(r.PathValue("id"))
	sequence, valid := receiptSequence(r)
	if !found || !valid {
		s.fail(w, 404, "その受付はありません。")
		return
	}
	snap, err := s.Routines.Snapshot(job.Name)
	if err != nil {
		s.fail(w, 500, "定期タスクの結果を読めませんでした。")
		return
	}
	index := slices.IndexFunc(snap.Receipts, func(item routine.Receipt) bool { return item.Sequence == sequence })
	if index < 0 {
		s.fail(w, 404, "その受付はありません。")
		return
	}
	if r.PathValue("id") != job.Name {
		http.Redirect(w, r, routine.ReceiptURL(job.Name, sequence)+routineQuery(r), http.StatusPermanentRedirect)
		return
	}

	page := routineReceiptPage{Job: job, ActionsEnabled: s.routineActions(), Receipt: snap.Receipts[index], Flash: r.URL.Query().Get("flash")}
	if t, ok := s.routineTask(job.Name); ok && (!page.ActionsEnabled || s.Store.KeepsTasks()) {
		page.RoutineTaskStatus = routineStatusLabel(t.Status)
	}
	for _, event := range snap.History {
		if c := event.Completion; c != nil && (sequence <= c.Through || slices.Contains(c.Sequences, sequence)) {
			page.History = append(page.History, event)
		}
	}
	if !page.ActionsEnabled && !page.Receipt.Reviewed {
		page.Token, err = s.routineToken(job.Name, 0, sequence)
		if err != nil {
			s.fail(w, 500, "受付を確認済みにするための画面を用意できませんでした。")
			return
		}
	}
	// A delayed run may have been recorded in an earlier month than its receipt.
	var execution schedule.Run
	for _, month := range schedule.Months(s.Store.Root(), job.Name) {
		for _, run := range schedule.ReadRuns(s.Store.Root(), job.Name, month) {
			if run.OccurrenceID() == page.Receipt.RunID || run.Request == page.Receipt.RunID {
				page.RunURL = schedule.RoutineURL(job.Name) + "?month=" + month + "#" + run.Anchor()
				execution = run
				page.RunAt = run.At
				if page.Receipt.ProblemKey == "" {
					page.Problem = run
				}
				break
			}
		}
		if page.RunURL != "" {
			break
		}
	}
	page.OutcomeLabel = receiptOutcomeLabel(page.Receipt, execution)
	if page.ActionsEnabled && page.Receipt.Summary == "" {
		page.Receipt.Summary = execution.Summary
	}
	s.render(w, 200, "routine-receipt", fmt.Sprintf("%s 受付 %d", job.Title, sequence), page)
}

func (s *Server) completeRoutine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.findRoutine(id)
	actionsEnabled := s.routineActions()
	if !ok || (!actionsEnabled && id != job.Name) {
		s.fail(w, 404, "その定期タスクはありません。")
		return
	}
	if actionsEnabled {
		target, fragment := schedule.RoutineURL(job.Name), "#receipts"
		if r.PathValue("sequence") != "" {
			sequence, valid := receiptSequence(r)
			if !valid {
				s.fail(w, http.StatusNotFound, "その受付はありません。")
				return
			}
			target, fragment = routine.ReceiptURL(job.Name, sequence), ""
		}
		flash := "この定期タスクでは、受付を確認済みにする操作は要りません。実行結果は受付で読めます。"
		http.Redirect(w, r, target+"?flash="+url.QueryEscape(flash)+fragment, http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, 400, "確認する受付を読み取れませんでした。ページを開き直してください。")
		return
	}
	view, err := s.readRoutineToken(r.FormValue("view"), id)
	if err != nil {
		s.fail(w, 409, err.Error())
		return
	}
	completion := routine.Completion{OperationID: view.OperationID, Actor: task.OwnerName, Through: view.Through}
	if r.PathValue("sequence") != "" {
		sequence, valid := receiptSequence(r)
		if !valid || view.Sequence != sequence {
			s.fail(w, 409, "受付の画面を開き直して、「この受付を確認済みにする」を押してください。")
			return
		}
		completion.Sequences = []uint64{sequence}
	} else if view.Sequence != 0 {
		s.fail(w, 409, "定期タスクの画面を開き直して、「未確認分をまとめて確認する」を押してください。")
		return
	}
	snap, err := s.Routines.Complete(id, completion)
	if err != nil {
		s.fail(w, 500, "確認の履歴を保存できませんでした。")
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	if view.Sequence != 0 {
		http.Redirect(w, r, routine.ReceiptURL(id, view.Sequence)+"?flash="+url.QueryEscape("この受付を確認済みにしました。"), 303)
		return
	}
	flash := "画面を開いた時点で未確認だった受付を、まとめて確認済みにしました。定期タスクの実行予定は変わりません。"
	if len(snap.Pending) > 0 {
		flash += "未確認の結果が" + strconv.Itoa(len(snap.Pending)) + "回分残っています。"
	}
	http.Redirect(w, r, schedule.RoutineURL(id)+"?flash="+url.QueryEscape(flash), 303)
}

type routineRecent struct {
	ID, Title string
	Through   uint64
	Single    bool
	At        time.Time
}

func (s *Server) recentRoutineReviews() []routineRecent {
	var rows []routineRecent
	for _, j := range s.routineJobs() {
		if j.ID == "" {
			continue
		}
		snap, err := s.Routines.Snapshot(j.Name)
		if err != nil {
			continue
		}
		for _, event := range snap.History {
			if c := event.Completion; c != nil {
				if c.Through > 0 {
					rows = append(rows, routineRecent{ID: j.Name, Title: j.Title, Through: c.Through, At: event.At})
				}
				for _, sequence := range c.Sequences {
					rows = append(rows, routineRecent{ID: j.Name, Title: j.Title, Through: sequence, Single: true, At: event.At})
				}
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	if len(rows) > 8 {
		rows = rows[:8]
	}
	return rows
}

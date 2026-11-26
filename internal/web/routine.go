package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
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
		return view, fmt.Errorf("画面を開き直してから、もう一度完了にしてください。")
	}
	raw, e := base64.RawURLEncoding.DecodeString(a)
	sig, e2 := base64.RawURLEncoding.DecodeString(b)
	mac := hmac.New(sha256.New, s.routineKey)
	mac.Write(raw)
	if e != nil || e2 != nil || !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(raw, &view) != nil || view.ID != id || view.OperationID == "" {
		return routineView{}, fmt.Errorf("画面を開き直してから、もう一度完了にしてください。")
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

type routineRow struct {
	schedule.Job
	Snapshot routine.Snapshot
	Next     time.Time
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

type routinePage struct {
	routineRow
	Token         string
	Flash         string
	Months        []string
	Month         string
	Runs          []routineRunRow
	Receipts      []routine.Receipt
	Health        schedule.Health
	Executions    []schedule.Execution
	PromptHTML    template.HTML
	DefinitionURL string
}

func (s *Server) routines(w http.ResponseWriter, r *http.Request) {
	var rows []routineRow
	for _, job := range s.routineJobs() {
		if job.ID == "" {
			rows = append(rows, routineRow{Job: job, Next: job.Next(s.Now())})
			continue
		}
		snap, err := s.Routines.Snapshot(job.Name)
		if err != nil {
			s.fail(w, 500, "定期タスクの結果を読めませんでした。")
			return
		}
		rows = append(rows, routineRow{Job: job, Snapshot: snap, Next: job.Next(s.Now())})
	}
	s.render(w, 200, "routines", "定期タスク", rows)
}
func (s *Server) addRoutineReview(sec *Section) error {
	for _, job := range s.routineJobs() {
		if job.ID == "" {
			continue
		}
		snap, err := s.Routines.Snapshot(job.Name)
		if err != nil {
			return err
		}
		if len(snap.Pending) == 0 {
			continue
		}
		latest := snap.Pending[len(snap.Pending)-1]
		row := Row{Task: task.Task{ID: job.Name, Title: job.Title, Area: job.Area, Status: task.Review}, Routine: true,
			URL: schedule.RoutineURL(job.Name), Next: fmt.Sprintf("未確認の結果が%d回分あります。%s", len(snap.Pending), receiptBrief(latest)),
			RoutineReceiptURL: routine.ReceiptURL(job.Name, latest.Sequence)}
		found := false
		for i := range sec.Blocks {
			if sec.Blocks[i].Area == job.Area {
				sec.Blocks[i].Rows = append(sec.Blocks[i].Rows, row)
				found = true
				break
			}
		}
		if !found {
			sec.Blocks = append(sec.Blocks, Block{Area: job.Area, Rows: []Row{row}})
		}
		sec.Count++
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
	token, err := s.routineToken(job.Name, snap.LatestSequence, 0)
	if err != nil {
		s.fail(w, 500, "完了操作を準備できませんでした。")
		return
	}
	page := routinePage{routineRow: routineRow{Job: job, Snapshot: snap, Next: job.Next(s.Now())}, Token: token, Flash: r.URL.Query().Get("flash"), Months: schedule.Months(s.Store.Root(), job.Name), Health: schedule.HealthOf(s.Store.Root(), job.Name)}
	definition := filepath.ToSlash(job.Path)
	page.PromptHTML = renderMarkdown(job.Prompt, path.Dir(definition))
	if definition != "" {
		page.DefinitionURL = "/files/" + escapePath(definition)
	}
	page.Month = r.URL.Query().Get("month")
	if page.Month == "" && len(page.Months) > 0 {
		page.Month = page.Months[0]
	}
	receipts := make(map[string]uint64, len(snap.Receipts))
	for _, receipt := range snap.Receipts {
		receipts[receipt.RunID] = receipt.Sequence
	}
	for _, reviewed := range []bool{false, true} {
		for i := len(snap.Receipts) - 1; i >= 0; i-- {
			if receipt := snap.Receipts[i]; receipt.Reviewed == reviewed {
				page.Receipts = append(page.Receipts, receipt)
			}
		}
	}
	for _, run := range schedule.ReadRuns(s.Store.Root(), job.Name, page.Month) {
		sequence := receipts[run.ID]
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

type routineReceiptPage struct {
	schedule.Job
	Receipt routine.Receipt
	History []routine.Event
	RunURL  string
	Token   string
	Flash   string
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

	page := routineReceiptPage{Job: job, Receipt: snap.Receipts[index], Flash: r.URL.Query().Get("flash")}
	for _, event := range snap.History {
		if c := event.Completion; c != nil && (sequence <= c.Through || slices.Contains(c.Sequences, sequence)) {
			page.History = append(page.History, event)
		}
	}
	if !page.Receipt.Reviewed {
		page.Token, err = s.routineToken(job.Name, 0, sequence)
		if err != nil {
			s.fail(w, 500, "完了操作を準備できませんでした。")
			return
		}
	}
	// A delayed run may have been recorded in an earlier month than its receipt.
	for _, month := range schedule.Months(s.Store.Root(), job.Name) {
		for _, run := range schedule.ReadRuns(s.Store.Root(), job.Name, month) {
			if run.ID == page.Receipt.RunID || run.Request == page.Receipt.RunID {
				page.RunURL = schedule.RoutineURL(job.Name) + "?month=" + month + "#" + run.Anchor()
				break
			}
		}
		if page.RunURL != "" {
			break
		}
	}
	s.render(w, 200, "routine-receipt", fmt.Sprintf("%s 受付 %d", job.Title, sequence), page)
}

func (s *Server) completeRoutine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if job, ok := s.findRoutine(id); !ok || id != job.Name {
		s.fail(w, 404, "その定期タスクはありません。")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, 400, "完了操作を読めませんでした。")
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
			s.fail(w, 409, "受付の画面を開き直してから、もう一度確認してください。")
			return
		}
		completion.Sequences = []uint64{sequence}
	} else if view.Sequence != 0 {
		s.fail(w, 409, "定期タスクの画面を開き直してから、もう一度完了にしてください。")
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
	flash := "表示した今回分を完了にしました。次の実行は続きます。"
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

package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/imos/lacia/internal/notify"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func validReportReference(value string) bool {
	if value == "" {
		return true
	}
	ref, err := url.Parse(value)
	if err != nil || ref.User != nil {
		return false
	}
	switch ref.Scheme {
	case "https", "http":
		return ref.Host != ""
	case "":
		return ref.Host == "" && strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.Contains(value, "\\")
	default:
		return false
	}
}

func validRunReport(job schedule.Job, report routine.Result) bool {
	if err := job.NormalizeCoverage(&report); err != nil {
		return false
	}
	if len(report.Matters)+len(report.Handoffs) > 500 || len(report.Unverified) > 500 || len(report.Resolved) > 500 || len(report.Summary) > 8192 ||
		!validReportReference(report.Reference) || report.Nothing && len(report.Matters)+len(report.Handoffs) > 0 {
		return false
	}
	if !report.Nothing && len(report.Matters)+len(report.Handoffs)+len(report.Resolved) == 0 && strings.TrimSpace(report.Summary) == "" && report.Reference == "" && report.CheckStatus == "" {
		return false
	}
	resolved := map[string]bool{}
	for _, key := range report.Resolved {
		if strings.TrimSpace(key) == "" || len(key) > 1024 || resolved[key] {
			return false
		}
		resolved[key] = true
	}
	for _, matter := range report.Matters {
		if !validMatter(matter, job.ExplicitMatters) {
			return false
		}
	}
	seen := map[[2]string]bool{}
	for _, handoff := range report.Handoffs {
		key := [2]string{handoff.Key, handoff.Revision}
		if seen[key] || !validMatter(handoff.Matter, true) || !task.ValidID(handoff.TaskID) {
			return false
		}
		seen[key] = true
	}
	return true
}

func validMatter(m routine.Matter, requireSummary bool) bool {
	return strings.TrimSpace(m.Key) != "" && strings.TrimSpace(m.Revision) != "" && len(m.Key) <= 1024 && len(m.Revision) <= 1024 &&
		len(m.Summary) <= 4096 && (!requireSummary || strings.TrimSpace(m.Summary) != "") && validReportReference(m.Reference)
}

func (s *Server) reportRoutineRun(w http.ResponseWriter, r *http.Request) {
	if s.ScheduleDispatch == nil {
		http.NotFound(w, r)
		return
	}
	id, runID := r.PathValue("id"), r.PathValue("run")
	execution, _ := s.ScheduleDispatch.Get(runID)
	var report routine.Result
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil || decoder.Decode(new(any)) != io.EOF ||
		(report.RunID != "" && report.RunID != runID) || !validRunReport(execution.Job, report) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "定期タスクの結果を読めませんでした"})
		return
	}
	if err := s.ScheduleDispatch.Report(id, runID, report); err != nil {
		if errors.Is(err, schedule.ErrRoutineReport) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "結果を保存できませんでした"})
		}
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (s *Server) routineNotices(w http.ResponseWriter, request *http.Request) {
	if s.Routines == nil || s.Schedules == nil {
		http.NotFound(w, request)
		return
	}
	if s.routineActions() {
		// An older agent would read a task notice as a receipt and give it
		// a different notification ID. Wait for the explicit opt-in.
		if request.URL.Query().Get("action") != "1" {
			writeJSON(w, http.StatusOK, []notify.RoutineNotice{})
			return
		}
		writeJSON(w, http.StatusOK, s.routineTaskNotices())
		return
	}
	out := []notify.RoutineNotice{}
	jobs, _ := s.Schedules()
	for _, job := range jobs {
		if job.ID == "" {
			continue
		}
		state, err := s.Routines.Snapshot(job.ID)
		if err != nil {
			http.Error(w, "定期タスクの通知を確かめられませんでした", http.StatusServiceUnavailable)
			return
		}

		for _, r := range state.Receipts {
			// The run's assigned reply route is authoritative even after the
			// definition changes. Do not also deliver its result through the Mac.
			routed := job.ReplyTo != ""
			if s.Requests != nil {
				if run, ok := s.Requests.Get(r.RunID); ok {
					routed = run.ReplyTo != ""
				}
			}
			if routed {
				continue
			}
			if !r.Notified {
				out = append(out, notify.RoutineNotice{RoutineID: job.ID, Sequence: r.Sequence,
					Title: job.Title, Count: max(1, len(r.Matters)), ReceiptPage: true})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) routineTaskNotices() []notify.RoutineNotice {
	out := []notify.RoutineNotice{}
	if !s.Store.KeepsTasks() {
		return out
	}
	for _, job := range s.routineJobs() {
		if !task.ValidRoutineID(job.ID) || job.ReplyTo != "" {
			continue
		}
		t, ok := s.routineTask(job.ID)
		if !ok {
			continue
		}
		sequence, summary, count := t.RoutineActionNotice()
		if sequence > 0 && count > 0 {
			out = append(out, notify.RoutineNotice{
				RoutineID: t.ID, Sequence: sequence, Title: t.Title, Summary: summary, Count: count, Action: true,
			})
		}
	}
	return out
}

func (s *Server) routineNoticeAcknowledged(w http.ResponseWriter, r *http.Request) {
	if s.Routines == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	sequence, valid := receiptSequence(r)
	if !valid {
		http.Error(w, "通知番号を読めませんでした", http.StatusBadRequest)
		return
	}
	action := r.URL.Query().Get("action") == "1"
	actionsEnabled := s.routineActions()
	var err error
	switch {
	case action && actionsEnabled && s.Store.KeepsTasks():
		err = s.Store.MarkRoutineActionNotified(id, sequence)
	case action:
		http.NotFound(w, r)
		return
	case actionsEnabled:
		// An older client may still be acknowledging a receipt it read before
		// switching modes. Its number is unrelated to the task notice sequence.
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		err = s.Routines.MarkNotified(id, sequence)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("定期タスク %s の通知を記録できませんでした", id), http.StatusConflict)
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	w.WriteHeader(http.StatusNoContent)
}

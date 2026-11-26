package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	if len(report.Matters)+len(report.Handoffs) > 500 || len(report.Unverified) > 500 || len(report.Summary) > 8192 ||
		!validReportReference(report.Reference) || report.Nothing && len(report.Matters)+len(report.Handoffs) > 0 {
		return false
	}
	if !report.Nothing && len(report.Matters)+len(report.Handoffs) == 0 && strings.TrimSpace(report.Summary) == "" && report.Reference == "" && report.CheckStatus == "" {
		return false
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

func (s *Server) routineNoticeAcknowledged(w http.ResponseWriter, r *http.Request) {
	if s.Routines == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	sequence, err := strconv.ParseUint(r.PathValue("sequence"), 10, 64)
	if err != nil || sequence == 0 {
		http.Error(w, "受付番号を読めませんでした", http.StatusBadRequest)
		return
	}
	if err := s.Routines.MarkNotified(id, sequence); err != nil {
		http.Error(w, fmt.Sprintf("定期タスク %s の通知を記録できませんでした", id), http.StatusConflict)
		return
	}
	if s.Changed != nil {
		s.Changed()
	}
	w.WriteHeader(http.StatusNoContent)
}

package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/imos/lacia/internal/schedule"
)

func (s *Server) scheduleWorker(w http.ResponseWriter, r *http.Request) {
	d := s.ScheduleDispatch
	device := r.PathValue("device")
	secret, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if d == nil || !bearer || !d.Routing.Authorized(device, secret) {
		http.Error(w, "端末の認証が必要です", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decode := func(v any) bool {
		if json.NewDecoder(r.Body).Decode(v) != nil {
			http.Error(w, "端末から届いた内容を読み取れませんでした。JSONの形式を確かめてください", http.StatusBadRequest)
			return false
		}
		return true
	}
	var err error
	var out any = struct{}{}
	switch r.PathValue("action") {
	case "poll":
		var status schedule.DeviceStatus
		if !decode(&status) {
			return
		}
		out, err = d.Poll(device, status)
	case "start":
		var start schedule.StartRequest
		if !decode(&start) {
			return
		}
		err = d.Start(device, r.PathValue("run"), start.Claim, s.Now())
	case "finish":
		var result schedule.FinishRequest
		if !decode(&result) {
			return
		}
		err = d.Finish(device, r.PathValue("run"), result.Claim, result.Result, result.Unknown)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		if errors.Is(err, schedule.ErrDispatchPaused) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "予約の実行状態を更新できませんでした", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) scheduleExecutions(w http.ResponseWriter, r *http.Request) {
	if s.ScheduleDispatch == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Executions []schedule.Execution   `json:"executions"`
		Events     []schedule.DeviceEvent `json:"device_events"`
	}{s.ScheduleDispatch.List(r.URL.Query().Get("name")), s.ScheduleDispatch.Events("")})
}

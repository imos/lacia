package web

import (
	"net/http"
	"os"
	"time"

	"github.com/imos/lacia/internal/calendar"
)

// apiCalendarScope lists the calendars this server shows. Notification
// clients use the same list for meeting notices.
func (s *Server) apiCalendarScope(w http.ResponseWriter, _ *http.Request) {
	scope := calendar.Scope
	if scope == nil {
		scope = []calendar.ScopeEntry{}
	}
	writeJSON(w, http.StatusOK, scope)
}

func (s *Server) apiCalendarList(w http.ResponseWriter, r *http.Request) {
	if s.calendar == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "このレイシアには予定のデータがありません"})
		return
	}
	q := r.URL.Query()
	result, err := s.calendar.List(time.Now(), q.Get("period"), q.Get("from"), q.Get("until"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

// apiSnapshot returns the stored calendar snapshot without display filters.
func (s *Server) apiSnapshot(w http.ResponseWriter, _ *http.Request) {
	if s.CalendarPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "予定のデータがありません"})
		return
	}
	data, err := os.ReadFile(s.CalendarPath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "予定のデータを読めませんでした"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(data)
}

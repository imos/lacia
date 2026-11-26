package web

import (
	"net/http"
	"time"

	"github.com/imos/lacia/internal/request"
)

// Busy is what a restart of this server would cut into: the requests the
// agent has not finished, and the conversations that ran a command a moment
// ago and may be about to run the next.
type Busy struct {
	Requests []BusyRequest `json:"requests"`
	Sessions []SessionRow  `json:"sessions"`
}

// BusyRequest is a request that is waiting or being worked on.
type BusyRequest struct {
	ID      string    `json:"id"`
	TaskID  string    `json:"task,omitempty"`
	Source  string    `json:"source,omitempty"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
}

// busy lists what is going on now. within is how long ago a conversation
// may have been heard to still count.
func (s *Server) busy(within time.Duration) Busy {
	out := Busy{Requests: []BusyRequest{}, Sessions: []SessionRow{}}
	if s.Requests != nil {
		for _, r := range s.Requests.Recent(0, "", true) {
			if r.Status == request.Queued || r.Status == request.Running {
				out.Requests = append(out.Requests, BusyRequest{ID: r.ID, TaskID: r.TaskID, Source: r.Source, Status: r.Status, Created: r.Created})
			}
		}
	}
	now := s.Now()
	for _, row := range s.sessionRows() {
		if now.Sub(row.Last) < within {
			out.Sessions = append(out.Sessions, row)
		}
	}
	return out
}

// apiBusy answers `lacia busy`. Asking does not count as a sign of life:
// the one who asks is about to restart the server, not to work.
func (s *Server) apiBusy(w http.ResponseWriter, r *http.Request) {
	within, err := time.ParseDuration(r.URL.Query().Get("within"))
	if err != nil || within <= 0 {
		within = 5 * time.Minute
	}
	writeJSON(w, http.StatusOK, s.busy(within))
}

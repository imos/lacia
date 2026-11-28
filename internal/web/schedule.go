package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/imos/lacia/internal/schedule"
)

// redirectLegacySchedule preserves saved links and history month filters.
func redirectLegacySchedule(w http.ResponseWriter, r *http.Request) {
	target := "/routines" + strings.TrimPrefix(r.URL.EscapedPath(), "/schedules")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

func (s *Server) redirectScheduleDetail(w http.ResponseWriter, r *http.Request) {
	if job, ok := s.findRoutine(r.PathValue("name")); ok {
		http.Redirect(w, r, "/routines/"+url.PathEscape(job.Name)+routineQuery(r), http.StatusPermanentRedirect)
		return
	}
	redirectLegacySchedule(w, r)
}

// schedulePage is a definition left in schedules/, which does not run: the
// page says why.
type schedulePage struct {
	schedule.Job
	Executions []schedule.Execution
}

func (s *Server) schedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var page *schedulePage
	for _, j := range s.routineJobs() {
		if j.Name == name {
			page = &schedulePage{Job: j}
			break
		}
	}
	if page == nil {
		s.fail(w, http.StatusNotFound, "その名前の定期タスクは見つかりませんでした。")
		return
	}
	if s.ScheduleDispatch != nil {
		page.Executions = s.ScheduleDispatch.List(name)
	}
	title := page.Title
	if title == "" {
		title = page.Name
	}
	s.render(w, http.StatusOK, "schedule", title, page)
}

// troubles lists the scheduled jobs and devices that need a look.
func (s *Server) troubles() []schedule.Trouble {
	if s.Schedules == nil {
		return nil
	}
	jobs, _ := s.Schedules()
	var devices map[string]schedule.DeviceStatus
	if s.ScheduleDispatch != nil {
		devices = s.ScheduleDispatch.Devices()
	}
	return schedule.Troubles(s.Store.Root(), jobs, devices)
}

// apiScheduleTroubles reports operational alerts to older notification clients.
func (s *Server) apiScheduleTroubles(w http.ResponseWriter, _ *http.Request) {
	if s.routineActions() {
		writeJSON(w, http.StatusOK, []schedule.Trouble{})
		return
	}
	troubles := s.troubles()
	if troubles == nil {
		troubles = []schedule.Trouble{}
	}
	for i := range troubles {
		troubles[i].Path = "/routines"
		if name := troubles[i].Name; name != "" {
			troubles[i].Path += "/" + url.PathEscape(name)
		}
	}
	writeJSON(w, http.StatusOK, troubles)
}

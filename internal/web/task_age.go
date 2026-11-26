package web

import (
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

func (s *Server) setStatusAge(row *Row, list taskListContext) {
	t := row.Task
	if s.Store.Format() < 1 || (row.Waits.Running() && (t.Status != row.ListStatus || t.Snoozed(list.now))) {
		return
	}
	lines := taskEvents(s.Store.Root(), t)
	since := scheduledStatusTime(lines, t, row.ListStatus, list.now)
	if row.Queued || row.ListStatus == task.Doing {
		if requestTime, applies := s.requestStatusTime(*row, lines, list.now); applies {
			if !requestTime.IsZero() && since.After(requestTime) {
				requestTime = since
			}
			since = requestTime
		}
	}
	if !since.IsZero() && !since.After(list.now) {
		row.StatusSince = since
		row.StatusAge = taskStatusAge(list.now.Sub(since))
	}
}

// requestStatusTime uses the request that moved the task to doing, if it
// can still be identified. Requests added to an existing doing task do not
// restart its age.
func (s *Server) requestStatusTime(row Row, lines []task.Line, now time.Time) (time.Time, bool) {
	if s.Requests == nil || row.Snoozed(now) || row.Waits.Running() {
		return time.Time{}, row.Queued
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if !lines[i].OK {
			continue
		}
		e := lines[i].Event
		entered := eventStatus(e)
		if entered == "" {
			continue
		}
		if entered == task.Doing && (e.By == "依頼の受付" || e.By == "依頼の再開") {
			return s.initialRequestTime(row, lines[i+1:]), true
		}
		break
	}
	return time.Time{}, row.Queued
}

func (s *Server) initialRequestTime(row Row, lines []task.Line) time.Time {
	var req request.Request
	var claimed time.Time
	for _, line := range lines {
		if !line.OK {
			continue
		}
		e := line.Event
		if req.ID == "" && e.Type == task.EventRequest && (e.Via == "依頼の受付" || e.Via == "再開") {
			var ok bool
			req, ok = s.Requests.Get(e.Request)
			if !ok || req.TaskID != row.ID || req.StatusPolicy == request.KeepStatus {
				return time.Time{}
			}
		}
		if claimed.IsZero() && e.Type == task.EventClaimed && e.Previous == "" {
			claimed, _ = time.Parse(time.RFC3339, e.At)
		}
	}
	if row.Queued {
		if req.Status == request.Queued {
			return req.Created
		}
		return time.Time{}
	}
	if req.Started.IsZero() || (!claimed.IsZero() && claimed.Before(req.Started)) {
		return claimed
	}
	return req.Started
}

// scheduledStatusTime accounts for virtual status changes that can be dated
// from the task's schedule. Overlapping conditions keep the task in the same
// list until the last condition clears.
func scheduledStatusTime(lines []task.Line, current task.Task, status task.Status, now time.Time) time.Time {
	state := task.Task{Timezone: current.Timezone}
	var displayed task.Status
	var since time.Time
	update := func(at time.Time) {
		if !state.Status.Valid() {
			return
		}
		if next := state.ListStatus(at, false, false); next != displayed {
			displayed, since = next, at
		}
	}
	expireResume := func(until time.Time) {
		if state.ResumeOn == "" {
			return
		}
		if when, err := task.ParseWhenIn(state.ResumeOn, now.Location()); err == nil && !when.After(until) {
			update(when)
		}
	}
	for _, line := range lines {
		if !line.OK {
			continue
		}
		e := line.Event
		entered := eventStatus(e)
		isCondition := e.Type == task.EventField && e.Value != nil && (e.Key == "resume_on" || e.Key == "after" || e.Key == "after_run")
		if entered == "" && !isCondition && e.Type != task.EventWoke {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			return time.Time{}
		}
		expireResume(at)
		if entered != "" {
			state.Status = entered
		}
		if isCondition {
			switch e.Key {
			case "resume_on":
				state.ResumeOn = *e.Value
			case "after":
				state.Blockers = task.ParseIDs(*e.Value)
			case "after_run":
				state.AfterRun = *e.Value
			}
		}
		if e.Type == task.EventWoke && e.Action == task.WakeClearedConditions {
			state.ResumeOn, state.Blockers, state.AfterRun = "", nil, ""
		}
		update(at)
	}
	expireResume(now)
	if state.Status == current.Status && displayed == status && state.ResumeOn == current.ResumeOn &&
		(len(state.Blockers) > 0) == (len(current.Blockers) > 0) && state.AfterRun == current.AfterRun {
		return since
	}
	// Older wake entries did not identify which fields they removed. The
	// stored status is still dated by its own transition when shown as-is.
	if status == current.Status {
		return recordedStatusTime(lines, status)
	}
	return time.Time{}
}

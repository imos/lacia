package notify

import (
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/imos/lacia/internal/task"
)

// NeedsAction reports whether the task is waiting on the user right now:
// for a decision, an answer or an action, or for a look at finished work.
func NeedsAction(t task.Task, now time.Time) bool {
	return t.Status.AwaitsUser() && !t.Snoozed(now)
}

// PlanTasks compares an authoritative task list with the remembered one and
// returns a notice for every task that entered "needs action". The first call
// only records the baseline. state.Outbox ends up holding the notices that
// are still valid, including the new ones.
func PlanTasks(tasks []task.Task, state *State, now time.Time, siteURL string) []Notice {
	at := seconds(now)
	previous := state.Tasks
	current := map[string]TaskEntry{}
	notices := []Notice{}
	for _, t := range tasks {
		if t.ID == "" {
			continue
		}
		action := NeedsAction(t, now)
		closing := action && t.Status == task.Closing
		old, seen := previous[t.ID]
		generation := old.Generation
		// The server stopped a loop of writes to the task: that is news even
		// when the task was waiting on the user already.
		stopped := action && seen && t.StoppedAt != "" && t.StoppedAt != old.Stopped
		// A finished task that went back to review has something new to
		// decide. The other direction only renames what was asked already.
		reopened := action && seen && old.Action && old.Closing && !closing
		if action && seen && (!old.Action || stopped || reopened) {
			generation++
		}
		// A task that appears after the baseline may enter action directly.
		entered := action && previous != nil && (!old.Action || stopped || reopened)
		current[t.ID] = TaskEntry{Action: action, Generation: generation, Stopped: t.StoppedAt, Closing: closing}
		if !entered {
			continue
		}
		title := t.Title
		if title == "" {
			title = "タスク"
		}
		heading := "確認をお願いします"
		if closing {
			heading = "作業が終わりました"
		}
		if t.StoppedAt != "" && t.StoppedAt != old.Stopped {
			heading = "繰り返しの書き込みを止めました"
		}
		notices = append(notices, Notice{
			ID:         "task-" + t.ID + "-" + strconv.Itoa(generation),
			Kind:       KindTask,
			TaskKey:    t.ID,
			Generation: generation,
			Title:      heading,
			Body:       t.ID + " " + title,
			URL:        strings.TrimRight(siteURL, "/") + "/tasks/" + url.PathEscape(t.ID),
			Fire:       at,
			Expires:    at + 86400,
		})
	}
	// Absent tasks stay remembered, so one that reappears is not a new transition.
	merged := map[string]TaskEntry{}
	maps.Copy(merged, previous)
	maps.Copy(merged, current)
	state.Tasks = merged
	state.Outbox = append(keep(state.Outbox, func(n Notice) bool {
		if strings.HasPrefix(n.TaskKey, alertPrefix) || strings.HasPrefix(n.TaskKey, routinePrefix) {
			return true // PlanAlerts decides about these
		}
		entry, ok := current[n.TaskKey]
		return ok && entry.Action && entry.Generation == n.Generation
	}), notices...)
	return notices
}

// Alert is something other than a task that needs the user, such as a
// scheduled job that keeps failing. The server lists the ones that hold
// now; Key stays the same while one lasts.
type Alert struct {
	Key   string `json:"key"`
	Name  string `json:"name,omitempty"` // the scheduled job, if it is about one
	Title string `json:"title"`
	Text  string `json:"text"`
	Path  string `json:"path,omitempty"`
}

// alertPrefix keeps the keys of alerts apart from task IDs in State.Tasks.
const alertPrefix = "alert:"

// PlanAlerts tells about every alert that is new since the last look, once.
// It runs after PlanTasks and shares its memory and outbox: an alert is
// remembered like a task that needs action while it is listed. One that
// went away and comes back is told about again.
func PlanAlerts(alerts []Alert, state *State, now time.Time, siteURL string) []Notice {
	at := seconds(now)
	listed := map[string]bool{}
	notices := []Notice{}
	for _, a := range alerts {
		if a.Key == "" {
			continue
		}
		key := alertPrefix + a.Key
		listed[key] = true
		old := state.Tasks[key]
		if old.Action {
			continue
		}
		entry := TaskEntry{Action: true, Generation: old.Generation + 1}
		state.Tasks[key] = entry
		suffix := ""
		if a.Name != "" {
			suffix = "/" + url.PathEscape(a.Name)
		}
		page := "/schedules" + suffix
		if a.Path == "/routines"+suffix {
			page = a.Path
		}
		notices = append(notices, Notice{
			ID:         "alert-" + a.Key + "-" + strconv.Itoa(entry.Generation),
			Kind:       KindTask,
			TaskKey:    key,
			Generation: entry.Generation,
			Title:      "予約の実行に問題があります",
			Body:       a.Title + "：" + a.Text,
			URL:        strings.TrimRight(siteURL, "/") + page,
			Fire:       at,
			Expires:    at + 86400,
		})
	}
	for key, entry := range state.Tasks {
		if strings.HasPrefix(key, alertPrefix) && entry.Action && !listed[key] {
			entry.Action = false
			state.Tasks[key] = entry
		}
	}
	state.Outbox = append(keep(state.Outbox, func(n Notice) bool {
		return !strings.HasPrefix(n.TaskKey, alertPrefix) || listed[n.TaskKey] && state.Tasks[n.TaskKey].Generation == n.Generation
	}), notices...)
	return notices
}

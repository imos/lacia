package schedule

import (
	"fmt"
	"sort"
	"strings"
)

// Trouble is a job, or a device that runs jobs, that somebody has to look
// at. Nothing tells a job that silently stopped running from a quiet day
// unless it is shown where the user looks anyway.
type Trouble struct {
	// Key stays the same while the trouble lasts, so that it is told about
	// once.
	Key   string `json:"key"`
	Name  string `json:"name,omitempty"` // the job; empty for a device
	Title string `json:"title"`
	Text  string `json:"text"`
	Path  string `json:"path,omitempty"`
}

// troubleAfter is how many runs in a row have to fail, or not to run,
// before a job counts as in trouble. One may be a passing fault.
const troubleAfter = 2

// Troubles lists the jobs whose definition cannot be used, the jobs that
// failed or did not run several times in a row, and the devices that wait
// for the user to sign in.
func Troubles(root string, jobs []Job, devices map[string]DeviceStatus) []Trouble {
	var out []Trouble
	groups := map[string]*Trouble{}
	for _, job := range jobs {
		title := job.Title
		if title == "" {
			title = job.Name
		}
		if job.Err != "" {
			out = append(out, Trouble{Key: "schedule:" + job.Name + ":definition", Name: job.Name, Title: title, Text: "定義に問題があるので、動いていません。" + job.Err})
			continue
		}
		if !job.Enabled {
			continue
		}
		h := HealthOf(root, job.Name)
		if job.ID != "" {
			// Failed runs of fixed-ID routines have review receipts.
			if h.Missed >= troubleAfter {
				out = append(out, Trouble{Key: "schedule:" + job.Name + ":missed", Name: job.Name, Title: title, Text: fmt.Sprintf("直近の記録では、%d回続けて予約を実行していません。", h.Missed)})
			}
			continue
		}
		covered := false
		scopes := make([]string, 0, len(h.Unverified))
		for scope := range h.Unverified {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		for _, scope := range scopes {
			item := h.Unverified[scope]
			if item.Count < 3 && !(h.Failed >= troubleAfter && item.Count >= troubleAfter) {
				continue
			}
			covered = true
			key := "schedule:" + job.Name + ":unverified:" + scope + ":" + item.Reason
			if item.CauseKey != "" && item.Device != "" {
				key = "check:" + item.Device + ":" + scope + ":" + item.Reason + ":" + item.CauseKey
			}
			text := fmt.Sprintf("%s は、%s から%d回続けて確かめられませんでした。", scope, item.Since.Format("2006-01-02 15:04 MST"), item.Count)
			if item.Reason != "" {
				text += " 報告された理由は「" + item.Reason + "」です。"
			}
			if !h.LastSuccess.IsZero() {
				text += " 最後にすべての範囲を確かめられたのは " + h.LastSuccess.Format("2006-01-02 15:04 MST") + " です。"
			}
			if existing := groups[key]; existing != nil {
				existing.Title += "、" + title
				existing.Text += " " + title + ": " + text
				existing.Name = ""
				existing.Path = "/routines"
			} else {
				groups[key] = &Trouble{Key: key, Name: job.Name, Title: title, Text: text, Path: "/routines/" + job.Name}
			}
		}
		switch {
		case covered:
		case h.Failed >= troubleAfter:
			out = append(out, Trouble{Key: "schedule:" + job.Name + ":failed", Name: job.Name, Title: title, Text: fmt.Sprintf("実行されなかった回を除くと、直近%d件の記録はすべて失敗でした。", h.Failed)})
		case h.Missed >= troubleAfter:
			out = append(out, Trouble{Key: "schedule:" + job.Name + ":missed", Name: job.Name, Title: title, Text: fmt.Sprintf("直近の記録では、%d回続けて予約を実行していません。", h.Missed)})
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := *groups[key]
		item.Title = strings.TrimSpace(item.Title)
		out = append(out, item)
	}
	names := make([]string, 0, len(devices))
	for name := range devices {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if devices[name].Status == WaitingAuth {
			out = append(out, Trouble{Key: "device:" + name + ":auth", Title: "端末 " + name, Text: "認証を待っています。この端末で動かす予約は、認証が済むまで始まりません。"})
		}
	}
	return out
}

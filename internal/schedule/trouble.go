package schedule

import (
	"fmt"
	"sort"
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

// troubleAfter is how many runs in a row have to be missed before a routine
// counts as in trouble. One may be a passing fault.
const troubleAfter = 2

// Troubles lists the routines whose definition cannot be used, the routines
// that did not run several times in a row, and the devices that wait for the
// user to sign in. Failed runs of a routine have review receipts instead.
func Troubles(root string, jobs []Job, devices map[string]DeviceStatus) []Trouble {
	var out []Trouble
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
		if h := HealthOf(root, job.Name); h.Missed >= troubleAfter {
			out = append(out, Trouble{Key: "schedule:" + job.Name + ":missed", Name: job.Name, Title: title, Text: fmt.Sprintf("直近の記録では、%d回続けて予約を実行していません。", h.Missed)})
		}
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

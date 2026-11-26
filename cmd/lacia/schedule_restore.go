package main

import (
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/schedule"
)

// restoreScheduledExecution uses the assignment committed with this ID,
// even if installation routing changed before the runner saved its slot.
func restoreScheduledExecution(queue *request.Queue, dispatch *schedule.Dispatch, runs *schedule.RunLog, id string, now time.Time) (bool, error) {
	e, ok := dispatch.Get(id)
	if !ok {
		return false, nil
	}
	if e.Status == "missed" {
		return true, runs.Skip(e.Job, now)
	}
	if e.Device == schedule.LocalDevice && !e.Result.Settled {
		if _, exists := queue.Get(id); !exists {
			if e.Status == schedule.Executing {
				if err := dispatch.SyncLocal(id, request.Unknown, schedule.Result{Settled: true, Failed: true, Error: "すでに始まった依頼の記録が見つかりません。この回は自動でやり直さず、結果の確認を待ちます。", Started: e.Started, Finished: now}); err != nil {
					return true, err
				}
				return true, runs.Begin(e.Job, id, now)

			}
			label := e.Job.Title
			if label == "" {
				label = e.Job.Name
			}
			if _, _, err := queue.Add(request.Request{ID: id, Kind: request.System, Source: "予約「" + label + "」", Side: e.Job.Side, Agent: e.Kind, ReplyTo: e.Job.ReplyTo, Text: e.Prompt}); err != nil {
				return true, err
			}
		}
	}
	return true, runs.Begin(e.Job, id, now)
}

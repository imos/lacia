package notify

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/imos/lacia/internal/routine"
)

const routinePrefix = "routine:"

// RoutineNotice identifies a single accepted run. A retry must use the same
// notification ID, including after an agent or server restart.
type RoutineNotice struct {
	RoutineID   string `json:"routine_id"`
	Sequence    uint64 `json:"sequence"`
	Title       string `json:"title"`
	Count       int    `json:"count"`
	ReceiptPage bool   `json:"receipt_page,omitempty"`
	Action      bool   `json:"action,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

func (r RoutineNotice) ID() string {
	if r.Action {
		return fmt.Sprintf("routine-action-%s-%d", r.RoutineID, r.Sequence)
	}
	return fmt.Sprintf("routine-%s-%d", r.RoutineID, r.Sequence)
}

func planRoutines(results []RoutineNotice, state *State, now time.Time, siteURL string) {
	at := seconds(now)
	queued := map[string]bool{}
	for _, n := range state.Outbox {
		queued[n.ID] = true
	}
	for _, result := range results {
		if result.RoutineID == "" || result.Sequence == 0 || queued[result.ID()] {
			continue
		}
		target := "/routines/" + url.PathEscape(result.RoutineID)
		title := "定期タスクに新しい結果があります"
		body := fmt.Sprintf("%s %s（%d件）", result.RoutineID, result.Title, result.Count)
		if result.ReceiptPage {
			target = routine.ReceiptURL(result.RoutineID, result.Sequence)
		}
		if result.Action {
			target += "#task"
			title = "定期タスクでお願いしたいことがあります"
			if result.Summary != "" {
				body += "\n" + result.Summary
			}
		}
		state.Outbox = append(state.Outbox, Notice{
			ID: result.ID(), Kind: KindTask, TaskKey: routinePrefix + result.ID(),
			Title: title,
			Body:  body,
			URL:   strings.TrimRight(siteURL, "/") + target,
			Fire:  at, Expires: at + 86400,
		})
		queued[result.ID()] = true
	}
}

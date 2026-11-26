package notify

import (
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestClosingIsAnnouncedAsFinishedWork(t *testing.T) {
	state := NewState()
	plan := func(status task.Status) []Notice {
		return PlanTasks([]task.Task{fixtureTask(status)}, state, testNow, testSite)
	}
	plan(task.Doing) // the baseline

	finished := plan(task.Closing)
	if len(finished) != 1 || finished[0].Title != "作業が終わりました" || finished[0].Body != "T1001 Fixture" || finished[0].URL != testSite+"/tasks/T1001" {
		t.Fatalf("entering closing: %+v", finished)
	}
	if again := plan(task.Closing); len(again) != 0 {
		t.Errorf("an unchanged closing task notified again: %+v", again)
	}

	// The server moved the task to review: there is something new to decide.
	reopened := plan(task.Review)
	if len(reopened) != 1 || reopened[0].Title != "確認をお願いします" || reopened[0].Generation != finished[0].Generation+1 {
		t.Fatalf("from closing to review: %+v", reopened)
	}
	if len(state.Outbox) != 1 || state.Outbox[0].ID != reopened[0].ID {
		t.Errorf("the notice of the finished work must give way: %+v", state.Outbox)
	}

	// Sorting a task that was in review already into closing asks nothing new.
	if renamed := plan(task.Closing); len(renamed) != 0 {
		t.Errorf("from review to closing: %+v", renamed)
	}
	plan(task.Done)
	if len(state.Outbox) != 0 || state.Tasks["T1001"].Action || state.Tasks["T1001"].Closing {
		t.Errorf("after closing the task: outbox %+v, entry %+v", state.Outbox, state.Tasks["T1001"])
	}

	// Review keeps its heading when it is entered the usual way.
	if usual := plan(task.Review); len(usual) != 1 || usual[0].Title != "確認をお願いします" {
		t.Errorf("entering review: %+v", usual)
	}
}

package main

import (
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestRemindersForTheTwoWaysOfWaitingOnTheUser(t *testing.T) {
	// A note on a closing task usually means that work has resumed.
	if line := resumeReminder(task.Task{ID: "T1001", Status: task.Closing}); !strings.Contains(line, "「完了確認待ち」のままです") || !strings.Contains(line, "lacia task set T1001 doing") {
		t.Errorf("resume reminder for closing: %q", line)
	}
	// Moving a task to review points at the other status, and only then.
	for status, want := range map[task.Status]bool{
		task.Review: true, task.Closing: false, task.Doing: false, task.Waiting: false, task.Todo: false, task.Done: false, task.Cancelled: false,
	} {
		line := closingReminder(task.Task{ID: "T1001", Status: status})
		if (line != "") != want {
			t.Errorf("%s: closing reminder = %q, want shown = %v", status, line, want)
		}
		if want && !strings.Contains(line, "lacia task set T1001 closing") {
			t.Errorf("%s: reminder does not give the command: %q", status, line)
		}
	}
}

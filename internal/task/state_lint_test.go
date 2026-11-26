package task

import (
	"strings"
	"testing"
	"time"
)

func TestStateWarnings(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, JST)
	context := StateContext{Now: now, StaleAfter: 12 * time.Hour, OwnerLast: map[string]time.Time{"old": now.Add(-12 * time.Hour), "recent": now.Add(-time.Hour), "zero": {}}}
	tests := []struct {
		name         string
		task         Task
		dependencies map[string]Task
		want         string
	}{
		{name: "future review", task: Task{Status: Review, ResumeOn: "2026-11-26"}, want: "確認待ちですが"},
		{name: "equal review", task: Task{Status: Review, ResumeOn: now.Format(time.RFC3339)}},
		{name: "past review", task: Task{Status: Review, ResumeOn: "2026-11-24"}},
		{name: "timezone review", task: Task{Status: Review, ResumeOn: "2026-11-25", Timezone: "America/New_York"}, want: "確認待ちですが"},
		{name: "waiting future", task: Task{Status: Waiting, ResumeOn: "2026-11-26"}},
		{name: "done future", task: Task{Status: Done, ResumeOn: "2026-11-26"}},
		{name: "old known use", task: Task{Status: Doing, Owner: "local-codex", OwnerSession: "old"}, want: "12時間以上前"},
		{name: "recent use", task: Task{Status: Doing, OwnerSession: "recent"}},
		{name: "unknown use", task: Task{Status: Doing, OwnerSession: "unknown"}},
		{name: "zero use", task: Task{Status: Doing, OwnerSession: "zero"}},
		{name: "no session", task: Task{Status: Doing, Owner: "local-codex"}},
		{name: "review old use", task: Task{Status: Review, OwnerSession: "old"}},
		{name: "open dependency", task: Task{Status: Waiting, After: []string{"B"}}, dependencies: map[string]Task{"B": {ID: "B", Title: "相手", Status: Todo}}, want: "after で待つタスク B（相手）"},
		{name: "missing dependency", task: Task{Status: Waiting, After: []string{"B"}}},
		{name: "closed dependent", task: Task{Status: Cancelled, After: []string{"B"}}, dependencies: map[string]Task{"B": {ID: "B", Status: Todo}}},
	}
	for _, field := range []string{"date", "review", "start", "closed", "doing"} {
		d := Task{ID: "B", Status: Todo}
		switch field {
		case "date":
			d.ResumeOn = "2026-11-26"
		case "review":
			d.ReviewAt = "2026-11-26"
		case "start":
			d.OnReady = "start"
		case "closed":
			d.Status = Done
		case "doing":
			d.Status = Doing
		}
		tests = append(tests, struct {
			name         string
			task         Task
			dependencies map[string]Task
			want         string
		}{name: "dependency " + field, task: Task{Status: Waiting, After: []string{"B"}}, dependencies: map[string]Task{"B": d}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := StateWarnings(tt.task, tt.dependencies, context)
			if tt.want == "" {
				if len(warnings) != 0 {
					t.Fatalf("unexpected warnings: %v", warnings)
				}
				return
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0].Message, tt.want) {
				t.Fatalf("warnings = %v, want %q", warnings, tt.want)
			}
		})
	}
	// Without a registry, even very old task metadata is not conversation activity.
	if got := StateWarnings(Task{Status: Doing, OwnerSession: "old", ModTime: now.Add(-48 * time.Hour)}, nil, StateContext{Now: now, StaleAfter: 12 * time.Hour}); len(got) != 0 {
		t.Fatal(got)
	}
}

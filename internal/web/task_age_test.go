package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/testrecords"
)

func TestTaskAgeUsesTheRequestThatStartedWork(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, task.JST)
	accepted, started := now.Add(-5*time.Hour), now.Add(-2*time.Hour)
	dir := t.TempDir()
	month := filepath.Join(dir, "2026-11")
	if err := os.Mkdir(month, 0o700); err != nil {
		t.Fatal(err)
	}
	req := request.Request{ID: "request-1", TaskID: "T1001", Status: request.Running, Created: accepted, Started: started}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(month, req.ID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	queue, err := request.OpenRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	row := Row{Task: task.Task{ID: req.TaskID, Status: task.Doing}, ListStatus: task.Doing}
	s := Server{Requests: queue}
	lines := []task.Line{
		{OK: true, Event: task.Event{Type: task.EventStatus, From: "todo", To: "doing", By: "依頼の受付", At: accepted.Format(time.RFC3339)}},
		{OK: true, Event: task.Event{Type: task.EventRequest, Request: req.ID, Via: "依頼の受付"}},
		{OK: true, Event: task.Event{Type: task.EventClaimed, Owner: "local-codex", At: now.Add(-time.Hour).Format(time.RFC3339)}},
	}
	if got, ok := s.requestStatusTime(row, lines, now); !ok || !got.Equal(started) {
		t.Fatalf("started = %v, %v, want %v", got, ok, started)
	}
	lines[0].Event.By = "local-claude"
	if _, ok := s.requestStatusTime(row, lines, now); ok {
		t.Fatal("a later request should not reset an existing task")
	}
}

func TestTaskStatusAge(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "0.0分前"},
		{18 * time.Second, "0.3分前"},
		{3*time.Minute + 6*time.Second, "3.1分前"},
		{9*time.Minute + 57*time.Second, "10分前"},
		{34 * time.Minute, "34分前"},
		{time.Hour, "1.0時間前"},
		{3*time.Hour + 6*time.Minute, "3.1時間前"},
		{12 * time.Hour, "12時間前"},
		{24 * time.Hour, "1.0日前"},
		{37 * time.Hour, "1.5日前"},
		{15 * 24 * time.Hour, "15日前"},
		{146 * 24 * time.Hour, "150日前"},
	} {
		if got := taskStatusAge(tc.elapsed); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.elapsed, got, tc.want)
		}
	}
}

func TestScheduledTaskStatusTime(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, task.JST)
	first, second := now.Add(-5*time.Hour), now.Add(-2*time.Hour)
	until := now.Add(time.Hour).Format("2006-01-02 15:04")
	empty := ""
	created := task.Event{At: first.Format(time.RFC3339), Type: task.EventCreated, Status: string(task.Todo)}
	resume := task.Event{At: first.Format(time.RFC3339), Type: task.EventField, Key: "resume_on", Value: &until}
	dependency := "T1001"
	blocked := task.Event{At: second.Format(time.RFC3339), Type: task.EventField, Key: "after", Value: &dependency}
	for _, tc := range []struct {
		name    string
		current task.Task
		status  task.Status
		events  []task.Event
		want    time.Time
	}{
		{"resume date", task.Task{Status: task.Todo, ResumeOn: until}, task.Waiting, []task.Event{created, resume}, first},
		{"waiting without an event", task.Task{Status: task.Todo, ResumeOn: until}, task.Waiting, []task.Event{created}, time.Time{}},
		{"dependency extends wait", task.Task{Status: task.Todo, Blockers: []string{dependency}, ResumeOn: until}, task.Waiting, []task.Event{created, resume, blocked}, first},
		{"condition removed", task.Task{Status: task.Todo}, task.Todo, []task.Event{
			created, resume, {At: second.Format(time.RFC3339), Type: task.EventField, Key: "resume_on", Value: &empty},
		}, second},
		{"wake to review", task.Task{Status: task.Review}, task.Review, []task.Event{
			created, resume, {At: second.Format(time.RFC3339), Type: task.EventWoke, Action: task.WakeClearedConditions, From: "todo", To: "review"},
		}, second},
		{"fault after review", task.Task{Status: task.Review}, task.Review, []task.Event{
			created,
			{At: second.Format(time.RFC3339), Type: task.EventStatus, From: "todo", To: "review"},
			{At: now.Format(time.RFC3339), Type: task.EventStopped},
		}, second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lines []task.Line
			for _, e := range tc.events {
				lines = append(lines, task.Line{OK: true, Event: e})
			}
			if got := scheduledStatusTime(lines, tc.current, tc.status, now); !got.Equal(tc.want) {
				t.Errorf("since = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTaskListsShowAndSortByStatusTime(t *testing.T) {
	root := t.TempDir()
	store, err := task.Open(testrecords.Init(t, root))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, task.JST)
	makeTask := func(status task.Status, hours time.Duration, known bool) task.Task {
		t.Helper()
		item, err := store.Create(task.NewTask{Area: "work", Title: "表示の確認", Status: status})
		if err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(root, item.Dir, task.HistoryFile)
		if err := os.Remove(log); err != nil {
			t.Fatal(err)
		}
		if known {
			if err := task.AppendEvent(log, now.Add(-hours), task.Event{Type: task.EventCreated, Status: string(status)}); err != nil {
				t.Fatal(err)
			}
			for _, e := range []task.Event{
				{Type: task.EventStatus, From: string(status), To: string(status)},
				{Type: task.EventClaimed, Owner: "local-claude"},
				{Type: task.EventNote, Text: "状態は同じままです。"},
			} {
				if err := task.AppendEvent(log, now.Add(-time.Minute), e); err != nil {
					t.Fatal(err)
				}
			}
		}
		return item
	}
	newer := makeTask(task.Review, 3*time.Hour+6*time.Minute, true)
	older := makeTask(task.Review, 10*time.Hour, true)
	doing := makeTask(task.Doing, 8*time.Minute, true)
	waiting := makeTask(task.Waiting, 29*time.Hour, true)
	todo := makeTask(task.Todo, 8*time.Hour, true)
	unknown := makeTask(task.Todo, 0, false)
	h, err := (&Server{Store: store, Now: func() time.Time { return now }}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/tasks", "/tasks?area=work"} {
		t.Run(path, func(t *testing.T) {
			code, body, _ := get(t, h, path)
			if code != 200 {
				t.Fatalf("HTTP %d: %s", code, body)
			}
			for _, pair := range []struct {
				task task.Task
				age  string
			}{{newer, "3.1時間前"}, {older, "10時間前"}, {doing, "8.0分前"}, {waiting, "1.2日前"}, {todo, "8.0時間前"}} {
				start := `href="/tasks/` + pair.task.ID + `">` + pair.task.ID + `</a><time class="task-age"`
				_, after, ok := strings.Cut(body, start)
				if end := strings.Index(after, "</time>"); !ok || end < 0 || !strings.HasSuffix(after[:end], ">("+pair.age+")") {
					t.Errorf("missing age %s under %s", pair.age, pair.task.ID)
				}
			}
			idRef := func(item task.Task) int {
				return strings.Index(body, `href="/tasks/`+item.ID+`">`+item.ID+`</a>`)
			}
			if idRef(newer) >= idRef(older) {
				t.Error("the most recent transition should be first")
			}
			if idRef(todo) >= idRef(unknown) || strings.Contains(body, `>`+unknown.ID+`</a><time`) {
				t.Error("the undated task should follow the known age without inventing a time")
			}
		})
	}
}

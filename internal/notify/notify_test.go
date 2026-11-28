package notify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/task"
)

func fixtureTask(status task.Status) task.Task {
	return task.Task{ID: "T1001", Title: "Fixture", Status: status, Area: "work", Dir: "work/tasks/T1001-fixture"}
}

func TestBaselineTransitionAndReentry(t *testing.T) {
	state := NewState()
	plan := func(status task.Status) []Notice {
		return PlanTasks([]task.Task{fixtureTask(status)}, state, testNow, testSite)
	}
	if got := plan(task.Review); len(got) != 0 {
		t.Fatalf("the baseline notified: %+v", got)
	}
	plan(task.Doing)
	first := plan(task.Review)
	want := []Notice{{
		ID: "task-T1001-1", Kind: "task", TaskKey: "T1001", Generation: 1, Title: "確認をお願いします", Body: "T1001 Fixture",
		URL: testSite + "/tasks/T1001", Fire: seconds(testNow), Expires: seconds(testNow) + 86400,
	}}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("first = %+v\nwant %+v", first, want)
	}
	if got := plan(task.Review); len(got) != 0 {
		t.Errorf("an unchanged task notified again: %+v", got)
	}
	plan(task.Doing)
	if second := plan(task.Review); len(second) != 1 || second[0].ID != "task-T1001-2" {
		t.Errorf("second = %+v", second)
	}
}

func TestNewTaskAfterBaselineAndReappearance(t *testing.T) {
	state := NewState()
	PlanTasks(nil, state, testNow, testSite)
	if state.Tasks == nil {
		t.Fatal("an empty list must still set the baseline")
	}
	got := PlanTasks([]task.Task{fixtureTask(task.Review)}, state, testNow, testSite)
	if len(got) != 1 || got[0].ID != "task-T1001-0" {
		t.Fatalf("a new task in review: %+v", got)
	}
	// The task disappears and comes back: remembered, so not a new transition.
	PlanTasks(nil, state, testNow, testSite)
	if len(state.Outbox) != 0 || !state.Tasks["T1001"].Action {
		t.Errorf("after disappearing: outbox = %+v, tasks = %+v", state.Outbox, state.Tasks)
	}
	if got := PlanTasks([]task.Task{fixtureTask(task.Review)}, state, testNow, testSite); len(got) != 0 {
		t.Errorf("a reappearing task notified: %+v", got)
	}
}

func TestFailedFetchDoesNotMoveOrResetBaseline(t *testing.T) {
	state := NewState()
	poll := func(tasks []task.Task, err error) Manifest {
		config := DefaultConfig()
		config.MeetingNotifications = false
		return Plan(Input{Config: config, Tasks: tasks, TasksErr: err, Now: testNow, SiteURL: testSite}, state)
	}
	poll([]task.Task{fixtureTask(task.Doing)}, nil)
	before := map[string]TaskEntry{"T1001": {}}
	if got := poll(nil, errors.New("fetch failed")); got.Health.Tasks != HealthUnavailable || !reflect.DeepEqual(state.Tasks, before) {
		t.Errorf("health = %+v, tasks = %+v", got.Health, state.Tasks)
	}
	poll([]task.Task{}, nil)
	if got := poll([]task.Task{fixtureTask(task.Review)}, nil); len(got.Notices) != 1 || got.Health.Tasks != HealthOK {
		t.Errorf("manifest = %+v", got)
	}

	// Before any baseline, a failure must not create one.
	state = NewState()
	poll(nil, errors.New("fetch failed"))
	if state.Tasks != nil {
		t.Errorf("a failed fetch set the baseline: %+v", state.Tasks)
	}
	if got := poll([]task.Task{fixtureTask(task.Review)}, nil); len(got.Notices) != 0 {
		t.Errorf("the first successful poll notified: %+v", got.Notices)
	}
}

func TestOutboxReconcilesOnlyOnAuthoritativeActionGeneration(t *testing.T) {
	state := NewState()
	poll := func(tasks []task.Task, err error) Manifest {
		return Plan(Input{Config: DefaultConfig(), Tasks: tasks, TasksErr: err, SnapshotErr: errors.New("none"), Now: testNow, SiteURL: testSite}, state)
	}
	poll([]task.Task{fixtureTask(task.Doing)}, nil)
	poll([]task.Task{fixtureTask(task.Review)}, nil)
	if len(state.Outbox) != 1 {
		t.Fatalf("outbox = %+v", state.Outbox)
	}
	notice := state.Outbox[0]
	if got := poll(nil, errors.New("fetch failed")); !reflect.DeepEqual(got.Notices, []Notice{notice}) || !reflect.DeepEqual(state.Outbox, []Notice{notice}) {
		t.Errorf("a failed fetch dropped the pending notice: %+v", got.Notices)
	}
	if got := poll([]task.Task{fixtureTask(task.Done)}, nil); len(got.Notices) != 0 || len(state.Outbox) != 0 {
		t.Errorf("a finished task stayed in the outbox: %+v", state.Outbox)
	}
	poll([]task.Task{fixtureTask(task.Review)}, nil)
	if len(state.Outbox) != 1 || state.Outbox[0].Generation == notice.Generation {
		t.Errorf("outbox = %+v", state.Outbox)
	}
	poll([]task.Task{}, nil)
	if len(state.Outbox) != 0 {
		t.Errorf("a missing task stayed in the outbox: %+v", state.Outbox)
	}
	// An undelivered notice expires after 24 hours even if the task stays in review.
	poll([]task.Task{fixtureTask(task.Doing)}, nil)
	poll([]task.Task{fixtureTask(task.Review)}, nil)
	late := Plan(Input{Config: DefaultConfig(), Tasks: []task.Task{fixtureTask(task.Review)}, SnapshotErr: errors.New("none"), Now: testNow.Add(24 * time.Hour), SiteURL: testSite}, state)
	if len(late.Notices) != 0 {
		t.Errorf("an expired notice stayed: %+v", late.Notices)
	}
}

func TestNeedsActionStatusAndSnoozeInJST(t *testing.T) {
	for _, status := range []task.Status{task.Todo, task.Doing, task.Waiting, task.Done, task.Cancelled} {
		if NeedsAction(fixtureTask(status), testNow) {
			t.Errorf("%s needs action", status)
		}
	}
	snoozed := fixtureTask(task.Review)
	snoozed.ResumeOn = "2026-11-11"
	if NeedsAction(snoozed, testNow) {
		t.Error("a snoozed task needs action")
	}
	// 2026-11-10T15:00Z is midnight of the 11th in Japan.
	if !NeedsAction(snoozed, time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)) {
		t.Error("the task did not resume at JST midnight")
	}
	if NeedsAction(snoozed, time.Date(2026, 11, 10, 14, 59, 59, 0, time.UTC)) {
		t.Error("the task resumed before JST midnight")
	}
}

func TestPlanHealthAndRetainedMeetings(t *testing.T) {
	snapshot, _ := json.Marshal(linkedSnapshot([]any{linkedEvent(nil)}, nil))
	state := NewState()
	input := Input{Config: DefaultConfig(), Tasks: []task.Task{}, Snapshot: snapshot, Now: testNow, SiteURL: testSite}
	first := Plan(input, state)
	if want := (Health{CheckedAt: seconds(testNow), Tasks: "ok", Calendar: "ok", Transport: "ok"}); !reflect.DeepEqual(first.Health, want) || len(first.Notices) != 1 {
		t.Fatalf("first = %+v", first)
	}
	if !reflect.DeepEqual(first.ConfirmedMeetings, []string{first.Notices[0].ID}) {
		t.Fatalf("confirmed meetings = %v", first.ConfirmedMeetings)
	}

	failed := input
	failed.Snapshot, failed.SnapshotErr = nil, errors.New("fetch failed")
	if got := Plan(failed, state); got.Health.Calendar != "unavailable" || !reflect.DeepEqual(got.Notices, first.Notices) || len(got.ConfirmedMeetings) != 0 {
		t.Errorf("a failed fetch: %+v", got)
	}
	broken := input
	broken.Snapshot = []byte(`{"schema_version":2}`)
	if got := Plan(broken, state); got.Health.Calendar != "unavailable" || !reflect.DeepEqual(got.Notices, first.Notices) || len(got.ConfirmedMeetings) != 0 {
		t.Errorf("a broken snapshot: %+v", got)
	}
	// The retained notice is dropped once it has expired.
	failed.Now = testNow.Add(time.Hour)
	if got := Plan(failed, state); len(got.Notices) != 0 {
		t.Errorf("an expired meeting stayed: %+v", got.Notices)
	}

	partial, _ := json.Marshal(linkedSnapshot([]any{}, map[string]any{"status": "partial", "complete": false}))
	stale := input
	stale.Snapshot = partial
	if got := Plan(stale, state); got.Health.Calendar != "partial" || len(got.Notices) != 1 || len(got.ConfirmedMeetings) != 0 {
		t.Errorf("a partial source: %+v", got)
	}

	for _, c := range []struct {
		change   func(*Config)
		tasks    string
		calendar string
		notices  int
	}{
		{func(c *Config) { c.Enabled = false }, "disabled", "disabled", 0},
		{func(c *Config) { c.MeetingNotifications = false }, "ok", "disabled", 0},
		{func(c *Config) { c.TaskNotifications = false }, "disabled", "ok", 1},
		{func(c *Config) { c.MeetingSource = "snapshot" }, "ok", "unavailable", 0},
		{func(c *Config) { c.MeetingSource = "eventkit" }, "ok", "disabled", 0},
	} {
		in := input
		c.change(&in.Config)
		in.Transport = "failed"
		got := Plan(in, state)
		if got.Health.Tasks != c.tasks || got.Health.Calendar != c.calendar || got.Health.Transport != "failed" || len(got.Notices) != c.notices || got.Notices == nil || len(got.ConfirmedMeetings) != 0 {
			t.Errorf("%+v: %+v", in.Config, got)
		}
	}
}

func TestPlanConfirmsOnlyObservedMeetingsInTheirDeliveryWindow(t *testing.T) {
	config := DefaultConfig()
	start := testNow.Add(time.Hour)
	fresh := map[string]any{"updated_at": "2026-11-10T09:58:00+09:00"}
	event := linkedEvent(nil)
	poll := func(state *State, snapshot map[string]any, now time.Time) Manifest {
		t.Helper()
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return Plan(Input{Config: config, Snapshot: raw, Now: now, SiteURL: testSite}, state)
	}
	state := NewState()
	initial := poll(state, linkedSnapshot([]any{event}, fresh), start.Add(-2*time.Minute))
	if len(initial.Notices) != 1 {
		t.Fatalf("the advance plan must contain one notice: %+v", initial)
	}
	id := initial.Notices[0].ID
	due := start.Add(-45 * time.Second)
	withFamilyFailure := linkedSnapshot([]any{event}, fresh)
	withFamilyFailure["sources"].([]any)[1].(map[string]any)["status"] = "error"
	if got := poll(state, withFamilyFailure, due); got.Health.Calendar != HealthPartial ||
		!reflect.DeepEqual(got.ConfirmedMeetings, []string{id}) {
		t.Errorf("a confirmed occurrence was lost after fire time: %+v", got)
	}
	unknown := linkedEvent(nil)
	delete(unknown, "meeting_candidate")
	moved := linkedEvent(map[string]any{
		"start": map[string]any{"dateTime": "2026-11-10T12:00:00+09:00"},
		"end":   map[string]any{"dateTime": "2026-11-10T13:00:00+09:00"},
	})
	for _, c := range []struct {
		name     string
		events   []any
		metadata map[string]any
	}{
		{"absent from a partial snapshot", []any{}, map[string]any{"status": "partial", "complete": false}},
		{"copied into a partial snapshot", []any{event}, map[string]any{"status": "partial", "complete": false}},
		{"unverified scope", []any{event}, map[string]any{"scope_verified": false}},
		{"stale snapshot", []any{event}, map[string]any{"updated_at": "2026-11-10T09:00:00+09:00"}},
		{"unclassified event", []any{unknown}, fresh},
		{"cancelled event", []any{linkedEvent(map[string]any{"status": "cancelled"})}, fresh},
		{"rescheduled event", []any{moved}, fresh},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := &State{Meetings: initial.Notices}
			got := poll(state, linkedSnapshot(c.events, c.metadata), due)
			for _, confirmed := range got.ConfirmedMeetings {
				if confirmed == id {
					t.Errorf("the original occurrence was confirmed: %+v", got)
				}
			}
		})
	}
	if got := poll(NewState(), withFamilyFailure, due); len(got.Notices) != 0 || len(got.ConfirmedMeetings) != 0 {
		t.Errorf("a missing advance plan was created after fire time: %+v", got)
	}
}

func TestParseConfig(t *testing.T) {
	if got, err := ParseConfig([]byte(`{"transport":{"port":1}}`)); err != nil || !reflect.DeepEqual(got, DefaultConfig()) {
		t.Errorf("defaults = %+v, %v", got, err)
	}
	got, err := ParseConfig([]byte(`{"enabled":false,"task_notifications":false,"meeting_notifications":false,"meeting_source":"eventkit","exclude_meetings":[{"event_id":"x"}]}`))
	want := Config{MeetingSource: "eventkit", ExcludeMeetings: []any{map[string]any{"event_id": "x"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("config = %+v, %v", got, err)
	}
	for _, raw := range []string{``, `[]`, `null`} {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("%q: no error", raw)
		}
	}
}

func TestStateAndManifestFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	files := testDir(t, dir)
	statePath := filepath.Join(dir, "agent-state.json")
	state, err := LoadState(files, "agent-state.json")
	if err != nil || state.Tasks != nil || state.Version != Version {
		t.Fatalf("first run = %+v, %v", state, err)
	}
	if err := SaveState(files, "agent-state.json", state); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(statePath); string(raw) != `{"version":1,"tasks":null,"outbox":[],"meetings":[]}` {
		t.Errorf("fresh state = %s", raw)
	}

	snapshot, _ := json.Marshal(linkedSnapshot([]any{linkedEvent(map[string]any{"title": "A&B <会議>"})}, nil))
	input := Input{Config: DefaultConfig(), Tasks: []task.Task{}, Snapshot: snapshot, Now: testNow, SiteURL: testSite}
	Plan(input, state)
	input.Tasks = []task.Task{fixtureTask(task.Review)}
	manifest := Plan(input, state)
	if err := SaveState(files, "agent-state.json", state); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := WriteManifest(files, "manifest.json", manifest); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{statePath, manifestPath} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode = %v, %v", path, info.Mode(), err)
		}
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v", info.Mode())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("temporary files were left: %v", entries)
	}

	reloaded, err := LoadState(files, "agent-state.json")
	if err != nil || !reflect.DeepEqual(reloaded, state) {
		t.Errorf("reloaded = %+v, %v\nwant %+v", reloaded, err, state)
	}
	if got, err := ReadManifest(files, "manifest.json"); err != nil || !reflect.DeepEqual(got, manifest) {
		t.Errorf("manifest = %+v, %v\nwant %+v", got, err, manifest)
	}

	// The field names are what the Swift app and the old state file use.
	raw, _ := os.ReadFile(manifestPath)
	var fields struct {
		Version int
		Notices []map[string]any
		Health  map[string]any
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) string {
		names := []string{}
		for _, name := range []string{"id", "kind", "title", "body", "url", "fire", "expires", "task_key", "generation", "checked_at", "tasks", "calendar", "transport"} {
			if _, ok := m[name]; ok {
				names = append(names, name)
			}
		}
		if len(names) != len(m) {
			t.Errorf("unexpected keys in %v", m)
		}
		return strings.Join(names, " ")
	}
	if fields.Version != 1 || len(fields.Notices) != 2 ||
		keys(fields.Notices[0]) != "id kind title body url fire expires task_key generation" ||
		fields.Notices[0]["generation"] != float64(0) ||
		keys(fields.Notices[1]) != "id kind title body url fire expires" ||
		keys(fields.Health) != "checked_at tasks calendar transport" {
		t.Errorf("manifest = %s", raw)
	}
	if !strings.Contains(string(raw), `10:00 A&B <会議>`) {
		t.Errorf("text was escaped: %s", raw)
	}

	// An empty manifest must hold an array, which the Swift decoder requires.
	if err := WriteManifest(files, "manifest.json", Manifest{Version: Version}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(manifestPath); !strings.Contains(string(raw), `"notices":[]`) {
		t.Errorf("empty manifest = %s", raw)
	}

	if err := os.WriteFile(statePath, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(files, "agent-state.json"); !errors.Is(err, ErrStateVersion) {
		t.Errorf("version 2: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(files, "agent-state.json", state); !errors.Is(err, ErrDirectoryPrivate) {
		t.Errorf("an open directory: %v", err)
	}
}

func TestAStoppedLoopIsToldEvenWhenTheTaskWaitedAlready(t *testing.T) {
	now := time.Date(2026, 11, 19, 0, 10, 0, 0, time.UTC)
	state := &State{}
	waiting := task.Task{ID: "T1062", Title: "評価の計画", Status: task.Review}
	if got := PlanTasks([]task.Task{waiting}, state, now, "https://example.test"); len(got) != 0 {
		t.Fatalf("the baseline told about %d tasks", len(got))
	}
	stopped := waiting
	stopped.StoppedAt = "2026-11-19 00:10"
	got := PlanTasks([]task.Task{stopped}, state, now, "https://example.test")
	if len(got) != 1 || got[0].Title != "繰り返しの書き込みを止めました" || got[0].Generation != 1 {
		t.Fatalf("after the stop: %+v", got)
	}
	// The same stop is told once; a later one is told again.
	if got := PlanTasks([]task.Task{stopped}, state, now, "https://example.test"); len(got) != 0 {
		t.Fatalf("the same stop was told twice: %+v", got)
	}
	if len(state.Outbox) != 1 {
		t.Fatalf("outbox = %+v", state.Outbox)
	}
	stopped.StoppedAt = "2026-11-19 00:42"
	if got := PlanTasks([]task.Task{stopped}, state, now, "https://example.test"); len(got) != 1 || got[0].Generation != 2 {
		t.Fatalf("after the second stop: %+v", got)
	}
	// A task that first appears with an old stop is an ordinary arrival.
	fresh := task.Task{ID: "T1070", Title: "新しいタスク", Status: task.Review}
	if got := PlanTasks([]task.Task{stopped, fresh}, state, now, "https://example.test"); len(got) != 1 || got[0].Title != "確認をお願いします" {
		t.Fatalf("a new task in review: %+v", got)
	}
}

func TestPlanAlerts(t *testing.T) {
	state := NewState()
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	PlanTasks(nil, state, now, "https://example.test")
	failing := []Alert{{Key: "schedule:github:failed", Name: "github", Title: "GitHubの確認", Text: "続けて2回、失敗しています。"}}
	got := PlanAlerts(failing, state, now, "https://example.test")
	if len(got) != 1 || got[0].URL != "https://example.test/schedules/github" || got[0].Kind != KindTask || got[0].Title != "予約の実行に問題があります" || got[0].Body != "GitHubの確認：続けて2回、失敗しています。" {
		t.Fatalf("first look: %+v", got)
	}
	// While it lasts it is not told about again, and the notice survives
	// the next look at the tasks.
	PlanTasks(nil, state, now, "https://example.test")
	if again := PlanAlerts(failing, state, now, "https://example.test"); len(again) != 0 || len(state.Outbox) != 1 {
		t.Fatalf("second look: %+v, outbox %+v", again, state.Outbox)
	}
	// Gone, and back: told about again.
	PlanTasks(nil, state, now, "https://example.test")
	PlanAlerts(nil, state, now, "https://example.test")
	if len(state.Outbox) != 0 {
		t.Fatalf("outbox after it went away: %+v", state.Outbox)
	}
	failing[0].Path = "/routines/github"
	if back := PlanAlerts(failing, state, now, "https://example.test"); len(back) != 1 || back[0].ID == got[0].ID || back[0].URL != "https://example.test/routines/github" {
		t.Fatalf("back: %+v", back)
	}
}

func TestRoutineNoticeOpensItsReceipt(t *testing.T) {
	state := NewState()
	result := RoutineNotice{RoutineID: "S1001", Sequence: 42, Title: "朝の確認", Count: 2, ReceiptPage: true}
	planRoutines([]RoutineNotice{result, result}, state, testNow, testSite+"/")
	if len(state.Outbox) != 1 || state.Outbox[0].ID != "routine-S1001-42" || state.Outbox[0].URL != testSite+"/routines/S1001/receipts/42" {
		t.Fatalf("receipt notifications: %+v", state.Outbox)
	}
	result.ReceiptPage = false
	result.Sequence++
	planRoutines([]RoutineNotice{result}, state, testNow, testSite+"/")
	if len(state.Outbox) != 2 || state.Outbox[1].URL != testSite+"/routines/S1001" {
		t.Fatalf("notification for server without receipt pages: %+v", state.Outbox)
	}
}

// testDir opens dir as a directory lacia may write in.
func testDir(t *testing.T, dir string) *disk.Dir {
	t.Helper()
	d, err := disk.OpenTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/notify"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/testrecords"
)

func TestExplicitRoutineMatters(t *testing.T) {
	for _, tc := range []struct {
		name, id, output, body, outcome, failure string
		scopes                                   []string
		legacy, handoff, reply, seeded, routed   bool
		closed, overlap, stopped                 bool
		receipts, notices                        int
	}{
		{name: "S1009 completion only", id: "S1009", body: `{"summary":"全ページを確認しました。AIPSの完了通知だけで新規依頼は0件です。"}`, outcome: schedule.RunDone, seeded: true},
		{name: "S1013 summary only", id: "S1013", body: `{"check_status":"complete","summary":"メールもコンプルも新しい依頼は0件です。"}`, outcome: schedule.RunDone},
		{name: "S1013 fixed scope normal zero", id: "S1013", scopes: []string{"inbox", "comple"}, body: `{"check_status":"complete","summary":"新しい依頼は0件です。","checks":[{"scope":"inbox","status":"verified"},{"scope":"comple","status":"verified"}]}`, outcome: schedule.RunDone},
		{name: "normal zero without summary", id: "S1009", body: `{"check_status":"complete","checks":[],"matters":[],"unverified":[],"handoffs":[]}`, outcome: schedule.RunNothing},
		{name: "S1016 actionable item and repeated revision", id: "S1016", body: `{"summary":"容量超過が1件です。","matters":[{"key":"local/storage","revision":"1","summary":"整理してよいファイルを選んでください。"}]}`, outcome: schedule.RunDone, receipts: 1, notices: 1},
		{name: "receipt with an existing reply route", id: "S1016", body: `{"matters":[{"key":"local/storage","revision":"1","summary":"整理してよいファイルを選んでください。"}]}`, outcome: schedule.RunDone, receipts: 1, routed: true},
		{name: "scheduled reply keeps each structured result", id: "S1009", output: "none", body: `{"summary":"晴れです。"}`, outcome: schedule.RunDone, routed: true},
		{name: "partial with explicit findings", id: "S1010", body: `{"check_status":"partial","unverified":["checks"],"matters":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。"}]}`, outcome: schedule.RunPartial, receipts: 1, notices: 1},
		{name: "legacy partial without findings", id: "S1001", body: `{"summary":"取得が途中で止まりました。","unverified":["private","review"]}`, outcome: schedule.RunPartial, legacy: true, receipts: 2, notices: 2},
		{name: "explicit partial without findings", id: "S1001", body: `{"summary":"取得が途中で止まりました。","unverified":["private","review"]}`, outcome: schedule.RunPartial},
		{name: "S1011 unavailable", id: "S1011", scopes: []string{"cluster"}, body: `{"check_status":"unavailable","reference":"/tasks/T1196","checks":[{"scope":"cluster","status":"unverified","reason":"denied"}]}`, outcome: schedule.RunFailed, failure: "check_unavailable"},
		{name: "required report missing", id: "S1014", scopes: []string{"jobs"}, outcome: schedule.RunFailed, failure: "result_missing"},
		{name: "no optional report with reply", id: "S1002", output: "none", outcome: schedule.RunDone, reply: true},
		{name: "explicit matter takes priority over handoff", overlap: true, receipts: 1, notices: 1, id: "S1010", handoff: true, body: `{"check_status":"complete","matters":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。"}],"handoffs":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。","task_id":"%s"}]}`, outcome: schedule.RunDone},
		{name: "confirmed handoff despite partial check", id: "S1010", handoff: true, body: `{"check_status":"partial","unverified":["checks"],"handoffs":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。","task_id":"%s"}]}`, outcome: schedule.RunPartial},
		{name: "task output links without receipt", id: "S1008", output: "task", handoff: true, body: `{"check_status":"complete","handoffs":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。","task_id":"%s"}]}`, outcome: schedule.RunDone},
		{name: "unverified handoff falls back to the receipt", id: "S1010", body: `{"check_status":"complete","handoffs":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。","task_id":"T999999"}]}`, outcome: schedule.RunPartial, failure: "handoff_unverified", receipts: 1, notices: 1},
		{name: "task output failed handoff goes to trouble", id: "S1008", output: "task", body: `{"check_status":"complete","handoffs":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。","task_id":"T999999"}]}`, outcome: schedule.RunPartial, failure: "handoff_unverified"},
		{name: "closed task falls back to the receipt", id: "S1010", handoff: true, closed: true, body: `{"check_status":"complete","handoffs":[{"key":"pr/1","revision":"a","summary":"依頼をレビューしてください。","task_id":"%s"}]}`, outcome: schedule.RunPartial, failure: "handoff_unverified", receipts: 1, notices: 1},
		{name: "stopped before start does not accept a result", id: "S1016", stopped: true, body: `{"matters":[{"key":"local/storage","revision":"1","summary":"整理してよいファイルを選んでください。"}]}`, outcome: schedule.RunSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			routines := "routines"
			if !tc.legacy && !tc.seeded {
				routines = "areas/work/routines"
				config := filepath.Join(root, task.ConfigFile)
				if err := os.MkdirAll(filepath.Dir(config), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(config, []byte(`{"format":2}`), 0644); err != nil {
					t.Fatal(err)
				}
			}
			store, err := task.Open(testrecords.Init(t, root))
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, routines, tc.id)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			def := "---\nid: " + tc.id + "\narea: work\ndaily: 09:00\n---\n確かめます。"
			if err := os.WriteFile(filepath.Join(dir, "routine.md"), []byte(def), 0644); err != nil {
				t.Fatal(err)
			}
			s := &Server{Store: store, Routines: routine.NewStore(root)}
			h, err := s.Handler()
			if err != nil {
				t.Fatal(err)
			}
			if tc.seeded {
				acceptRoutine(t, s, tc.id, "historical")
				if err := s.Routines.MarkNotified(tc.id, 1); err != nil {
					t.Fatal(err)
				}
			}
			body := tc.body
			var taskID string
			if tc.handoff {
				status := task.Review
				if tc.closed {
					status = task.Done
				}
				saved, err := s.Store.Create(task.NewTask{Area: "work", Title: "確認するPR", Status: status,
					Body: task.Body(task.AreaRoutineFormat, "依頼が届きました。", "依頼をレビューしてください。")})
				if err != nil {
					t.Fatal(err)
				}
				taskID, body = saved.ID, fmt.Sprintf(body, saved.ID)
			}
			routing, err := schedule.LoadRouting("", root, "work", "codex", "codex")
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "dispatch.json")
			open := func() *schedule.Dispatch {
				dispatch, err := schedule.OpenDispatch(state, routing)
				if err != nil {
					t.Fatal(err)
				}
				dispatch.Active = func(schedule.Job) bool { return !tc.stopped }
				dispatch.HandoffTaskOpen = func(id string) bool {
					tk, err := s.Store.Get(id)
					return err == nil && tk.ID == id && !tk.Status.Closed()
				}
				s.ScheduleDispatch = dispatch
				return dispatch
			}
			d := open()
			job := schedule.Job{ID: tc.id, Name: tc.id, Title: tc.name, Output: tc.output, ExplicitMatters: !tc.legacy,
				CheckResult: len(tc.scopes) > 0, CheckScopes: tc.scopes, Enabled: true}
			if job.Output == "" {
				job.Output = "matters"
			}
			if job.Output == "none" && tc.body != "" {
				job.Every = time.Hour
			}
			if tc.routed {
				job.ReplyTo = "discord:800"
			}
			s.Schedules = func() ([]schedule.Job, map[string]string) {
				displayJob := job
				displayJob.ExplicitMatters = tc.legacy // The running policy has since been changed.
				return []schedule.Job{displayJob}, nil
			}
			log := &schedule.RunLog{Root: root, State: filepath.Join(t.TempDir(), "pending.json"),
				Execution: func(id string) (schedule.Execution, bool) { return d.Get(id) },
				Review: func(id string, _ schedule.Run, report routine.Result) error {
					_, _, err := s.Routines.Accept(id, report)
					return err
				}}
			count := 2
			taskHandoffFailure := tc.output == "task" && tc.failure == "handoff_unverified"
			if taskHandoffFailure {
				count = 3
			}
			for n := range count {
				now := time.Date(2026, 11, 26, 9+n, 0, 0, 0, task.JST)
				job.Slot = now
				id := fmt.Sprintf("run-%d", n)
				if err := d.Enqueue(id, job, "", now); err != nil {
					t.Fatal(err)
				}
				if err := log.Begin(job, id, now); err != nil {
					t.Fatal(err)
				}
				report := func(payload string, want int) {
					t.Helper()
					out := httptest.NewRecorder()
					h.ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/api/routines/"+tc.id+"/runs/"+id, strings.NewReader(payload)))
					if out.Code != want {
						t.Fatalf("report: %d, want %d: %s", out.Code, want, out.Body)
					}
				}
				if tc.body != "" {
					report(body, 200)
					report(body, 200)
				}
				result := schedule.Result{Settled: true, Finished: now, Reply: "報告はありません。"}
				if tc.reply || tc.routed {
					result.Reply = "晴れです。"
				}
				if tc.stopped {
					if skipped, err := d.SkipLocalIfStopped(id, now); err != nil || !skipped {
						t.Fatalf("expected stopped run: %v", err)
					}
				} else if err := d.SyncLocal(id, "done", result); err != nil {
					t.Fatal(err)
				}
				d = open()
				if tc.body != "" {
					report(body, 200) // same result, after the run and a restart
				}
				if err := log.Settle(d.Result, now); err != nil {
					t.Fatal(err)
				}
				if err := log.Settle(d.Result, now); err != nil {
					t.Fatal(err)
				}
			}
			runs := schedule.ReadRuns(root, tc.id, "2026-11")
			if len(runs) != count || runs[0].Outcome != tc.outcome || runs[0].FailureKind != tc.failure {
				t.Fatalf("history=%+v", runs)
			}
			savedHandoff := tc.handoff && !tc.closed && !tc.overlap
			if savedHandoff && (len(runs[0].Handoffs) != 1 || runs[0].Handoffs[0].TaskID != taskID || len(runs[0].Matters) != 0) {
				t.Fatalf("handoff history=%+v", runs[0])
			}
			if tc.overlap && (len(runs[0].Handoffs) != 0 || len(runs[0].Matters) != 1) {
				t.Fatalf("explicit matter not preserved: %+v", runs[0])
			}
			if tc.failure == "handoff_unverified" && len(runs[0].UnverifiedHandoffs) != 1 {
				t.Fatalf("missing handoff failure: %+v", runs[0])
			}
			wantReceipts := tc.receipts
			if tc.seeded {
				wantReceipts++
			}
			snapshot, err := s.Routines.Snapshot(tc.id)
			if err != nil || len(snapshot.Receipts) != wantReceipts || tc.seeded && !snapshot.Receipts[0].Notified {
				t.Fatalf("receipts=%+v, want %d: %v", snapshot, wantReceipts, err)
			}
			code, raw, _ := get(t, h, "/api/routine-notices")
			var notices []notify.RoutineNotice
			if code != 200 || json.Unmarshal([]byte(raw), &notices) != nil || len(notices) != tc.notices {
				t.Fatalf("notices: %d %s, want %d", code, raw, tc.notices)
			}
			_, home, _ := get(t, h, "/")
			if strings.Contains(home, "未確認の結果が") != (wantReceipts > 0) {
				t.Fatal("home receipt count differs")
			}
			if savedHandoff {
				_, detail, _ := get(t, h, "/routines/"+tc.id)
				if !strings.Contains(detail, `href="/tasks/`+taskID+`"`) {
					t.Fatal("missing task link in the run history")
				}
			}
			deviceAlerts := map[string]schedule.DeviceStatus{"local-platform": {Status: schedule.WaitingAuth}}
			if trouble := schedule.Troubles(root, []schedule.Job{job}, deviceAlerts); len(trouble) < 1 ||
				trouble[len(trouble)-1].Key != "device:local-platform:auth" || ((tc.outcome == schedule.RunFailed || taskHandoffFailure) && len(trouble) != 2) {
				t.Fatalf("trouble=%+v", trouble)
			}
		})
	}
}

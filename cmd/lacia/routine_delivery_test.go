package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
)

func TestRoutineTextDeliveryKeepsReviewAndDoesNotResend(t *testing.T) {
	for _, test := range []struct {
		name, route, text, initial, wantDelivery string
		notified                                 bool
		sends                                    int
	}{
		{"sent", "discord:200", "結果です。", "", "sent", true, 1},
		{"failed", "discord:403", "結果です。", "", "failed", false, 1},
		{"silent", "discord:200", "報告はありません。", "", "silent", false, 0},
		{"unknown", "discord:200", "結果です。", "sending", "sending", false, 0},
		{"native", "", "結果です。", "", "", false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			queueDir := filepath.Join(root, "requests")
			q, err := request.Open(queueDir)
			if err != nil {
				t.Fatal(err)
			}
			r, _, err := q.Add(request.Request{ID: "sched-weather-2026-11-25-0600", Kind: request.System, Source: "予約「天気」", ReplyTo: test.route, Text: "確認します。"})
			if err != nil {
				t.Fatal(err)
			}
			r.Status, r.Reply, r.Finished = request.Done, test.text, time.Now()
			if test.initial != "" {
				q.SetDelivery(r.ID, test.initial)
				r.Delivery = test.initial
			}
			store := routine.NewStore(root)
			if _, _, err := store.Accept("S1001", routine.Result{RunID: r.ID, Summary: "読む結果です。"}); err != nil {
				t.Fatal(err)
			}
			bot := &recordingScheduledDiscord{}
			deliverDiscordResult(context.Background(), bot, q, r)
			saved, _ := q.Get(r.ID)
			if saved.Delivery != test.wantDelivery {
				t.Fatalf("delivery %q", saved.Delivery)
			}
			// Replay a sender callback and reconstruct both stores, as after a crash
			// between sending the text and recording the receipt.
			deliverDiscordResult(context.Background(), bot, q, saved)
			reopened, err := request.Open(queueDir)
			if err != nil {
				t.Fatal(err)
			}
			store = routine.NewStore(root)
			jobs := []schedule.Job{{ID: "S1001", Name: "S1001", ReplyTo: "discord:changed-after-this-run"}}
			for i := 0; i < 2; i++ {
				if err := syncRoutineDeliveries(store, jobs, reopened.Get); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := store.Snapshot("S1001")
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Pending) != 1 || snapshot.Receipts[0].Reviewed || snapshot.Receipts[0].Notified != test.notified {
				t.Fatalf("receipt changed review or delivery: %+v", snapshot)
			}
			notified := 0
			for _, event := range snapshot.History {
				if event.Type == "notified" {
					notified++
				}
			}
			want := 0
			if test.notified {
				want = 1
			}
			if notified != want {
				t.Fatalf("notified events=%d", notified)
			}
			if len(bot.scheduled) != test.sends || len(bot.ordinary) != 0 {
				t.Fatal("duplicate or different-route send", bot)
			}
		})
	}
}

func TestSyncExistingRoutineProblemsUsesLatestStoredFailure(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "routines", "S1042", "runs", "2026-11")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	history := `{"at":"2026-11-26T09:00:00+09:00","outcome":"failed","summary":"以前の理由です。"}` + "\n" +
		`{"at":"2026-11-27T09:00:00+09:00","outcome":"failed","summary":"3拠点ともPod一覧を取得できませんでした。","failure_kind":"check_unavailable","check_status":"unavailable","unverified":["cluster:site-a/auth","cluster:site-b/auth","cluster:site-c/auth"],"checks":[{"scope":"cluster:site-a/auth","status":"unverified","reason":"通信エラー"},{"scope":"cluster:site-b/auth","status":"unverified","reason":"通信エラー"},{"scope":"cluster:site-c/auth","status":"unverified","reason":"通信エラー"}],"id":"run-2026-11-27"}` + "\n"
	file := filepath.Join(dir, "runs.jsonl")
	if err := os.WriteFile(file, []byte(history), 0644); err != nil {
		t.Fatal(err)
	}
	jobs := []schedule.Job{{ID: "S1042", Name: "S1042", Enabled: true}}
	store := routine.NewStore(root)
	for range 2 {
		if err := syncExistingRoutineProblems(store, jobs); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot("S1042")
	if err != nil || len(snapshot.Pending) != 1 {
		t.Fatalf("review: %+v, %v", snapshot, err)
	}
	failure := snapshot.Pending[0]
	if failure.RunID != "run-2026-11-27" || len(failure.Unverified) != 3 ||
		failure.Unverified[0] != "cluster:site-a/auth：通信エラー" {
		t.Errorf("incorrect failure receipt: %+v", failure)
	}
	if original, err := os.ReadFile(file); err != nil || string(original) != history {
		t.Fatal("stored run history was changed", err)
	}
}

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/request"
)

type recordedSend struct{ channel, key, text string }
type recordingDiscord struct {
	sent []recordedSend
	err  error
}

func (b *recordingDiscord) Send(_ context.Context, channel, key, text string) error {
	b.sent = append(b.sent, recordedSend{channel, key, text})
	return b.err
}

func TestChildRepliesKeepOriginalDestination(t *testing.T) {
	for _, destination := range []string{"discord:channel-a", "discord:thread-a", ""} {
		t.Run(destination, func(t *testing.T) {
			dir := t.TempDir()
			q, err := request.Open(testDir(t, dir))
			if err != nil {
				t.Fatal(err)
			}
			parent, _, _ := q.Add(request.Request{ID: "parent-a", Text: "調べてください", ReplyTo: destination, TaskID: "F1014"})
			q.Add(request.Request{ID: "parent-b", Text: "別の依頼です", ReplyTo: "discord:channel-b"})
			child := codex.Child{ThreadID: "worker-a", TurnID: "turn-a", OriginTurnID: "turn-a", RequestID: parent.ID, Kind: "completed"}
			enqueueChild(q, child)
			enqueueChild(q, child)
			child.Kind = "shutdown"
			enqueueChild(q, child)
			q, err = request.Open(testDir(t, dir))
			if err != nil {
				t.Fatal(err)
			}
			bot := &recordingDiscord{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			count := 0
			q.Finished = func(r request.Request) {
				deliverDiscord(ctx, bot, q, r)
				// A stale callback cannot post a second time, even after success.
				deliverDiscord(ctx, bot, q, r)
				count++
				if count == 3 {
					cancel()
				}
			}
			q.Work(ctx, false, func(ctx context.Context, _ string) (string, string, error) {
				r := request.Current(ctx)
				if r.ID == "" {
					t.Fatal("missing trusted request context")
				}
				if r.ParentID != "" && (r.ReplyTo != destination || r.TaskID != "F1014") {
					t.Fatalf("child lost origin: %+v", r)
				}
				return "結果をお伝えします。", "turn-result", nil
			}, nil)
			want := 3
			if destination == "" {
				want = 1
			}
			if len(bot.sent) != want {
				t.Fatalf("sent %+v", bot.sent)
			}
			if destination != "" && (bot.sent[0].channel != destination[len("discord:"):] || bot.sent[2].channel != bot.sent[0].channel) {
				t.Fatalf("wrong route: %+v", bot.sent)
			}
		})
	}
}

func TestChildSuppressionMissingRouteAndUncertainSend(t *testing.T) {
	dir := t.TempDir()
	q, _ := request.Open(testDir(t, dir))
	parent, _, _ := q.Add(request.Request{ID: "parent-a", Text: "調べてください", ReplyTo: "discord:channel-a"})
	enqueueChild(q, codex.Child{ThreadID: "missing", Kind: "completed"})
	var missing request.Request
	for _, r := range q.Recent(0, "", true) {
		if r.Kind == request.System {
			missing = r
		}
	}
	if missing.Delivery != "missing_route" || missing.DeliveryLabel() == "" {
		t.Fatalf("missing destination hidden: %+v", missing)
	}
	bot := &recordingDiscord{}
	silent, _, _ := q.Add(request.Request{ID: "silent-a", Kind: request.System, ReplyTo: parent.ReplyTo, Text: "確認します"})
	silent.Status, silent.Reply = request.Done, "報告はありません。"
	deliverDiscord(context.Background(), bot, q, silent)
	if r, _ := q.Get(silent.ID); r.Delivery != "silent" {
		t.Fatalf("silent: %+v", r)
	}
	uncertain, _, _ := q.Add(request.Request{ID: "failed-a", ReplyTo: parent.ReplyTo, Text: "調べます"})
	uncertain.Status, uncertain.Reply = request.Done, "結果です"
	bot.err = errors.New("unknown POST outcome")
	deliverDiscord(context.Background(), bot, q, uncertain)
	deliverDiscord(context.Background(), bot, q, uncertain)
	if len(bot.sent) != 1 {
		t.Fatalf("blind retry: %+v", bot.sent)
	}
	if r, _ := q.Get(uncertain.ID); r.Delivery != "failed" {
		t.Fatalf("failed: %+v", r)
	}
	// A persisted pre-POST claim is also held across restart.
	claimed, _, _ := q.Add(request.Request{ID: "claimed-a", ReplyTo: parent.ReplyTo, Text: "調べます"})
	if ok, err := q.ClaimDelivery(claimed.ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	q, _ = request.Open(testDir(t, dir))
	claimed.Status, claimed.Reply = request.Done, "結果です"
	deliverDiscord(context.Background(), bot, q, claimed)
	if len(bot.sent) != 1 {
		t.Fatal("restart retried an uncertain send")
	}
	// Reopening must not return a sending record as a fresh delivery.
	// Finished is set by Work in production; check the claim directly here.
	if ok, err := q.ClaimDelivery(claimed.ID); ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(q.Undelivered(time.Time{})) != 0 {
		t.Fatal("an uncertain delivery was returned for retry")
	}
}

// Exercise the production queue, completion enqueue and delivery path without
// an LLM or a real Discord POST. A child remains active across the next turn.
func TestDelegatedWorkLeavesQueueFreeAndReturnsToOrigin(t *testing.T) {
	q, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	parent, _, err := q.Add(request.Request{ID: "long-work", TaskID: "T1001", ReplyTo: "discord:original", Text: "長い調査をしてください"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	bot := &recordingDiscord{}
	shortAnswered := make(chan struct{})
	finished := make(chan struct{})
	childDone := false
	q.Finished = func(r request.Request) {
		deliverDiscord(ctx, bot, q, r)
		switch r.ID {
		case parent.ID:
			if _, _, err := q.Add(request.Request{ID: "short-question", TaskID: "T1002", ReplyTo: "discord:other", Text: "短い質問です"}); err != nil {
				t.Error(err)
			}
		case "short-question":
			close(shortAnswered)
		default:
			if r.ParentID == parent.ID {
				close(finished)
				cancel()
			}
		}
	}
	go func() {
		select {
		case <-shortAnswered:
			enqueueChild(q, codex.Child{ThreadID: "long-worker", TurnID: "later-turn", OriginTurnID: "first-turn", RequestID: parent.ID, Kind: "completed"})
		case <-ctx.Done():
		}
	}()
	q.Work(ctx, false, func(ctx context.Context, _ string) (string, string, error) {
		r := request.Current(ctx)
		switch r.ID {
		case parent.ID:
			return "わたしが調べます。分かったらお知らせします。", "first-turn", nil
		case "short-question":
			if childDone {
				t.Error("short question waited for child completion")
			}
			return "短い質問への答えです。", "second-turn", nil
		default:
			childDone = true
			if r.ParentID != parent.ID || r.ReplyTo != parent.ReplyTo || r.TaskID != parent.TaskID {
				t.Errorf("completion lost original destination: %+v", r)
			}
			return "長い調査の結果です。", "third-turn", nil
		}
	}, nil)
	select {
	case <-finished:
	default:
		t.Fatal("completion did not return after the short answer")
	}
	if len(bot.sent) != 3 || bot.sent[0].channel != "original" || bot.sent[1].channel != "other" || bot.sent[2].channel != "original" {
		t.Fatalf("delivery order or destination: %+v", bot.sent)
	}
	for _, r := range q.Recent(0, "", true) {
		if r.Started.Before(r.Created) || r.Finished.Before(r.Started) {
			t.Fatalf("invalid existing queue timestamps: %+v", r)
		}
		t.Logf("%s wait=%s turn=%s", r.ID, r.Started.Sub(r.Created), r.Finished.Sub(r.Started))
	}
}

package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/imos/lacia/internal/request"
)

type recordingScheduledDiscord struct {
	ordinary  []string
	scheduled []string
}

func (b *recordingScheduledDiscord) Send(_ context.Context, channel, _, _ string) error {
	b.ordinary = append(b.ordinary, channel)
	return nil
}

func (b *recordingScheduledDiscord) SendScheduled(_ context.Context, channel, _, _ string) error {
	b.scheduled = append(b.scheduled, channel)
	if channel == "403" {
		return errors.New("fixture: Discord refuses this channel")
	}
	return nil
}

func TestDiscordScheduledResultsUseTheScheduleDestination(t *testing.T) {
	ctx := context.Background()
	q, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	bot := &recordingScheduledDiscord{}
	q.Finished = func(r request.Request) { deliverDiscordResult(ctx, bot, q, r) }
	remote := request.Request{
		ID: "sched-example-2026-11-25-0700", Kind: request.System, Source: "予約「例」",
		ReplyTo: "discord:200", Text: "確認します。", Status: request.Done, Reply: "結果です。", Finished: time.Now(),
	}
	if err := q.RecordExternal(remote); err != nil {
		t.Fatal(err)
	}
	repeated := remote
	repeated.ReplyTo = "discord:201"
	if err := q.RecordExternal(repeated); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		r    request.Request
		text string
		want string
	}{
		{r: request.Request{ID: "sched-daily-2026-11-25-0700", Kind: request.System, Source: "予約「例」", ReplyTo: "discord:300"}, text: "結果です。", want: "sent"},
		{r: request.Request{ID: "sched-denied-2026-11-25-0700", Kind: request.System, Source: "予約「例」", ReplyTo: "discord:403"}, text: "結果です。", want: "failed"},
		{r: request.Request{ID: "sched-silent-2026-11-25-0700", Kind: request.System, Source: "予約「例」", ReplyTo: "discord:500"}, text: "報告はありません。", want: "silent"},
		{r: request.Request{ID: "sched-no-target-2026-11-25-0700", Kind: request.System, Source: "予約「例」"}, text: "結果です。"},
		{r: request.Request{ID: "child-scheduled", Kind: request.System, Source: "Codex", ReplyTo: "discord:200", ParentID: remote.ID}, text: "結果です。", want: "sent"},
		{r: request.Request{ID: "child-bad-route", Kind: request.System, Source: "Codex", ReplyTo: "discord:300", ParentID: remote.ID}, text: "結果です。", want: "missing_route"},
		{r: request.Request{ID: "discord-user", Kind: request.User, Source: "Discord", ReplyTo: "discord:900"}, text: "結果です。", want: "sent"},
		{r: request.Request{ID: "peer-system", Kind: request.System, Source: "家庭用", ReplyTo: "discord:100"}, text: "結果です。", want: "sent"},
	} {
		test.r.Text = "確認します。"
		saved, _, err := q.Add(test.r)
		if err != nil {
			t.Fatal(err)
		}
		saved.Status, saved.Reply = request.Done, test.text
		deliverDiscordResult(ctx, bot, q, saved)
		deliverDiscordResult(ctx, bot, q, saved)
		stored, _ := q.Get(saved.ID)
		if stored.Delivery != test.want {
			t.Errorf("%s delivery = %q, want %q", stored.ID, stored.Delivery, test.want)
		}
	}
	if want := []string{"200", "300", "403", "200"}; !reflect.DeepEqual(bot.scheduled, want) {
		t.Errorf("scheduled = %v, want %v", bot.scheduled, want)
	}
	if want := []string{"900", "100"}; !reflect.DeepEqual(bot.ordinary, want) {
		t.Errorf("ordinary = %v, want %v", bot.ordinary, want)
	}
}

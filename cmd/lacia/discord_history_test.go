package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/discord"
	"github.com/imos/lacia/internal/request"
)

type historyFixture struct {
	calls [][2]string
}

func (b *historyFixture) PrepareImages(context.Context, []discord.ImageRef) ([]string, string, func()) {
	return nil, "", func() {}
}

func (b *historyFixture) History(_ context.Context, channel, original, before string, limit int) (discord.HistoryPage, error) {
	b.calls = append(b.calls, [2]string{channel, before})
	if original != "90001" || limit != 24 {
		return discord.HistoryPage{}, fmt.Errorf("unexpected history arguments")
	}
	if before == "90001" {
		return discord.HistoryPage{Messages: []discord.HistoryMessage{{Text: "1ページ目の証拠"}}, NextBefore: "90000"}, nil
	}
	return discord.HistoryPage{Messages: []discord.HistoryMessage{{Text: "2ページ目の証拠"}}}, nil
}

func discordHistoryRequest() request.Request {
	return request.Request{ID: "discord-90001", Author: "discord:300", Kind: request.User, Source: "Discord",
		DiscordSourceChannel: "250", ReplyTo: "discord:250", DiscordReviewReply: true, Status: request.Done}
}

func TestDiscordReviewLoadsHistoryOnlyForCurrentChannel(t *testing.T) {
	bot := &historyFixture{}
	r := discordHistoryRequest()
	calls := 0
	res, err := runDiscordTurn(context.Background(), bot, r, "元の投稿", func(_ context.Context, text, reqID string, _ []string) (codex.Result, error) {
		calls++
		if reqID != r.ID || !strings.Contains(text, "元の投稿") || !strings.Contains(text, "[discord-silent]") {
			t.Fatalf("request context: %s %s", reqID, text)
		}
		switch calls {
		case 1:
			if strings.Contains(text, "証拠") {
				t.Fatal("fetched history without a request")
			}
			return codex.Result{Reply: "[discord-history]"}, nil
		case 2:
			if !strings.Contains(text, "1ページ目の証拠") {
				t.Fatalf("history missing: %s", text)
			}
			return codex.Result{Reply: "[discord-history 90000]"}, nil
		case 3:
			if !strings.Contains(text, "2ページ目の証拠") {
				t.Fatalf("second history page missing: %s", text)
			}
			return codex.Result{Reply: "[discord-reply]\n分かりました。"}, nil
		}
		t.Fatal("too many model turns")
		return codex.Result{}, nil
	})
	r.Reply = res.Reply
	if err != nil || len(bot.calls) != 2 || bot.calls[0] != [2]string{"250", "90001"} || bot.calls[1] != [2]string{"250", "90000"} ||
		posted(r) != "分かりました。" {
		t.Fatalf("history=%v posted=%q err=%v", bot.calls, posted(r), err)
	}
}

func TestMergedDiscordPostsAskForAReplyOnlyWhenAddressed(t *testing.T) {
	r := discordHistoryRequest()
	r.From = "佐藤さん"
	r.Merged = []string{"discord-90002"}
	if p := request.Prompt(r); !strings.Contains(p, "返事が必要かを判断") || strings.Contains(p, "1度だけ返事をしてください") {
		t.Errorf("merged posts being reviewed asked for a reply: %s", p)
	}
	r.DiscordReviewReply = false
	if p := request.Prompt(r); !strings.Contains(p, "1度だけ返事をしてください") {
		t.Errorf("merged direct requests did not ask for one reply: %s", p)
	}
}

func TestDiscordReviewDoesNotPostIrrelevantOrInternalReplies(t *testing.T) {
	for _, reply := range []string{"[discord-silent]", "返信する必要を認めません。", "[discord-history 90001]", "[discord-reply]"} {
		r := discordHistoryRequest()
		r.Reply = reply
		if got := posted(r); got != "" {
			t.Fatalf("%q leaked: %q", reply, got)
		}
	}
	r := discordHistoryRequest()
	r.Status, r.Reply = request.Failed, "[discord-reply]\n送らないでください。"
	if got := posted(r); got != "" {
		t.Fatalf("failed ambient request leaked %q", got)
	}
	r.DiscordReviewReply, r.Status, r.Reply = false, request.Done, "直接のお返事です。"
	if got := posted(r); got != r.Reply {
		t.Fatalf("direct mention lost: %q", got)
	}
}

func TestDiscordHistoryCannotReadPastTheTrigger(t *testing.T) {
	bot := &historyFixture{}
	r := discordHistoryRequest()
	res, err := runDiscordTurn(context.Background(), bot, r, "元の投稿", func(context.Context, string, string, []string) (codex.Result, error) {
		if len(bot.calls) == 0 {
			return codex.Result{Reply: "[discord-history 99999]"}, nil
		}
		return codex.Result{Reply: "[discord-silent]"}, nil
	})
	if err != nil || len(bot.calls) != 1 || bot.calls[0] != [2]string{"250", "90001"} || res.Reply != "[discord-silent]" {
		t.Fatalf("history=%v result=%q err=%v", bot.calls, res.Reply, err)
	}
}

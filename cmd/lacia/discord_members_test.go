package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/discord"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

type recordingPauser struct{ paused []time.Duration }

func (p *recordingPauser) Pause(who string, d time.Duration) (discord.Member, time.Time, error) {
	p.paused = append(p.paused, d)
	return discord.Member{ID: who}, time.Now().Add(d), nil
}

func TestReceiveDiscordTellsWhoAndStopsAFlood(t *testing.T) {
	q, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	fam := members{DailyLimit: 3}
	child := discord.Member{ID: "7", Name: "Yさん", Role: discord.Child, About: "小学生"}
	now := time.Now() // the queue stamps requests with the real clock
	p := &recordingPauser{}
	send := func(id, text string) bool {
		created, err := receiveDiscord(p, q, fam, discord.Incoming{ID: id, Channel: "100", AuthorID: "7", Author: "さとう", Member: child, Text: text}, now)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	for i, id := range []string{"000001", "000002", "000003"} {
		if !send(id, "しつもん") {
			t.Fatalf("message %d was not taken", i+1)
		}
	}
	r, _ := q.Get("discord-000001")
	prompt := request.Prompt(r)
	for _, want := range []string{"Yさん（お子さん。DiscordのID 7）からの依頼です。", "Discord での表示名は「さとう」です。\n小学生\nこの方の発言は", "きょう3件目", "続けて届いた3件の発言"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if send("000004", "まだまだ") || len(p.paused) != 1 || p.paused[0] <= 0 || p.paused[0] > 24*time.Hour {
		t.Fatalf("the fourth message was not stopped until midnight: %v", p.paused)
	}
	notice, ok := q.Get("discord-limit-7-" + now.In(task.JST).Format("20060102"))
	if !ok || notice.Kind != request.System || notice.ReplyTo != "discord:100" || !strings.Contains(notice.Text, "きょう3件を超えました") {
		t.Fatalf("no notice about the limit: %+v", notice)
	}

	// An adult is never stopped, and someone without a role keeps the old form.
	adult := discord.Member{ID: "1", Name: "Xさん", Role: discord.Adult}
	for _, id := range []string{"000011", "000012", "000013", "000014", "000015"} {
		if created, err := receiveDiscord(p, q, fam, discord.Incoming{ID: id, Channel: "100", AuthorID: "1", Author: "x", Member: adult, Text: "予定"}, now); err != nil || !created {
			t.Fatalf("adult message %s: %v %v", id, created, err)
		}
	}
	if len(p.paused) != 1 {
		t.Fatal("an adult was paused")
	}
}

func TestStageFollowsTheSchoolYear(t *testing.T) {
	at := func(s string) time.Time {
		v, _ := time.ParseInLocation("2006-01-02", s, task.JST)
		return v
	}
	for _, c := range []struct{ born, now, want string }{
		{"2017-10-05", "2026-11-17", "9歳です。学年の目安は小学3年生"},
		{"2017-10-05", "2027-03-31", "9歳です。学年の目安は小学3年生"},
		{"2017-10-05", "2027-04-01", "9歳です。学年の目安は小学4年生"},
		{"2017-10-05", "2026-10-04", "8歳です。学年の目安は小学3年生"},
		{"2021-07-07", "2026-11-17", "5歳です。学年の目安は年中"},
		{"2021-07-07", "2028-04-01", "6歳です。学年の目安は小学1年生"},
		{"2018-04-01", "2026-11-17", "8歳です。学年の目安は小学3年生"}, // born on April 1: the year above
		{"2018-04-02", "2026-11-17", "8歳です。学年の目安は小学2年生"},
		{"2024-07-07", "2026-11-17", "2歳です。年少になる前の年齢です。"},
		{"1989-01-15", "2026-11-17", "37歳です"},
	} {
		if got := stage(c.born, at(c.now)); !strings.Contains(got, c.want) {
			t.Errorf("stage(%s, %s) = %q, want %q", c.born, c.now, got, c.want)
		}
	}
	if got := stage("", at("2026-11-17")); got != "" {
		t.Errorf("without a birthday: %q", got)
	}
}

func TestReceiveDiscordPreservesOriginalImagesThroughMergeAndRestart(t *testing.T) {
	dir := t.TempDir()
	q, err := request.Open(testDir(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"200001", "200002"} {
		in := discord.Incoming{ID: id, Channel: "900", AuthorID: "300", Author: "合成テスト", Text: "添付ファイル: label.png", Images: []discord.ImageRef{{Channel: "100", Message: id, Author: "300", Attachment: "400"}}}
		if _, err := receiveDiscord(&recordingPauser{}, q, members{}, in, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	q, err = request.Open(testDir(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := q.Get("discord-200001")
	if !ok || r.ReplyTo != "discord:900" || len(r.DiscordImages) != 2 || r.DiscordImages[0].Channel != "100" || r.DiscordImages[1].Message != "200002" {
		t.Fatalf("lost original attachment routes: %+v", r)
	}
	if !strings.Contains(request.Prompt(r), "label.png") {
		t.Fatal("attachment-only text lost")
	}
}

type syntheticImagePreparer struct {
	paths   []string
	remarks string
	cleaned bool
}

func (p *syntheticImagePreparer) PrepareImages(context.Context, []discord.ImageRef) ([]string, string, func()) {
	return p.paths, p.remarks, func() { p.cleaned = true }
}

func TestDiscordTurnPreservesTextAndCleansAfterFailure(t *testing.T) {
	for _, imagesFail := range []bool{false, true} {
		p := &syntheticImagePreparer{paths: []string{"/synthetic/label.png"}, remarks: "\n添付を渡しました。"}
		if imagesFail {
			p.paths = nil
			p.remarks = "\n添付画像を取得できませんでした。"
		}
		r := request.Request{ID: "discord-200001", DiscordImages: []discord.ImageRef{{Channel: "100"}}}
		expected := errors.New("synthetic Codex failure")
		_, err := runDiscordTurn(context.Background(), p, r, "本文は残してください。", func(_ context.Context, text, id string, images []string) (codex.Result, error) {
			if p.cleaned {
				t.Error("images cleaned before turn")
			}
			if id != r.ID || !strings.HasPrefix(text, "本文は残してください。") || !strings.Contains(text, p.remarks) || len(images) != len(p.paths) {
				t.Fatalf("lost text or image state: %s %s %v", id, text, images)
			}
			return codex.Result{}, expected
		})
		if !errors.Is(err, expected) || !p.cleaned {
			t.Fatal("failure or cleanup lost")
		}
	}
}

type historyImagePreparer struct {
	syntheticImagePreparer
	channel, message string
	err              error
	calls            int
}

func (p *historyImagePreparer) HistoryImages(_ context.Context, channel, message string) ([]discord.ImageRef, error) {
	p.channel, p.message = channel, message
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return []discord.ImageRef{{Channel: channel, Message: message}}, nil
}

func TestDiscordTurnLoadsHistoricalImagesFromSourceChannel(t *testing.T) {
	p := &historyImagePreparer{syntheticImagePreparer: syntheticImagePreparer{paths: []string{"/synthetic/image.png"}}}
	r := request.Request{
		ID: "discord-200001", Kind: request.User, Source: "Discord", Author: "discord:301",
		ReplyTo: "discord:901", DiscordSourceChannel: "100",
	}
	turns := 0
	result, err := runDiscordTurn(context.Background(), p, r, "元の依頼", func(_ context.Context, text, id string, images []string) (codex.Result, error) {
		turns++
		if id != r.ID {
			t.Fatalf("request ID changed: %q", id)
		}
		if turns == 1 {
			if len(images) != 0 || !strings.Contains(text, historyImageInstruction) {
				t.Fatal("missing history instructions on the first turn")
			}
			return codex.Result{Reply: "[discord-images 200000]"}, nil
		}
		if len(images) != 1 || p.cleaned || !strings.Contains(text, "200000") {
			t.Fatal("follow-up did not receive the historical image")
		}
		return codex.Result{Reply: "画像を確認しました。"}, nil
	})
	if err != nil || result.Reply != "画像を確認しました。" || turns != 2 || !p.cleaned || p.calls != 1 || p.channel != "100" || p.message != "200000" {
		t.Fatalf("wrong result or history scope: %#v %v %#v", result, err, p)
	}
}

func TestDiscordTurnHistoryDirectiveIsScopedAndBounded(t *testing.T) {
	for _, test := range []struct {
		name    string
		channel string
		kind    string
		fail    bool
		want    int
	}{
		{name: "request with no recorded source", kind: request.User},
		{name: "schedule", channel: "100", kind: request.System},
		{name: "history lookup failure", channel: "100", kind: request.User, fail: true, want: 1},
		{name: "bounded lookups", channel: "100", kind: request.User, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &historyImagePreparer{}
			if test.fail {
				p.err = errors.New("確認できませんでした。")
			}
			r := request.Request{ID: "discord-200001", Kind: test.kind, Source: "Discord", Author: "discord:301", ReplyTo: "discord:901", DiscordSourceChannel: test.channel}
			result, err := runDiscordTurn(context.Background(), p, r, "依頼", func(_ context.Context, _, _ string, _ []string) (codex.Result, error) {
				return codex.Result{Reply: "[discord-images 200000]"}, nil
			})
			if err != nil || strings.Contains(result.Reply, "[discord-images") || p.calls != test.want {
				t.Fatalf("unexpected history lookup: %#v %v, calls %d", result, err, p.calls)
			}
		})
	}
}

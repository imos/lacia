package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
)

// work lets the conversation handle everything that is queued, answering
// with what answer returns.
func work(t *testing.T, q *request.Queue, answer func(request.Request) (string, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.Work(ctx, false, func(ctx context.Context, _ string) (string, string, error) {
			reply, err := answer(request.Current(ctx))
			return reply, "turn", err
		}, nil)
		close(done)
	}()
	for deadline := time.Now().Add(5 * time.Second); q.Pending() > 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the queue did not empty")
		}
	}
	cancel()
	<-done
}

func TestTidyWiringLogsTellsAndLetsGoOnlyAfterAnAnswer(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(root, "memory"), 0o755)
	queue, err := request.Open(testDir(t, filepath.Join(state, "requests")))
	if err != nil {
		t.Fatal(err)
	}
	// As in serve: what finishes is delivered to Discord when it has a
	// destination there.
	bot := &recordingDiscord{}
	queue.Finished = func(r request.Request) { deliverDiscord(context.Background(), bot, queue, r) }
	log := daylog.Discord(testDir(t, root), "log/discord")
	saved := 0
	log.Changed = func() { saved++ }

	now := time.Date(2026, 11, 17, 3, 30, 0, 0, task.JST)
	said := time.Date(2026, 11, 16, 19, 0, 0, 0, task.JST)
	view := "<わたしのこと>\n# わたしのこと\n\nわたしのものの見方です。\n\n## わたし自身\n\n- 結論を先に聞きたい方だと考えています。\n\n## 家庭モードで\n\n- 短い返事が喜ばれるようです。\n</わたしのこと>\n<今夜変えたこと>\n最初の版です。\n</今夜変えたこと>\n"
	keeper := &self.Keeper{Records: testDir(t, root), Name: "memory/self.md", State: testDir(t, filepath.Join(state, "self")), Label: "家庭モード", ChatSide: true,
		Now: func() time.Time { return now },
		Think: func(_ context.Context, prompt string) (string, error) {
			if strings.Contains(prompt, "<覚えておく候補>") {
				return "<話題>\n木曜日のお迎え\n</話題>\n<覚えておく候補>\n- Xさんは、木曜日の夕方は迎えに行けないそうです。（11月16日 19:00、Xさん）\n</覚えておく候補>\n", nil
			}
			return view, nil
		},
		Exchanges: func(since time.Time) []self.Exchange {
			if !said.After(since) {
				return nil
			}
			return []self.Exchange{{At: said, From: "Xさん", Source: "Discord", Text: "木曜日の夕方は、迎えに行けません。", Reply: "承知しました。"}}
		}}
	// The server had already written the day's conversation.
	log.Add(daylog.Entry{Key: "message:1", At: said, Who: "Xさん", Place: "#lacia", Text: "木曜日の夕方は、迎えに行けません。"})
	wireTidy(keeper, queue, log, "log/discord")

	notices := func() []request.Request {
		var out []request.Request
		for _, r := range queue.Recent(0, "", true) {
			if strings.HasPrefix(r.ID, "tidy-") {
				out = append(out, r)
			}
		}
		return out
	}
	night := func(day int) {
		t.Helper()
		now = time.Date(2026, 11, day, 3, 30, 0, 0, task.JST)
		if _, err := keeper.Sleep(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}

	// The first night: the topic and the candidate join the day they are about.
	night(17)
	data, err := os.ReadFile(filepath.Join(root, "log", "discord", "2026-11", "2026-11-16.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "\n## 19:00 Xさん（#lacia） <!-- message:1 -->\n\n> 木曜日の夕方は、迎えに行けません。\n" +
		"\n## 23:59 夜の整理（その日の話題と、覚えておく候補） <!-- tidy:2026-11-16 -->\n\n> 話題: 木曜日のお迎え\n>\n> 覚えておく候補（提案です。元の記録を確かめ、必要な内容を memory/ に書きます）\n> - Xさんは、木曜日の夕方は迎えに行けないそうです。（11月16日 19:00、Xさん）\n" +
		"\n（サーバーが夜の整理で書き足しました。その日の記録の末尾と分かるように、見出しの時刻を23:59にしています。）\n"
	if !strings.HasSuffix(string(data), want) || saved != 2 {
		t.Errorf("saves asked = %d, log =\n%s", saved, data)
	}

	// The conversation is told by a notice that has nowhere to be posted.
	list := notices()
	if len(list) != 1 {
		t.Fatalf("notices = %d", len(list))
	}
	first := list[0]
	if first.Kind != request.System || first.Source != tidySource || first.ReplyTo != "" || first.Side || first.TaskID != "" ||
		!strings.Contains(first.Text, "`log/discord/2026-11/2026-11-16.md`") || !strings.Contains(first.Text, "木曜日の夕方は迎えに行けない") {
		t.Errorf("notice = %+v", first)
	}
	if prompt := request.Prompt(first); !strings.HasPrefix(prompt, "システムからのお知らせです。\n届いた経路: 夜の整理\n") || strings.Contains(prompt, "Discordの依頼ID") {
		t.Errorf("prompt =\n%s", prompt)
	}

	// An answer that failed, or whose outcome is unknown, lets nothing go:
	// the candidates are sent again the next night.
	for i, failure := range []error{errors.New("codex failed"), fmt.Errorf("%w: connection dropped", request.ErrUncertain)} {
		work(t, queue, func(request.Request) (string, error) { return "途中まで書いた返事です。", failure })
		last := notices()[0]
		if wantStatus := []string{request.Failed, request.Unknown}[i]; last.Status != wantStatus {
			t.Fatalf("status = %s", last.Status)
		}
		if got := keeper.PendingTidy(); len(got) != 1 || got[0].Day != "2026-11-16" {
			t.Fatalf("after %v, pending = %+v", failure, got)
		}
		night(18 + i)
		if list = notices(); len(list) != 2+i || !strings.Contains(list[0].Text, "同じ候補をもう1度送ります") || !strings.Contains(list[0].Text, "木曜日の夕方は迎えに行けない") {
			t.Fatalf("after %v: %d notices", failure, len(list))
		}
	}

	// An answer lets the candidates go. A request from Discord finishing in
	// between is still delivered there, and only that.
	queue.Add(request.Request{ID: "discord-123456", Source: "Discord", ReplyTo: "discord:100", Text: "ありがとう"})
	work(t, queue, func(r request.Request) (string, error) {
		if r.Source == tidySource {
			return "報告はありません。", nil
		}
		return "どういたしまして。", nil
	})
	if got := keeper.PendingTidy(); len(got) != 0 {
		t.Errorf("pending after the answer = %+v", got)
	}
	if len(bot.sent) != 1 || bot.sent[0].key != "discord-123456" || bot.sent[0].channel != "100" {
		t.Errorf("posted to Discord: %+v", bot.sent)
	}
	for _, r := range notices() {
		if r.Delivery != "" {
			t.Errorf("notice %s was handled as a delivery: %q", r.ID, r.Delivery)
		}
	}
	night(20)
	if len(notices()) != 3 {
		t.Errorf("a night with nothing pending sent a notice: %d", len(notices()))
	}
	// The log was written once; the later nights added nothing to that day.
	again, _ := os.ReadFile(filepath.Join(root, "log", "discord", "2026-11", "2026-11-16.md"))
	if string(again) != string(data) {
		t.Error("the day's log changed after the first night")
	}
}

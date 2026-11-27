package discord

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/daylog"
)

func jstTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.FixedZone("JST", 9*60*60))
	if err != nil {
		panic(err)
	}
	return t
}

func TestSentAt(t *testing.T) {
	// A synthetic ID with the timestamp from Discord's example.
	if got := SentAt("175928843960320001").UTC().Format(time.RFC3339); got != "2016-04-30T11:18:25Z" {
		t.Errorf("SentAt = %s", got)
	}
	if !SentAt("abc").IsZero() || !SentAt("11").IsZero() {
		t.Error("a value that is not a Discord ID got a time")
	}
}

func TestBotKeepsWhatWasSaidAndPosted(t *testing.T) {
	f := newFake(t)
	f.channels["100"] = Channel{ID: "100", Type: typeText, Name: "lacia"}
	f.channels["200"] = Channel{ID: "200", Type: typePublicThread, ParentID: "100", OwnerID: "1", Name: "旅行の相談"}
	f.history["100"] = []Message{{ID: "10"}}
	dir := t.TempDir()
	cfg := Config{Token: "the-token", GuildID: "G", Channels: []string{"100"}, State: filepath.Join(dir, "discord.json"),
		Members: []Member{{ID: "20", Name: "Xさん", Role: Adult}, {ID: "21", Name: "Yさん", Role: Child}},
		api:     f.server.URL + "/api", gateway: "ws" + strings.TrimPrefix(f.server.URL, "http") + "/gateway"}
	bot := New(cfg)
	bot.Log = daylog.Discord(filepath.Join(dir, "log"))
	received := make(chan Incoming, 16)
	bot.Receive = func(in Incoming) (bool, error) { received <- in; return true, nil }
	ready := make(chan struct{})
	bot.Ready = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the bot never became ready")
	}
	if _, _, err := bot.Pause("21", time.Hour); err != nil {
		t.Fatal(err)
	}
	// IDs made from a time, as Discord's are: 2026-11-18 08:15 and 08:16 JST.
	id := func(when string, n int) string {
		ms := jstTime(when).UnixMilli() - 1420070400000
		return strconv.FormatUint(uint64(ms)<<22+uint64(n), 10)
	}
	a, b, c, d := id("2026-11-18 08:15", 1), id("2026-11-18 08:16", 2), id("2026-11-18 08:17", 3), id("2026-11-18 08:18", 4)
	f.events <- message(a, "100", "20", "写真を送ります", `,"attachments":[{"filename":"a.png","url":"https://cdn.example/a.png?ex=1","content_type":"image/png","size":2048}]`)
	f.events <- message(a, "100", "20", "写真を送ります", "") // the same message again
	f.events <- message(b, "200", "20", "スレッドの続きです", "")
	f.events <- message(c, "100", "99", "家族ではない人", "")
	f.events <- message(d, "100", "21", "止められているあいだの発言", "")
	for i := 0; i < 2; i++ {
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatal("a message was not received")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(readLogOrEmpty(bot.Log, jstTime("2026-11-18 08:00")), "止められているあいだの発言") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := readLogOrEmpty(bot.Log, jstTime("2026-11-18 08:00"))
	for _, want := range []string{
		"\n## 08:15 Xさん（#lacia） <!-- message:" + a + " -->\n\n> 写真を送ります\n\n添付ファイル: a.png（image/png、2 KiB）\n",
		"\n## 08:16 Xさん（#lacia › 旅行の相談） <!-- message:" + b + " -->\n\n> スレッドの続きです\n",
		// Not taken as a request while paused, but still something that was said.
		"\n## 08:18 Yさん（#lacia） <!-- message:" + d + " -->\n\n> 止められているあいだの発言\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "cdn.example") || strings.Contains(got, "家族ではない人") || strings.Count(got, "\n## ") != 3 {
		t.Errorf("log =\n%s", got)
	}

	// What Lacia posts goes to the day it was posted on.
	if err := bot.Send(context.Background(), "200", "discord-"+b, "  承知しました。\n調べてみます。 "); err != nil {
		t.Fatal(err)
	}
	bot.Send(context.Background(), "200", "discord-"+b, "承知しました。\n調べてみます。") // a repeated delivery
	today := readLogOrEmpty(bot.Log, time.Now())
	if !strings.Contains(today, " レイシア（#lacia › 旅行の相談） <!-- post:discord-"+b+" -->\n\n> 承知しました。\n> 調べてみます。\n") || strings.Count(today, "post:discord-"+b) != 1 {
		t.Errorf("posts =\n%s", today)
	}
	// A place that cannot be looked up is written by its ID.
	bot.Send(context.Background(), "300", "sched-morning-1", "おはようございます。")
	if today = readLogOrEmpty(bot.Log, time.Now()); !strings.Contains(today, " レイシア（チャンネル 300） <!-- post:sched-morning-1 -->") {
		t.Errorf("posts =\n%s", today)
	}
}

func readLogOrEmpty(l *daylog.Log, when time.Time) string {
	data, _ := os.ReadFile(l.Path(when))
	return string(data)
}

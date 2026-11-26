package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
)

// snowflake makes a Discord ID for a time in Japan.
func snowflake(t *testing.T, when string, n int) string {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", when, task.JST)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatUint(uint64(at.UnixMilli()-1420070400000)<<22+uint64(n), 10)
}

func TestDiscordBackfillRebuildsPastDaysOnce(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "family", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	jst := func(s string) time.Time {
		at, _ := time.ParseInLocation("2006-01-02 15:04", s, task.JST)
		return at
	}
	a, b, c, d := snowflake(t, "2026-11-10 08:15", 1), snowflake(t, "2026-11-10 08:16", 2), snowflake(t, "2026-11-11 23:59", 3), snowflake(t, "2026-11-12 07:00", 4)
	requests := []request.Request{
		// Two messages folded into one request, with an attachment.
		{ID: "discord-" + a, Kind: request.User, Source: "Discord", Author: "discord:20", From: "Xさん（大人。DiscordのID 20）", ReplyTo: "discord:100",
			Merged: []string{"discord-" + b}, Text: "写真です\n添付ファイル: a.png https://cdn.example/a.png?ex=1\n\nもう1枚あります",
			Status: request.Done, Reply: "受け取りました。", Delivery: "sent", Created: jst("2026-11-10 08:15"), Finished: jst("2026-11-10 08:17")},
		// Answered after midnight: the reply belongs to the next day.
		// Saved before requests carried an author.
		{ID: "discord-" + c, Kind: request.User, Source: "Discord", From: "Yさん（DiscordのID 21）", ReplyTo: "discord:200",
			Text: "あしたは晴れる?", Status: request.Done, Reply: "晴れの予報です。", Delivery: "sent", Created: jst("2026-11-11 23:59"), Finished: jst("2026-11-12 00:01")},
		// A scheduled post, and a notice that stayed silent.
		{ID: "sched-morning-20261112", Kind: request.System, Source: "予約「朝」", ReplyTo: "discord:100", Text: "朝の投稿をしてください",
			Status: request.Done, Reply: "おはようございます。", Delivery: "sent", Created: jst("2026-11-12 06:30"), Finished: jst("2026-11-12 06:31")},
		{ID: "child-0123456789", Kind: request.System, ReplyTo: "discord:100", Text: "終わりました", Status: request.Done, Reply: "報告はありません。",
			Delivery: "silent", Created: jst("2026-11-12 06:40"), Finished: jst("2026-11-12 06:41")},
		// A request that failed: the apology is what was posted.
		{ID: "discord-" + d, Kind: request.User, Source: "Discord", Author: "discord:20", From: "Xさん（大人。DiscordのID 20）", ReplyTo: "discord:100",
			Text: "予定を教えて", Status: request.Failed, Delivery: "sent", Created: jst("2026-11-12 07:00"), Finished: jst("2026-11-12 07:02")},
		// Not from Discord, and not posted there.
		{ID: "r20261112-000000-abcdef", Kind: request.User, Source: "Web", Text: "仕事側からの伝言", Status: request.Done, Reply: "承知しました。",
			Created: jst("2026-11-12 09:00"), Finished: jst("2026-11-12 09:01")},
	}
	for _, r := range requests {
		data, _ := json.Marshal(r)
		path := filepath.Join(state, "requests", r.Created.UTC().Format("2006-01"), r.ID+".json")
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A request that is running in the live server must stay as it is.
	running := filepath.Join(state, "requests", "2026-11", "discord-999.json")
	os.WriteFile(running, []byte(`{"id":"discord-999","kind":"user","source":"Discord","text":"処理中","status":"running","reply_to":"discord:100","author":"discord:20","created":"2026-11-12T01:00:00Z"}`), 0o600)
	before, _ := os.ReadFile(running)
	// A note that the nightly rewrite has not digested yet.
	note, _ := json.Marshal(self.Note{At: jst("2026-11-12 07:30"), Text: "朝の返事は、短いほうがよさそうです。"})
	os.MkdirAll(filepath.Join(state, "self"), 0o700)
	os.WriteFile(filepath.Join(state, "self", "notes.jsonl"), append(note, '\n'), 0o600)

	// The server already wrote one of the messages itself.
	log := daylog.Discord(filepath.Join(root, "log", "discord"))
	log.Add(daylog.Entry{Key: "message:" + d, At: jst("2026-11-12 07:00"), Who: "Xさん", Place: "#lacia", Text: "予定を教えて"})

	args := []string{"--root", root, "--state", state, "--channels", "100=lacia"}
	if err := runDiscordBackfill(append([]string{"--dry-run"}, args...)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "log", "discord", "2026-11", "2026-11-10.md")); !os.IsNotExist(err) {
		t.Fatal("the dry run wrote a file")
	}
	if err := runDiscordBackfill(args); err != nil {
		t.Fatal(err)
	}
	read := func(day string) string {
		data, err := os.ReadFile(filepath.Join(root, "log", "discord", day[:7], day+".md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	first := read("2026-11-10")
	if !strings.Contains(first, "\n## 08:15 Xさん（#lacia） <!-- message:"+a+" message:"+b+" backfill -->\n\n> 写真です\n> 添付ファイル: a.png\n>\n> もう1枚あります\n\n（続けて届いた2件の発言を1件にまとめました。見出しには最初の発言の時刻を使っています。）\n") ||
		!strings.Contains(first, "\n## 08:17 レイシア（#lacia） <!-- post:discord-"+a+" backfill -->\n\n> 受け取りました。\n") || strings.Contains(first, "cdn.example") {
		t.Errorf("2026-11-10 =\n%s", first)
	}
	if got := read("2026-11-11"); !strings.Contains(got, "## 23:59 Yさん（チャンネル 200）") || strings.Contains(got, "晴れの予報です。") {
		t.Errorf("2026-11-11 =\n%s", got)
	}
	last := read("2026-11-12")
	order := []string{"## 07:00 Xさん（#lacia） <!-- message:" + d + " -->", "## 00:01 レイシア（チャンネル 200）", "> 晴れの予報です。", "## 06:31 レイシア（#lacia） <!-- post:sched-morning-20261112 backfill -->",
		"## 07:02 レイシア（#lacia） <!-- post:discord-" + d + " backfill -->\n\n> 申し訳ありません。", "## 07:30 レイシアの書き留め <!-- notice:", "> 朝の返事は、短いほうがよさそうです。"}
	at := 0
	for _, want := range order {
		i := strings.Index(last[at:], want)
		if i < 0 {
			t.Fatalf("missing or out of order: %q in\n%s", want, last)
		}
		at += i
	}
	if strings.Contains(last, "報告はありません") || strings.Contains(last, "仕事側からの伝言") || strings.Contains(last, "処理中") || strings.Count(last, "message:"+d) != 1 {
		t.Errorf("2026-11-12 =\n%s", last)
	}
	// A second run adds nothing.
	if err := runDiscordBackfill(args); err != nil {
		t.Fatal(err)
	}
	if read("2026-11-10") != first || read("2026-11-12") != last {
		t.Error("the second run changed the log")
	}
	if after, _ := os.ReadFile(running); string(after) != string(before) {
		t.Error("the backfill changed a saved request")
	}

	// The task side keeps the notes alone.
	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, "work", "tasks"), 0o755)
	for i := 0; i < 2; i++ {
		if err := runSelfBackfill([]string{"--root", work, "--state", state}); err != nil {
			t.Fatal(err)
		}
	}
	notes, err := os.ReadFile(filepath.Join(work, "log", "notices", "2026-11", "2026-11-12.md"))
	if err != nil || !strings.HasPrefix(string(notes), "# 2026-11-12（木）の書き留め\n") || strings.Count(string(notes), "\n## ") != 1 || strings.Contains(string(notes), "Xさん") {
		t.Errorf("notices = %s (%v)", notes, err)
	}
}

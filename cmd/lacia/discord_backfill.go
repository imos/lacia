package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/discord"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
)

// runDiscordBackfill rebuilds the log of past days from the requests the
// server has saved, and from the notes it has not digested yet. It only
// appends entries that are not in the log yet, so it can be run again, and
// run while the server is writing.
func runDiscordBackfill(args []string) error {
	return runBackfill("discord backfill", args, "log/discord", daylog.Discord, true)
}

// runSelfBackfill does the same for the notes alone, in a mode that keeps
// them in files of their own.
func runSelfBackfill(args []string) error {
	return runBackfill("self backfill", args, "log/notices", daylog.Notices, false)
}

func runBackfill(name string, args []string, defaultPath string, kind func(string) *daylog.Log, requests bool) error {
	fs := commandFlags(name)
	root := fs.String("root", "", "記録のリポジトリー")
	state := fs.String("state", "", "サーバーの --state と同じディレクトリー")
	path := fs.String("log", defaultPath, "ログを置くディレクトリー（記録のリポジトリーからの相対パス）")
	entryLimit := fs.Int("entry", daylog.DefaultEntryLimit, "1件の本文の上限（字）")
	dayLimit := fs.Int("day", daylog.DefaultDayLimit, "1日分のファイルの上限（バイト）")
	channels := new(string)
	if requests {
		channels = fs.String("channels", "", "チャンネルのIDと名前の対応（例: 123=lacia,456=旅行）。指定がなければ、IDで書きます")
	}
	dryRun := fs.Bool("dry-run", false, "ログに書き足す件数だけを確かめます")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *state == "" || fs.NArg() != 0 || !filepath.IsLocal(filepath.FromSlash(*path)) {
		return fmt.Errorf("使い方: lacia %s --state <サーバーの状態のディレクトリー> [--root DIR] [--log %s] [--dry-run]", name, defaultPath)
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, pair := range strings.Split(*channels, ",") {
		if id, name, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && id != "" && name != "" {
			names[id] = "#" + strings.TrimPrefix(name, "#")
		}
	}
	var entries []daylog.Entry
	read, unreadable, exchanges := 0, 0, 0
	if requests {
		// The files are read as they are. request.Open is not used: it would
		// mark requests that are running in the live server as interrupted.
		files, err := filepath.Glob(filepath.Join(*state, "requests", "*", "*.json"))
		if err != nil {
			return err
		}
		for _, file := range files {
			var r request.Request
			data, err := os.ReadFile(file)
			if err != nil || json.Unmarshal(data, &r) != nil || r.ID == "" {
				unreadable++
				continue
			}
			read++
			entries = append(entries, backfillEntries(r, names)...)
		}
		exchanges = len(entries)
	}
	// Notes are dropped once a night has digested them; what is still in
	// the file is all that can be recovered.
	if data, err := os.ReadFile(filepath.Join(*state, "self", "notes.jsonl")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			var n self.Note
			if json.Unmarshal([]byte(line), &n) == nil && n.Text != "" && !n.At.IsZero() {
				e := daylog.Notice(n.At, n.From, n.Task, n.Text)
				e.Backfilled = true
				entries = append(entries, e)
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
	log := kind(filepath.Join(dir, filepath.FromSlash(*path)))
	cfg, err := task.LoadConfig(dir)
	if err != nil {
		return err
	}
	log.Location = cfg.Location()
	log.EntryLimit, log.DayLimit, log.DryRun = *entryLimit, *dayLimit, *dryRun
	added, days := 0, map[string]bool{}
	for _, e := range entries {
		ok, err := log.Add(e)
		if err != nil {
			return err
		}
		if ok {
			added++
			days[log.Path(e.At)] = true
		}
	}
	verb := "書き足しました"
	if *dryRun {
		fmt.Print("ファイルは書き換えません（--dry-run）。")
		verb = "書き足せます"
	}
	if requests {
		fmt.Printf("依頼の記録を%d件読みました。Discord の発言とわたしの投稿は合わせて%d件です。まとめて受け付けた発言は1件と数えています。", read, exchanges)
	}
	fmt.Printf("残っている書き留めは%d件です。", len(entries)-exchanges)
	if added == 0 {
		fmt.Println("ログに書き足すものはありません。")
	} else {
		fmt.Printf("全%d件のうち、ログになかった%d件を%d日分のファイルに%s。\n", len(entries), added, len(days), verb)
	}
	if unreadable > 0 {
		fmt.Printf("読めなかった依頼の記録が%d件ありました。\n", unreadable)
	}
	return nil
}

var (
	discordFrom    = regexp.MustCompile(`^(.+)（[^（）]*DiscordのID [0-9]+）$`)
	attachmentLine = regexp.MustCompile(`(?m)^(添付ファイル: .+?) https?://\S+$`)
)

// backfillEntries turns one saved request into what the log would have
// kept: the message from someone on Discord, if the request was one, and what was
// posted to Discord in answer. Messages folded into a waiting request
// cannot be told apart any more, so they stay one entry.
func backfillEntries(r request.Request, names map[string]string) []daylog.Entry {
	channel, ok := strings.CutPrefix(r.ReplyTo, "discord:")
	if !ok || channel == "" {
		return nil
	}
	place := names[channel]
	if place == "" {
		place = "チャンネル " + channel
	}
	var out []daylog.Entry
	// Requests saved before 2026-11-16 carry no Author; the ID and the source
	// tell that they came from Discord. A notice such as "discord-limit-..."
	// has no message ID after the prefix.
	if id, ok := strings.CutPrefix(r.ID, "discord-"); ok && r.Kind == request.User && !discord.SentAt(id).IsZero() && (r.Source == "Discord" || strings.HasPrefix(r.Author, "discord:")) {
		e := daylog.Entry{Key: "message:" + id, At: discord.SentAt(id), Who: r.From, Place: place, Backfilled: true,
			// The URL of an attachment was part of the request; it has
			// expired by now, and the log does not keep it.
			Text: attachmentLine.ReplaceAllString(r.Text, "$1")}
		if e.At.IsZero() {
			e.At = r.Created
		}
		if m := discordFrom.FindStringSubmatch(r.From); m != nil {
			e.Who = m[1]
		}
		for _, merged := range r.Merged {
			e.More = append(e.More, "message:"+strings.TrimPrefix(merged, "discord-"))
		}
		if n := len(r.Merged); n > 0 {
			e.Remark = fmt.Sprintf("続けて届いた%d件の発言を1件にまとめました。見出しには最初の発言の時刻を使っています。", n+1)
		}
		out = append(out, e)
	}
	if text := posted(r); r.Delivery == "sent" && text != "" && !r.Finished.IsZero() {
		out = append(out, daylog.Entry{Key: "post:" + r.ID, At: r.Finished, Who: "レイシア", Place: place, Text: text, Backfilled: true})
	}
	return out
}

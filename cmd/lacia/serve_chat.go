package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/web"
)

// startChat connects the queue to the peer profile and to Discord.
func (s *serveRun) startChat() error {
	c, server, queue := s.c, s.server, s.queue
	// The logs kept in the repository (docs/design/discord.md).
	// Saving to Git follows within about a minute, as for any other
	// change there.
	for _, path := range []string{*c.str("discord-log"), *c.str("notice-log")} {
		if path != "" && !filepath.IsLocal(filepath.FromSlash(path)) {
			return errors.New("--discord-log と --notice-log には、リポジトリーの中の相対パスを指定してください")
		}
	}
	server.Requests = queue
	if s.peerURL != "" && !s.answers {
		told, err := s.state.MakeSub("told-peer", 0o700)
		if err != nil {
			return err
		}
		sent, err := request.OpenRecords(told)
		if err != nil {
			return err
		}
		server.Peer = &web.Peer{URL: s.peerURL, Sent: sent}
	}
	if s.answers {
		// Replies people on the chat should also see go to the first channel.
		server.Inbox = &web.Inbox{}
		if channel, _, _ := strings.Cut(os.Getenv("DISCORD_CHANNEL_IDS"), ","); strings.TrimSpace(channel) != "" && *c.on("discord") {
			server.Inbox.Discord = "discord:" + strings.TrimSpace(channel)
		}
	}
	// A reply to a message from the task side goes back there at once.
	replyToWork := func(r request.Request) {
		if s.answers {
			web.ReplyToPeer(s.ctx, nil, s.peerURL, queue, r)
		}
	}
	queue.Finished = replyToWork
	if !*c.on("discord") {
		return nil
	}
	return s.startDiscord(replyToWork)
}

func (s *serveRun) startDiscord(replyToWork func(request.Request)) error {
	server, queue := s.server, s.queue
	bot, err := newDiscord(s.state, "discord.json", s.family, queue, s.store.Location())
	if err != nil {
		return err
	}
	if discordLog := *s.c.str("discord-log"); discordLog != "" {
		s.chatLog = s.dayLog(discordLog, daylog.Discord)
		bot.Log = s.chatLog
	}
	s.imageBot = bot
	server.Members = bot
	server.Attachments = bot
	deliver := func(r request.Request) { deliverDiscordResult(s.ctx, bot, queue, r) }
	queue.Finished = func(r request.Request) { deliver(r); replyToWork(r) }
	go func() {
		// Replies that were ready when the server last stopped.
		for _, r := range queue.Undelivered(time.Now().Add(-2 * time.Hour)) {
			deliver(r)
		}
		if err := bot.Run(s.ctx); err != nil {
			slog.Error("discord stopped", "detail", "Discord から依頼を受け付ける処理が止まりました。", "error", err)
			s.stop()
		}
	}()
	return nil
}

// dayLog opens a log the records keep by day, at path relative to them.
func (s *serveRun) dayLog(path string, kind func(*disk.Dir, string) *daylog.Log) *daylog.Log {
	l := kind(s.store.Records(), filepath.FromSlash(path))
	l.Location = s.store.Location()
	l.EntryLimit, l.DayLimit = *s.c.num("discord-log-entry"), *s.c.num("discord-log-day")
	l.Changed = func() {
		if s.server.Changed != nil {
			s.server.Changed()
		}
	}
	return l
}

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
)

// startSelf keeps Lacia's own record, which a conversation rewrites every
// night.
func (s *serveRun) startSelf() error {
	c, dir, queue, server, store := s.c, s.dir, s.queue, s.server, s.store
	selfPath := *c.str("self")
	if selfPath == "" || selfPath == off {
		return nil
	}
	at := *c.str("self-at")
	if at == off {
		at = ""
	}
	state, err := s.state.MakeSub("self", 0o700)
	if err != nil {
		return err
	}
	keeper := &self.Keeper{Location: store.Location(), Records: store.Records(), Name: filepath.FromSlash(selfPath), State: state, Label: *c.str("label"), PeerLabel: s.peerLabel, ChatSide: s.answers, At: at, People: *c.str("people")}
	// The nightly launcher owns the permission and workspace flags.
	if len(strings.Fields(*c.str("self-claude"))) > 1 {
		return errors.New("--self-claude には Claude Code の実行ファイルだけを指定してください。引数は付けられません")
	}
	night := nightAgentOf(c)
	if err := night.checkChoice(); err != nil {
		return err
	}
	exchanges := *c.on("self-exchanges")
	keeper.OpenSession = night.open(*c.str("self-model"), dir, exchanges, keeper.State, s.stateDir, *c.str("config"), *c.str("discord-members"))
	slog.Info("self: nightly", "detail", nightSummary(c))
	if exchanges {
		keeper.Exchanges = func(since time.Time) []self.Exchange { return exchangesSince(queue, since) }
		keeper.RecordChanges = self.RecentRecords(dir, keeper.Path())
	}
	if server.Peer != nil {
		keeper.PeerCore, keeper.ShareCore = server.Peer.Core, server.Peer.ShareCore
	}
	// The conversation runs for days and reads the document only at
	// the start, so it is told whenever the document changed.
	keeper.Saved = func(night self.Night) {
		if server.Changed != nil {
			server.Changed()
		}
		text := "自分についての記録（`" + selfPath + "`）が新しくなりました。読み直して、これまでのやり取りを自分がどう受け止めてきたかを確かめてください。作業の許可については、これまでどおり決まりに従ってください。読み終えたら、「報告はありません。」とだけ答えてください。"
		if _, _, err := queue.Add(request.Request{ID: "self-" + night.At.UTC().Format("20060102-150405"), Kind: request.System, Source: "夜の書き直し", Text: text}); err != nil {
			slog.Error("self notice", "detail", "「わたしのこと」を書き直したことを窓口に知らせられませんでした。", "error", err)
		}
	}
	s.logNotices(keeper)
	if prompt := *c.str("self-tidy-prompt"); prompt != "" {
		text, err := os.ReadFile(prompt)
		if err != nil {
			return fmt.Errorf("--self-tidy-prompt の文書を読めませんでした: %w", err)
		}
		keeper.TidyPrompt = string(text)
	}
	if *c.on("self-tidy") && exchanges {
		wireTidy(keeper, queue, s.chatLog, *c.str("discord-log"))
	}
	if *c.on("self-improve") && store.KeepsTasks() {
		s.wireImprove(keeper)
	}
	server.Self = keeper
	go s.tickSelf(keeper)
	return nil
}

// logNotices keeps what the night notes down in a log by day. On the chat
// side the notes join the conversation's files.
func (s *serveRun) logNotices(keeper *self.Keeper) {
	noticeLog, discordLog := *s.c.str("notice-log"), *s.c.str("discord-log")
	if noticeLog == "" {
		return
	}
	notices := s.chatLog
	if notices == nil || path.Clean(noticeLog) != path.Clean(discordLog) {
		notices = s.dayLog(noticeLog, daylog.Notices)
	}
	keeper.Noted = func(n self.Note) {
		if _, err := notices.Add(daylog.Notice(n.At, n.From, n.Task, n.Text)); err != nil {
			slog.Error("notice log", "detail", "書き留めを日ごとのログに保存できませんでした。", "error", err)
		}
	}
}

func (s *serveRun) wireImprove(keeper *self.Keeper) {
	queue, store := s.queue, s.store
	keeper.ImproveTell = func(notice, text string) error {
		_, _, err := queue.Add(request.Request{ID: notice, Kind: request.System, Source: improveSource, Text: text})
		return err
	}
	keeper.TaskState = func(id string) string {
		if t, err := store.Get(id); err == nil {
			return t.Status.Label()
		}
		return ""
	}
}

func (s *serveRun) tickSelf(keeper *self.Keeper) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			if !s.draining() {
				s.restart.runNightly(func() { keeper.Tick(s.ctx, now) })
			}
		}
	}
}

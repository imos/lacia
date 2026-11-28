package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/gcal"
	"github.com/imos/lacia/internal/web"
)

// openCalendarSnapshot decides which file of events the server shows. The
// served copy is kept under persistent server state. The checkout path
// remains an input for a scheduled producer that has not been replaced.
func (s *serveRun) openCalendarSnapshot() {
	c, dir, server := s.c, s.dir, s.server
	if server.CalendarPath == "" {
		for _, candidate := range []string{"calendar/snapshot.json", "results/calendar-web-snapshot.md"} {
			if _, err := os.Stat(filepath.Join(dir, candidate)); err == nil {
				server.CalendarPath = filepath.Join(dir, candidate)
				break
			}
		}
	}
	calendarDirect := *c.on("calendar-direct")
	s.directCalendar = calendarDirect && (*c.str("site") != "" || *c.str("google-client") != "" || len(calendar.Scope) > 0)
	if server.CalendarPath == "" && !s.directCalendar &&
		!fileExists(filepath.Join(s.stateDir, "calendar", "snapshot.json")) &&
		!fileExists(filepath.Join(dir, "schedules", "calendar.md")) {
		return
	}
	s.snapshotInput = server.CalendarPath
	if s.snapshotInput == "" {
		s.snapshotInput = filepath.Join(dir, "calendar", "snapshot.json")
	}
	s.snapshotState = &calendar.SnapshotState{Dir: s.state, Name: "calendar/snapshot.json", LegacyOnly: !calendarDirect}
	if s.snapshotInput != s.snapshotState.Path() {
		// During the transition, prefer the last committed version over
		// an unfinished write from the older producer.
		if !fileExists(s.snapshotState.Path()) {
			if rel, err := filepath.Rel(dir, s.snapshotInput); err == nil {
				if seed, err := program.Git().Output(context.Background(), dir, "show", "HEAD:"+filepath.ToSlash(rel)); err == nil {
					if err := s.snapshotState.Publish(seed); err != nil {
						slog.Warn("calendar seed", "detail", "記録に保存してあった予定を、サーバーの最初の予定一覧に取り込めませんでした。", "error", err)
					}
				}
			}
		}
		if err := s.snapshotState.Import(s.snapshotInput); err != nil {
			slog.Warn("calendar snapshot import", "detail", "ファイルからサーバーに予定一覧を取り込めませんでした。", "error", err)
		}
	}
	server.CalendarPath = s.snapshotState.Path()
}

// startCalendarFetcher makes the server read Google Calendar itself, once
// every calendar is connected.
func (s *serveRun) startCalendarFetcher() error {
	if !s.directCalendar {
		return nil
	}
	c, server := s.c, s.server
	site, googleClient := *c.str("site"), *c.str("google-client")
	clientID, clientSecret := "", ""
	if googleClient != "" {
		var err error
		if clientID, clientSecret, err = gcal.LoadClient(googleClient); err != nil {
			return err
		}
	}
	// Google sends the user back to the public address, or, without
	// one, to this server on the loopback interface.
	_, port, _ := net.SplitHostPort(*c.str("addr"))
	gstore := &gcal.Store{Dir: s.state, Name: "secrets/gcal.json"}
	gclient := &gcal.Client{Endpoints: gcal.Google}
	// openCalendarSnapshot has made snapshotState, since the server reads
	// the calendars itself.
	fetcher := &gcal.Fetcher{Location: s.store.Location(), Store: gstore, Client: gclient, Path: server.CalendarPath, Publish: s.snapshotState.PublishDirect, Overrides: resolvePath(s.dir, filepath.FromSlash(*c.str("calendar-overrides")))}
	kick := make(chan struct{}, 1)
	server.Calendars = &web.Calendars{Refresh: fetcher.Once, Store: gstore, Client: gclient, Site: site, Local: "http://127.0.0.1:" + port,
		ClientID: clientID, ClientSecret: clientSecret, Write: *c.on("calendar-write"), Readers: func() map[[2]string]string {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return fetcher.Readers(ctx)
		}, Connected: func() {
			fetcher.Forget()
			select {
			case kick <- struct{}{}:
			default:
			}
		}}
	go fetcher.Run(s.ctx, 10*time.Minute, kick)
	s.fetcher = fetcher
	return nil
}

func (s *serveRun) calendarIsDirect() bool { return s.fetcher != nil && s.fetcher.Ready() }

// followCalendarSnapshot keeps importing the file an older producer writes,
// until the server reads the calendars itself.
func (s *serveRun) followCalendarSnapshot() {
	if s.snapshotState == nil || s.snapshotInput == s.snapshotState.Path() {
		return
	}
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if !s.calendarIsDirect() {
					if err := s.snapshotState.Import(s.snapshotInput); err != nil {
						slog.Warn("calendar snapshot import", "detail", "ファイルからサーバーに予定一覧を取り込めませんでした。", "error", err)
					}
				}
			}
		}
	}()
}

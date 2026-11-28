package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/gitsync"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/todo"
)

// openRecords reads the records' own settings and opens the tasks.
func (s *serveRun) openRecords() error {
	dir := s.dir
	cfg, err := task.LoadConfig(dir)
	if err != nil {
		return err
	}
	if err := cfg.RequireCurrentFormat(); err != nil {
		return err
	}
	if user := *s.c.str("user"); user != "" {
		os.Setenv("LACIA_USER", user)
	}
	if s.hideCalendarHolidays, err = calendar.HolidaySetting(*s.c.str("calendar-holidays")); err != nil {
		return err
	}
	task.SetOwner(dir)
	calendars := s.c.str("calendars")
	if *calendars == "" {
		// lacia init writes the list here, so that a copy of the template
		// needs no setting to read its owner's calendar.
		if path := filepath.Join(dir, calendarsFile); fileExists(path) {
			*calendars = path
		}
	}
	if *calendars != "" {
		if calendar.Scope, err = calendar.LoadScope(*calendars); err != nil {
			return fmt.Errorf("--calendars: %w", err)
		}
	}
	if err := calendar.SetHidden(cfg.CalendarHidden.Always, cfg.CalendarHidden.Week); err != nil {
		return err
	}
	s.store, err = task.Open(dir)
	return err
}

func (s *serveRun) startGitSync() {
	if !*s.c.on("git-sync") {
		return
	}
	server, store := s.server, s.store
	syncer := &gitsync.Syncer{Dir: s.dir, Prepare: func() error {
		if err := store.Reload(); err != nil {
			return err
		}
		if s.snapshotState == nil {
			guardCalendar(s.store.Records(), server.CalendarPath)
		}
		return todo.WriteLive(store, time.Now(), server.Waits, server.Requests, server.Handovers)
	}}
	server.Changed, server.Notice = syncer.Kick, syncer.Notice
	server.PublishedRevision = syncer.PublishedRevision
	s.syncing.Add(1)
	go func() {
		defer s.syncing.Done()
		syncer.Run(s.ctx)
	}()
}

// followRecords keeps the index in step with the disk, since Codex edits task
// files directly, and does what falls due as time passes.
func (s *serveRun) followRecords() {
	server, store, dir := s.server, s.store, s.dir
	var expired time.Time
	ticker := time.NewTicker(*s.c.span("rescan"))
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			if err := store.Reload(); err != nil {
				slog.Error("reload", "detail", "記録のリポジトリーからタスクを読み直せませんでした。", "error", err)
			}
			// Files kept outside Git go away some time after their task closed.
			if time.Since(expired) > time.Hour {
				expired = time.Now()
				for _, t := range store.ExpireFiles(expired) {
					slog.Info("files expired", "id", t.ID)
				}
			}
			// A wait past its deadline, or one nobody continued, goes to the user.
			server.TickWaits()
			// A task returns to review or starts when all its conditions are met.
			if woken := store.WakeWithRuns(now, func(name string, since time.Time) (task.RoutineCompletion, bool) {
				if job, ok := schedule.Resolve(dir, name); ok {
					name = job.Name
				}
				return schedule.FirstCompletion(dir, name, since, now, store.Location())
			}); len(woken) > 0 {
				for _, t := range woken {
					slog.Info("task woken", "id", t.ID, "on_ready", t.OnReady)
					if t.StartsWhenReady() {
						server.StartReady(t)
					}
				}
				if server.Changed != nil {
					server.Changed()
				}
			}
			// A waiting task that is looked at again and again gets its
			// next round, and a conversation is asked to look.
			if server.Recheck(now) && server.Changed != nil {
				server.Changed()
			}
		}
	}
}

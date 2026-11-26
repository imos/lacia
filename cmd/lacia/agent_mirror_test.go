package main

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/imos/lacia/internal/agent"
	"github.com/imos/lacia/internal/notify"
)

func TestMirrorOutagePlansOneDurableNotice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private", "notice-state.json")
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	poll := mirrorPoll{dir: dir, site: "https://lacia.example", statePath: path, state: notify.NewState()}
	notice := func(at time.Time) notify.Manifest {
		poll.fail(at)
		var m notify.Manifest
		poll.display(&m, at, true)
		return m
	}
	if m := notice(now); len(m.Notices) != 0 || m.Health.Sync != "failed" {
		t.Fatalf("initial failure = %+v", m)
	}
	if m := notice(now.Add(14 * time.Minute)); len(m.Notices) != 0 {
		t.Fatalf("early notice = %+v", m)
	}
	first := notice(now.Add(15 * time.Minute))
	if len(first.Notices) != 1 || first.Notices[0].Kind != "sync" {
		t.Fatalf("first due notice = %+v", first)
	}
	restarted, err := notify.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	poll.state = restarted
	later := notice(now.Add(10 * time.Hour))
	if !reflect.DeepEqual(later.Notices, first.Notices) {
		t.Fatalf("continued outage changed the notice: %+v vs %+v", later.Notices, first.Notices)
	}
	poll.clearFailure()
	m := notice(now.Add(11 * time.Hour))
	if len(m.Notices) != 0 {
		t.Fatalf("notified at the start of a new outage: %+v", m)
	}
	m = notice(now.Add(11*time.Hour + 15*time.Minute))
	if len(m.Notices) != 1 || m.Notices[0].ID == first.Notices[0].ID {
		t.Fatalf("new outage did not have an independent notice: %+v", m)
	}
}

func TestMirrorReviewDeduplicatesRecoveryAndReportsTasks(t *testing.T) {
	dir := t.TempDir()
	poll := mirrorPoll{dir: dir, state: notify.NewState()}
	result := agent.MirrorResult{Backup: dir, LocalOnly: []string{
		"work/tasks/2026-11/T1048-old/artifacts/report.md",
		"work/tasks/2026-11/T1045/log.md",
		"work/tasks/2026-11/T1045/notes.md",
	}}
	poll.addReview(result)
	poll.addReview(result)
	m := notify.Manifest{}
	poll.display(&m, time.Now(), false)
	want := []notify.SyncRecovery{{Path: dir, Tasks: []string{"T1045", "T1048"}}}
	if !reflect.DeepEqual(m.Health.SyncReview, want) {
		t.Fatalf("review = %+v", m.Health.SyncReview)
	}
}

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imos/lacia/internal/agent"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/notify"
)

const (
	mirrorLegacyPoll = 5 * time.Minute
	mirrorAlertAfter = 15 * time.Minute
)

var mirrorTaskPath = regexp.MustCompile(`(?:^|/)tasks/[0-9]{4}-[0-9]{2}/([A-Z][0-9]+)(?:-[^/]+)?(?:/|$)`)

type mirrorPoll struct {
	dir       string
	site      string
	files     *disk.Dir
	stateName string
	state     *notify.State

	attempted  time.Time
	advertised string
	healthy    bool
}

func (m *mirrorPoll) observe(ctx context.Context, now time.Time, revision string, readErr error) {
	if m.dir == "" || ctx.Err() != nil {
		return
	}
	if readErr != nil {
		m.fail(now)
		return
	}
	if m.healthy && !m.attempted.IsZero() {
		if revision != "" && revision == m.advertised {
			return
		}
		if revision == "" && now.Sub(m.attempted) < mirrorLegacyPoll {
			return
		}
	}
	m.attempted = now
	call, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	result, err := agent.SyncMirror(call, m.dir, revision)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("task mirror", "detail", "サーバーの記録を手元に同期できませんでした。", "error", err)
		m.fail(now)
		return
	}
	previous := m.copyState()
	if result.Backup != "" {
		if len(result.LocalOnly) != 0 {
			m.addReview(result)
		}
		slog.Info("task mirror recovered", "backup", result.Backup, "local_only_files", len(result.LocalOnly))
	}
	// An interrupted recovery may finish at the previously saved revision.
	// Keep polling until the current server revision has been checked too.
	current := result.Backup == "" || result.Revision == revision
	if current {
		m.clearFailure()
	}
	// Persist the review before acknowledging the recovery to Git. After a
	// crash, the same recovery can be observed again and is deduplicated by path.
	if err := notify.SaveState(m.files, m.stateName, m.state); err != nil {
		m.state.Sync = previous
		m.healthy = false
		slog.Warn("task mirror state", "detail", "手元の記録の同期状況を保存できませんでした。", "error", err)
		return
	}
	if result.Backup != "" {
		if err := agent.AcknowledgeMirrorRecovery(m.dir, result.Backup); err != nil {
			m.healthy = false
			slog.Warn("task mirror recovery receipt", "detail", "手元の記録を復旧しましたが、復旧が済んだことを記録できませんでした。", "error", err)
			return
		}
	}
	m.advertised = revision
	m.healthy = current
	slog.Info("task mirror checked", "revision", result.Revision, "current", current)
}

func (m *mirrorPoll) clearFailure() {
	if m.state.Sync != nil {
		m.state.Sync.FailureSince = time.Time{}
		m.state.Sync.Notice = nil
	}
}

func (m *mirrorPoll) addReview(result agent.MirrorResult) {
	if m.state.Sync == nil {
		m.state.Sync = &notify.SyncState{}
	}
	for _, old := range m.state.Sync.Review {
		if old.Path == result.Backup {
			return
		}
	}
	tasks := []string{}
	for _, path := range result.LocalOnly {
		if match := mirrorTaskPath.FindStringSubmatch(filepath.ToSlash(path)); match != nil && !slices.Contains(tasks, match[1]) {
			tasks = append(tasks, match[1])
		}
	}
	slices.Sort(tasks)
	m.state.Sync.Review = append(m.state.Sync.Review, notify.SyncRecovery{Path: result.Backup, Tasks: tasks})
}

func (m *mirrorPoll) fail(now time.Time) {
	if m.dir == "" {
		return
	}
	m.healthy = false
	previous := m.copyState()
	state := m.state.Sync
	if state.FailureSince.IsZero() {
		state.FailureSince = now
	}
	if now.Sub(state.FailureSince) >= mirrorAlertAfter && state.Notice == nil {
		state.Notice = &notify.Notice{
			ID:   "sync-" + strconv.FormatInt(state.FailureSince.UnixNano(), 10),
			Kind: "sync", Title: "Macのタスクの同期が止まっています",
			Body:    "サーバーの更新を15分以上取り込めていません。メニューバーから同期の状態を確かめられます。",
			URL:     strings.TrimRight(m.site, "/") + "/tasks",
			Fire:    float64(now.Add(2 * time.Second).Unix()),
			Expires: float64(now.Add(24 * time.Hour).Unix()),
		}
	}
	if err := notify.SaveState(m.files, m.stateName, m.state); err != nil {
		m.state.Sync = previous
		slog.Warn("task mirror state", "detail", "手元の記録の同期状況を保存できませんでした。", "error", err)
	}
}

// copyState returns the value that can be restored after a persistence error.
func (m *mirrorPoll) copyState() *notify.SyncState {
	previous := m.state.Sync
	m.state.Sync = &notify.SyncState{}
	if previous != nil {
		*m.state.Sync = *previous
	}
	return previous
}

func (m *mirrorPoll) display(manifest *notify.Manifest, now time.Time, enabled bool) {
	if m.dir == "" {
		manifest.Health.Sync = "disabled"
		return
	}
	manifest.Health.Sync = "checking"
	if m.healthy {
		manifest.Health.Sync = "ok"
	}
	if state := m.state.Sync; state != nil {
		if !state.FailureSince.IsZero() {
			manifest.Health.Sync = "failed"
		}
		if enabled && state.Notice != nil && state.Notice.Expires > float64(now.Unix()) {
			manifest.Notices = append(manifest.Notices, *state.Notice)
		}
		for _, review := range state.Review {
			if info, err := os.Stat(review.Path); err == nil && info.IsDir() {
				manifest.Health.SyncReview = append(manifest.Health.SyncReview, review)
			}
		}
	}
}

package calendar

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/imos/lacia/internal/effect/disk"
)

// SnapshotState holds the served snapshot outside the record repository.
// An older producer can still submit its snapshot through the old path.
type SnapshotState struct {
	Path       string
	LegacyOnly bool // the operator deliberately switched back to the legacy producer
	mu         sync.Mutex
}

// Publish validates and guards a candidate before replacing the served file.
// A rejected candidate leaves the last readable snapshot in place.
func (s *SnapshotState) Publish(candidate []byte) error {
	return s.publish(candidate, true)
}

// PublishDirect stores a snapshot read by this server. The fetcher has
// already decided which sources can be replaced and which must be retained.
func (s *SnapshotState) PublishDirect(candidate []byte) error {
	return s.publish(candidate, false)
}

func (s *SnapshotState) publish(candidate []byte, guardDrops bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := validSnapshot(candidate)
	if err != nil {
		return fmt.Errorf("予定のスナップショットを検証できません: %w", err)
	}
	if guardDrops && snapshot.DirectGoogle {
		return fmt.Errorf("従来の予定ファイルに、直接取得を示す設定は書けません")
	}
	previous, err := os.ReadFile(s.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if old, err := validSnapshot(previous); err == nil {
		if !snapshot.UpdatedAt.After(old.UpdatedAt) || guardDrops && old.DirectGoogle && !s.LegacyOnly {
			return nil
		}
		if guardDrops {
			candidate, _, err = Guard(previous, candidate)
			if err != nil {
				return err
			}
			if _, err := validSnapshot(candidate); err != nil {
				return fmt.Errorf("前回の予定を保ったスナップショットを読めません: %w", err)
			}
		}
	}
	return disk.WriteFile(s.Path, candidate, 0o600)
}

// Import reads a candidate when the older producer has written one.
func (s *SnapshotState) Import(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.Publish(data)
}

func validSnapshot(data []byte) (Snapshot, error) {
	if len(data) > maxSnapshot {
		return Snapshot{}, errors.New("予定のスナップショットが大きすぎます")
	}
	snapshot, err := Parse(data)
	if err == nil && (!snapshot.Available || snapshot.UpdatedAt.IsZero()) {
		err = errors.New("予定の取得時刻がありません")
	}
	return snapshot, err
}

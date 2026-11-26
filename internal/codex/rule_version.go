package codex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/imos/lacia/internal/rules"
)

type threadRecord struct {
	ThreadID       string         `json:"thread_id"`
	Rules          rules.Revision `json:"rules,omitempty"`
	RolloverNight  string         `json:"rollover_night,omitempty"`
	RetiredThreads []string       `json:"retired_threads,omitempty"`
}

func (b *Bridge) threadFile() string {
	return filepath.Join(b.cfg.StateDir, "thread.json")
}

func (b *Bridge) loadThread() (threadRecord, error) {
	var record threadRecord
	data, err := os.ReadFile(b.threadFile())
	if errors.Is(err, os.ErrNotExist) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	return record, json.Unmarshal(data, &record)
}

func (b *Bridge) saveThread(id string, version rules.Revision) error {
	record, err := b.loadThread()
	if err != nil {
		return err
	}
	record.ThreadID, record.Rules = id, version
	return b.writeThread(record)
}

func (b *Bridge) writeThread(record threadRecord) error {
	file, err := os.CreateTemp(b.cfg.StateDir, ".thread-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(record)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), b.threadFile())
}

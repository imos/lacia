package codex

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/imos/lacia/internal/rules"
)

type threadRecord struct {
	ThreadID string         `json:"thread_id"`
	Rules    rules.Revision `json:"rules,omitempty"`
}

func (b *Bridge) loadThread() (threadRecord, error) {
	var record threadRecord
	data, err := b.cfg.State.ReadFile("thread.json")
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
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return b.cfg.State.WriteFileNoSync("thread.json", append(data, '\n'), 0o600)
}

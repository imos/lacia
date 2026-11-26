package calendar

import (
	"encoding/json"
	"strings"
	"testing"
)

func snapshotJSON(work, family int) []byte {
	make := func(n int) []map[string]any {
		out := []map[string]any{}
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{"id": string(rune('a' + i))})
		}
		return out
	}
	data, _ := json.Marshal(map[string]any{"schema_version": 1, "sources": []map[string]any{
		{"account": "work@example.com", "calendar_id": "primary", "status": "ok", "complete": true, "updated_at": "2026-11-11T12:00:00Z", "events": make(work)},
		{"account": "home@example.com", "calendar_id": "family", "status": "ok", "complete": true, "updated_at": "2026-11-11T12:00:00Z", "events": make(family)},
	}})
	return data
}

func TestGuardKeepsEventsThatVanished(t *testing.T) {
	out, changed, err := Guard(snapshotJSON(24, 14), snapshotJSON(0, 12))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var got struct {
		Sources []struct {
			Status   string
			Complete bool
			Error    string
			Events   []any
		}
	}
	json.Unmarshal(out, &got)
	if len(got.Sources[0].Events) != 24 || got.Sources[0].Status != "partial" || got.Sources[0].Complete || !strings.Contains(got.Sources[0].Error, "24件から0件") {
		t.Errorf("work source = %+v", got.Sources[0])
	}
	if len(got.Sources[1].Events) != 12 || got.Sources[1].Status != "ok" {
		t.Errorf("a small change was not accepted: %+v", got.Sources[1])
	}
	if _, changed, _ := Guard(snapshotJSON(24, 14), snapshotJSON(13, 7)); changed {
		t.Error("losing less than half was rejected")
	}
	if _, changed, _ := Guard(snapshotJSON(3, 2), snapshotJSON(0, 0)); changed {
		t.Error("a nearly empty calendar becoming empty was rejected")
	}
	if _, changed, _ := Guard([]byte("not json"), snapshotJSON(0, 0)); changed {
		t.Error("a missing previous snapshot must not block the new one")
	}
}

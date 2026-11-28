package calendar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/imos/lacia/internal/effect/disk"
)

func TestSnapshotStateRetainsPreviousAcrossRestart(t *testing.T) {
	prevScope := Scope
	Scope = nil
	t.Cleanup(func() { Scope = prevScope })
	root := t.TempDir()
	state, err := disk.OpenTools(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	path := filepath.Join(root, "state", "calendar", "snapshot.json")
	legacy := filepath.Join(root, "input.json")
	candidate := func(stamp string, count int) []byte {
		events := []obj{}
		for i := range count {
			events = append(events, obj{
				"id": fmt.Sprint(i), "title": "会議",
				"start": obj{"dateTime": "2026-11-11T09:00:00+09:00"},
				"end":   obj{"dateTime": "2026-11-11T10:00:00+09:00"},
			})
		}
		snapshot := fixture(events...)
		snapshot["updated_at"], source(snapshot, 0)["updated_at"] = stamp, stamp
		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	check := func(count int, status string) {
		t.Helper()
		snapshot, err := Load(path)
		if err != nil || len(snapshot.Sources) != 1 {
			t.Fatalf("snapshot: %+v, %v", snapshot, err)
		}
		source := snapshot.Sources[0]
		if len(source.Events) != count || source.Status != status {
			t.Fatalf("count = %d, status = %q", len(source.Events), source.Status)
		}
	}
	first := candidate("2026-11-10T06:00:00+09:00", 8)
	if err := os.WriteFile(legacy, first, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &SnapshotState{Dir: state, Name: "calendar/snapshot.json"}
	if err := s.Import(legacy); err != nil {
		t.Fatal(err)
	}
	check(8, "ok")
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	s = &SnapshotState{Dir: state, Name: "calendar/snapshot.json"}
	if err := s.Import(legacy); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{[]byte("{"), []byte("{}"), []byte("null")} {
		if err := s.Publish(invalid); err == nil {
			t.Fatalf("%q was accepted", invalid)
		}
	}
	check(8, "ok")
	if err := s.Publish(candidate("2026-11-10T06:10:00+09:00", 1)); err != nil {
		t.Fatal(err)
	}
	check(8, "partial")
	if err := s.Publish(first); err != nil {
		t.Fatal(err)
	}
	check(8, "partial")
	if err := s.Publish(candidate("2026-11-10T06:20:00+09:00", 5)); err != nil {
		t.Fatal(err)
	}
	check(5, "ok")
	var before obj
	if err := json.Unmarshal(candidate("2026-11-10T06:30:00+09:00", 0), &before); err != nil {
		t.Fatal(err)
	}
	before["direct_google"] = true
	direct, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishDirect(direct); err != nil {
		t.Fatal(err)
	}
	check(0, "ok")
	legacyLater := candidate("2026-11-10T06:40:00+09:00", 3)
	if err := s.Publish(legacyLater); err != nil {
		t.Fatal(err)
	}
	check(0, "ok")
	s = &SnapshotState{Dir: state, Name: "calendar/snapshot.json", LegacyOnly: true}
	if err := s.Publish(legacyLater); err != nil {
		t.Fatal(err)
	}
	check(3, "ok")
}

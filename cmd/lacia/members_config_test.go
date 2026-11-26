package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFamilyWithoutATableIsRefused(t *testing.T) {
	// The binary carries no table of its own. Starting Discord without one
	// must fail, never fall back to accepting the whole channel.
	if _, err := loadMembers(""); err == nil || !strings.Contains(err.Error(), "--discord-members") {
		t.Fatalf("a missing table was accepted: %v", err)
	}
}

func TestFamilyExplicitOverrideAndFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "explicit members.json")
	data := `{"members":[{"id":"fixture-adult","name":"大人の検証用","role":"adult"},{"username":"fixture-child","name":"子どもの検証用","role":"child","birthday":"2017-04-02","about":"検証用です。"}],"daily_limit":17}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadMembers(path)
	if err != nil {
		t.Fatal(err)
	}
	var want members
	if err := json.Unmarshal([]byte(data), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("explicit table identities, roles or daily limit changed")
	}
	for _, invalid := range []string{`{}`, `{"members":[]}`, `{"members":[{"id":"1","name":"検証","role":"unknown"}]}`, `{"members":[{"id":"1","role":"adult"}]}`, `{"members":[{"name":"検証","role":"child"}]}`, `not JSON`} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadMembers(path); err == nil {
			t.Error("invalid override was accepted")
		}
	}
	if _, err := loadMembers(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing table was accepted")
	}
}

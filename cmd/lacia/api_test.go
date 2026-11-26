package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestReadOnlyCopyGoesThroughTheServer(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "work", "tasks"), 0o755)
	t.Setenv("LACIA_API", "")
	t.Setenv("LACIA_TASKS", root)
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\n\turl = x\n"), 0o644)
	if got := apiBase(); got != "" {
		t.Errorf("a writable repository: %q", got)
	}
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\n\turl = x\n\tpushurl = disabled://lacia-local-readonly\n"), 0o644)
	if got := apiBase(); got != localAPI {
		t.Errorf("a read-only copy: %q", got)
	}
	t.Setenv("LACIA_API", "http://example.test/")
	if got := apiBase(); got != "http://example.test" {
		t.Errorf("explicit: %q", got)
	}
}

func TestRestartRetriesRefusedDial(t *testing.T) {
	dials := 0
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	got, err := dialWhileRestarting(t.Context(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return nil, syscall.ECONNREFUSED
		}
		return client, nil
	}, "tcp", "fixture", time.Second, time.Millisecond)
	if err != nil || got != client || dials != 2 {
		t.Fatalf("connection=%v dials=%d err=%v", got, dials, err)
	}
	dials = 0
	_, err = dialWhileRestarting(t.Context(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, io.ErrUnexpectedEOF
	}, "tcp", "fixture", time.Second, time.Millisecond)
	if !errors.Is(err, io.ErrUnexpectedEOF) || dials != 1 {
		t.Fatalf("unexpected error was retried: dials=%d err=%v", dials, err)
	}
}

func TestRestartDoesNotReplayWrittenRequest(t *testing.T) {
	var writes atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer old.Close()
	if err := apiClient(old.URL).call(http.MethodPost, "/fixture", map[string]int{"n": 1}, nil); err == nil {
		t.Fatal("connection loss was not reported")
	}
	if got := writes.Load(); got != 1 {
		t.Fatalf("writes=%d, want one", got)
	}
}

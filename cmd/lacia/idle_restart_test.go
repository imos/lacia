package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestartWaitsForActiveWorkAndClosesHTTPAdmission(t *testing.T) {
	dir := t.TempDir()
	if err := requestIdleRestart(filepath.Join(dir, restartRequestFile)); err != nil {
		t.Fatal(err)
	}
	r := &idleRestart{path: filepath.Join(dir, restartRequestFile)}
	idle := func() bool { return false }
	if ok, err := r.stopIfIdle(time.Now(), func() bool { return true }); ok || err != nil || !r.pending() {
		t.Fatalf("consumed restart while working: %v %v", ok, err)
	}
	handler := r.handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Even with a known pending restart, an HTTP request in progress
		// can store its result and return before the server shuts down.
		ok, err := r.stopIfIdle(time.Now(), idle)
		if ok || err != nil || !r.pending() {
			t.Errorf("consumed restart inside a request: %v %v", ok, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	before := httptest.NewRecorder()
	handler.ServeHTTP(before, httptest.NewRequest(http.MethodPost, "/fixture", nil))
	if before.Code != http.StatusNoContent {
		t.Fatalf("running server rejected a result: %d", before.Code)
	}
	if ok, err := r.stopIfIdle(time.Now(), idle); ok || err != nil {
		t.Fatalf("ignored quiet period: %v %v", ok, err)
	}
	if ok, err := r.stopIfIdle(time.Now().Add(3*time.Second), idle); !ok || err != nil {
		t.Fatalf("server did not stop while idle: %v %v", ok, err)
	}
	if _, err := os.Stat(r.path); !os.IsNotExist(err) {
		t.Fatalf("restart request was not consumed: %v", err)
	}
	after := httptest.NewRecorder()
	handler.ServeHTTP(after, httptest.NewRequest(http.MethodPost, "/fixture", nil))
	if !r.pending() || after.Code != http.StatusServiceUnavailable {
		t.Fatalf("new work admitted during shutdown: %d", after.Code)
	}
}

func TestRestartWaitsForNightlyRun(t *testing.T) {
	dir := t.TempDir()
	r := &idleRestart{path: filepath.Join(dir, restartRequestFile)}
	idle := func() bool { return false }
	r.runNightly(func() {
		if err := requestIdleRestart(filepath.Join(dir, restartRequestFile)); err != nil {
			t.Fatal(err)
		}
		if ok, err := r.stopIfIdle(time.Now(), idle); ok || err != nil {
			t.Fatalf("interrupted nightly run: %v %v", ok, err)
		}
	})
	r.runNightly(func() { t.Error("started a nightly run during restart") })
	if ok, err := r.stopIfIdle(time.Now(), idle); !ok || err != nil {
		t.Fatalf("server did not stop after nightly run: %v %v", ok, err)
	}
}

// The command asks the running server, which keeps the request in its own
// state directory. A server from before the route says so, instead of
// leaving a request nobody watches for.
func TestRestartCommandAsksTheRunningServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, restartRequestFile)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/restart", func(w http.ResponseWriter, _ *http.Request) {
		if err := requestIdleRestart(path); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"scheduled":true}`))
	})
	current := httptest.NewServer(mux)
	defer current.Close()
	t.Setenv("LACIA_API", current.URL)
	for range 2 {
		// Asking twice keeps the one request.
		if err := runIdleRestart([]string{"--when-idle"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the server did not keep the request: %v", err)
	}
	if err := runIdleRestart([]string{"--when-idle", "--state", dir}); err == nil {
		t.Error("accepted a state directory from the caller")
	}

	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	t.Setenv("LACIA_API", old.URL)
	err := runIdleRestart([]string{"--when-idle"})
	if err == nil || !strings.Contains(err.Error(), "対応していない版") {
		t.Fatalf("an older server = %v", err)
	}
}

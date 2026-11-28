package program

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutput(t *testing.T) {
	dir := t.TempDir()
	out, err := Sh().Output(context.Background(), dir, "-c", "pwd")
	if err != nil {
		t.Fatal(err)
	}
	// The temporary directory may be reached through a symbolic link.
	if filepath.Base(strings.TrimSpace(string(out))) != filepath.Base(dir) {
		t.Errorf("ran in %q, want %q", out, dir)
	}

	_, err = Sh().Output(context.Background(), "", "-c", "echo broken >&2; exit 3")
	if code, ok := ExitCode(err); !ok || code != 3 {
		t.Errorf("exit code = %d, %v; want 3", code, ok)
	}
	if err == nil || !strings.Contains(err.Error(), "/bin/sh -c") || !strings.HasSuffix(err.Error(), ": broken") {
		t.Errorf("error = %v; want the command and its stderr", err)
	}
}

func TestFind(t *testing.T) {
	if _, err := Lacia("/nonexistent/lacia").Find(); err == nil {
		t.Error("found a program that does not exist")
	}
	if err := Lacia("/nonexistent/lacia").Replace(nil, nil); err == nil {
		t.Error("replaced this process with a program that does not exist")
	}
}

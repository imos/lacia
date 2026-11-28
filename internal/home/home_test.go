package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaces(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"HOME": dir}
	getenv := func(k string) string { return env[k] }
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logins := Logins(getenv)
	if logins.Path() != filepath.Join(dir, ".lacia", "config", "logins.json") || logins.Stale() || Moves(getenv) != nil {
		t.Fatalf("a fresh machine: %+v %v", logins, Moves(getenv))
	}

	// A machine of an older version keeps reading its files, and is told
	// how to bring them over, leaving a link behind.
	write(filepath.Join(dir, ".config", "lacia", "logins.json"))
	write(filepath.Join(dir, ".config", "lacia", "serve.conf"))
	if !Logins(getenv).Stale() || Settings(getenv).Path() != filepath.Join(dir, ".config", "lacia", "serve.conf") {
		t.Fatalf("old files: %+v", Settings(getenv))
	}
	moves := strings.Join(Moves(getenv), "\n")
	for _, want := range []string{"mv " + filepath.Join(dir, ".config", "lacia") + " " + filepath.Join(dir, ".lacia", "config"), "ln -s " + filepath.Join(dir, ".lacia", "config") + " " + filepath.Join(dir, ".config", "lacia"), "lacia.conf"} {
		if !strings.Contains(moves, want) {
			t.Errorf("moves lack %q:\n%s", want, moves)
		}
	}

	// Once the new place has the file, it is read, and nothing is told.
	write(filepath.Join(dir, ".lacia", "config", "logins.json"))
	write(filepath.Join(dir, ".lacia", "config", "lacia.conf"))
	if Logins(getenv).Stale() || Settings(getenv).Stale() || Moves(getenv) != nil {
		t.Errorf("after moving: %v", Moves(getenv))
	}

	// LACIA_CONFIG_DIR and LACIA_HOME name the places themselves.
	env["LACIA_CONFIG_DIR"], env["LACIA_HOME"] = "/etc/lacia", "/opt/lacia"
	if Logins(getenv).Path() != "/etc/lacia/logins.json" || State(getenv) != "/opt/lacia/state" {
		t.Errorf("named: %s %s", Logins(getenv).Path(), State(getenv))
	}
	if quote("/a b/it's") != `'/a b/it'\''s'` || quote("/plain/path") != "/plain/path" {
		t.Error("quote")
	}
}

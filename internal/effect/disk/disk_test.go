package disk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDir(t *testing.T) {
	path := t.TempDir()
	d, err := OpenRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if err := d.WriteFile("a/b/task.md", []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteFile("a/b/task.md", []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(path, "a/b/task.md")); got != "two" {
		t.Errorf("task.md = %q, want two", got)
	}
	if entries, _ := os.ReadDir(filepath.Join(path, "a/b")); len(entries) != 1 {
		t.Errorf("a/b has %d entries; the temporary file was left behind", len(entries))
	}

	for range 2 {
		if err := d.Append("log.jsonl", []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(t, filepath.Join(path, "log.jsonl")); got != "x\nx\n" {
		t.Errorf("log.jsonl = %q", got)
	}

	if err := d.CreateNew("log.jsonl", nil, 0o644); !errors.Is(err, fs.ErrExist) {
		t.Errorf("CreateNew over a file = %v, want fs.ErrExist", err)
	}
	if err := d.Rename("log.jsonl", "old/log.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("old/log.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("old/log.jsonl"); err != nil {
		t.Errorf("removing a missing file = %v, want nil", err)
	}
	if err := d.RemoveAll("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "a")); !os.IsNotExist(err) {
		t.Errorf("a remains after RemoveAll: %v", err)
	}
}

func TestDirStaysInside(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	os.Mkdir(outside, 0o755)
	tools := filepath.Join(base, "tools")
	d, err := OpenTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := os.Symlink(outside, filepath.Join(tools, "escape")); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../outside/x", "escape/x", filepath.Join(outside, "x")} {
		err := d.WriteFile(name, []byte("x"), 0o600)
		if err == nil {
			t.Errorf("wrote %s outside the tools directory", name)
		} else if !strings.Contains(err.Error(), tools) {
			t.Errorf("error %q does not name the tools directory", err)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("found %d files outside", len(entries))
	}
	if info, _ := os.Stat(tools); info.Mode().Perm() != 0o700 {
		t.Errorf("tools directory mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := InstallLaunchAgent("jp.example.lacia", []byte("<plist/>")); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library/LaunchAgents/jp.example.lacia.plist")
	if got := read(t, plist); got != "<plist/>" {
		t.Errorf("plist = %q", got)
	}
	if err := InstallLaunchAgent("../x", nil); err == nil {
		t.Error("accepted a label that names another directory")
	}
	if err := RemoveLaunchAgent("jp.example.lacia"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Errorf("plist remains: %v", err)
	}

	link := filepath.Join(home, ".local/bin/lacia")
	for _, target := range []string{"/old/lacia", "/new/lacia"} {
		if err := LinkCommand(target); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.Readlink(link); got != "/new/lacia" {
		t.Errorf("link points at %q", got)
	}
	if err := UnlinkCommand(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("link remains: %v", err)
	}

	os.WriteFile(link, []byte("someone's binary"), 0o755)
	if err := UnlinkCommand(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, link); got != "someone's binary" {
		t.Error("removed a file that was not a link")
	}
}

func TestDirOpenAppendAndMkdirAll(t *testing.T) {
	base := t.TempDir()
	tools := filepath.Join(base, "tools")
	d, err := OpenTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Path() != tools {
		t.Errorf("Path() = %q, want %q", d.Path(), tools)
	}

	if err := d.MkdirAll("state/runs"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(tools, "state/runs")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("state/runs = %v, %v; want a directory with mode 0700", info, err)
	}

	for _, line := range []string{"a\n", "b\n"} {
		f, err := d.OpenAppend("logs/server.log", 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(line)
		f.Close()
	}
	if got := read(t, filepath.Join(tools, "logs/server.log")); got != "a\nb\n" {
		t.Errorf("server.log = %q, want both lines", got)
	}

	if err := d.MkdirAll("../outside"); err == nil {
		t.Error("made a directory outside the tools directory")
	}
	if f, err := d.OpenAppend("../outside.log", 0o600); err == nil {
		f.Close()
		t.Error("opened a file outside the tools directory")
	}
	if entries, _ := os.ReadDir(base); len(entries) != 1 {
		t.Errorf("found %d entries beside the tools directory, want none", len(entries)-1)
	}
}

func TestPathHelpers(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state", "a.json")
	if err := WriteFileNoSync(state, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(state, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(base, "records", "calendar", "snapshot.json")
	if err := WriteFileNoSync(shared, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(base, "state", "log", "notes.jsonl")
	for range 2 {
		if err := Append(log, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if read(t, state) != "two" || read(t, log) != "x\nx\n" {
		t.Errorf("state = %q, log = %q", read(t, state), read(t, log))
	}
	for path, want := range map[string]os.FileMode{
		filepath.Dir(state): 0o700, filepath.Dir(log): 0o700, state: 0o600,
		filepath.Dir(shared): 0o755, shared: 0o644,
	} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v (%v), want %v", path, info.Mode().Perm(), err, want)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(state)); len(entries) != 2 {
		t.Errorf("state has %d entries; a temporary file was left behind", len(entries))
	}
	for _, path := range []string{state, state, filepath.Join(base, "missing", "a")} {
		if err := Remove(path); err != nil {
			t.Errorf("Remove(%s) = %v", path, err)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state remains: %v", err)
	}
}

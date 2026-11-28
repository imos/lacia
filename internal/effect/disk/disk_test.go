package disk

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if got, err := d.ReadFile("a/b/task.md"); string(got) != "two" || err != nil {
		t.Errorf("ReadFile = %q, %v, want two", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(path, "a/b")); len(entries) != 1 {
		t.Errorf("a/b has %d entries; the temporary file was left behind", len(entries))
	}

	for range 2 {
		if err := d.Append("log.jsonl", []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AppendSync("log.jsonl", []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(path, "log.jsonl")); got != "x\nx\ny\n" {
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
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600)
	if _, err := d.ReadFile("escape/secret"); err == nil {
		t.Error("read a file outside the tools directory through a link")
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

func TestDirModes(t *testing.T) {
	base := t.TempDir()
	dir, err := OpenTools(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.Mkdir(filepath.Join(base, "records"), 0o755); err != nil {
		t.Fatal(err)
	}
	records, err := OpenRecords(filepath.Join(base, "records"))
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	state := filepath.Join(base, "state", "a.json")
	if err := dir.WriteFileNoSync("a.json", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dir.WriteFile("a.json", []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(base, "records", "calendar", "snapshot.json")
	if err := records.WriteFileNoSync(filepath.Join("calendar", "snapshot.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(base, "state", "log", "notes.jsonl")
	for range 2 {
		if err := dir.Append(filepath.Join("log", "notes.jsonl"), []byte("x\n"), 0o600); err != nil {
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
	if entries, _ := os.ReadDir(filepath.Dir(state)); len(entries) != 2 { // a.json and log
		t.Errorf("state has %d entries; a temporary file was left behind", len(entries))
	}
	f, err := dir.OpenAppend("worker.log", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("y\n")
	f.Close()
	if err := dir.Rename("worker.log", "worker.log.old"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(base, "state", "worker.log.old")); got != "y\n" {
		t.Errorf("worker.log.old = %q", got)
	}
	if err := dir.CreateNew("restart", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dir.CreateNew("restart", nil, 0o600); !errors.Is(err, fs.ErrExist) {
		t.Errorf("CreateNew over a file = %v, want fs.ErrExist", err)
	}
	lock, err := dir.OpenLock("worker.lock", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if err := os.Symlink(state, filepath.Join(base, "state", "linked.lock")); err != nil {
		t.Fatal(err)
	}
	if lock, err := dir.OpenLock("linked.lock", 0o600); err == nil {
		lock.Close()
		t.Error("OpenLock opened a link")
	}
	if err := dir.RemoveFile("a.json"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.json", filepath.Join("missing", "a")} {
		if err := dir.RemoveFile(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("RemoveFile(%s) = %v, want fs.ErrNotExist", name, err)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state remains: %v", err)
	}
}

// Writers that make the same file at once must all get it: macOS may
// refuse one of them for a moment.
func TestAppendFromWritersAtOnce(t *testing.T) {
	d, err := OpenRecords(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var wg sync.WaitGroup
	for i := range 800 {
		wg.Go(func() {
			if err := d.Append(fmt.Sprintf("%d.log", i/8), []byte("x\n"), 0o644); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestSubAndClaims(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "records", "local"), 0o755)
	os.WriteFile(filepath.Join(base, "records", "memory.md"), []byte("secret"), 0o644)
	os.Symlink("../memory.md", filepath.Join(base, "records", "local", "link.pdf"))
	os.WriteFile(filepath.Join(base, "records", "local", "a.pdf"), []byte("%PDF-"), 0o644)
	records, err := OpenRecords(filepath.Join(base, "records"))
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	local, err := records.Sub("local")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if f, err := local.Open("a.pdf"); err != nil {
		t.Errorf("Open(a.pdf) = %v", err)
	} else {
		f.Close()
	}
	if _, err := local.Open("link.pdf"); err == nil {
		t.Error("a link that leads out of local/ was opened")
	}

	state, err := OpenTools(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	codex, err := state.MakeSub("codex/side", 0o700)
	if err != nil {
		t.Fatal(err)
	}
	defer codex.Close()
	if err := codex.WriteFile("thread.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(base, "state", "codex", "side", "thread.json")); got != "{}" {
		t.Errorf("thread = %q", got)
	}
	if _, err := state.MakeSub("../outside", 0o700); err == nil {
		t.Error("MakeSub made a directory outside")
	}
	if err := state.CreateNewSync("ledger/k.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.CreateNewSync("ledger/k.json", []byte("{}"), 0o600); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a second claim = %v, want fs.ErrExist", err)
	}
	if got := read(t, filepath.Join(base, "state", "ledger", "k.json")); got != "{}" {
		t.Errorf("claim = %q", got)
	}
}

func TestDirMkdirTemp(t *testing.T) {
	path := t.TempDir()
	d, err := OpenRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	name, err := d.MkdirTemp("local/rescue", "r-")
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.MkdirTemp("local/rescue", "r-")
	if err != nil || other == name || !strings.HasPrefix(name, filepath.Join("local/rescue", "r-")) {
		t.Errorf("MkdirTemp = %q and %q, %v", name, other, err)
	}
	for _, dir := range []string{"local/rescue", name} {
		if info, err := d.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want a directory of 0o700", dir, info, err)
		}
	}
	if err := d.RemoveFile(name); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RemoveFile of a removed directory = %v, want fs.ErrNotExist", err)
	}
	if err := d.Remove(name); err != nil {
		t.Errorf("Remove of a removed directory = %v, want nil", err)
	}
}

func TestTempDirNames(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	path, err := TempDir("lacia-test-")
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenTempDir(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	for _, name := range []string{"..", "a/b", "../" + filepath.Base(path)} {
		if d, err := OpenTempDir(name); err == nil {
			d.Close()
			t.Errorf("OpenTempDir(%q) opened", name)
		}
		if _, err := SocketDir(name); err == nil && name != ".." {
			t.Errorf("SocketDir(%q) made a directory", name)
		}
	}
	if err := RemoveTempDir(path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTempDir(filepath.Join(path, "inner")); err == nil {
		t.Error("RemoveTempDir removed a directory deeper than the temporary directory")
	}
}

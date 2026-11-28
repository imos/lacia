package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyConfig(t *testing.T) {
	write := func(text string) string {
		path := filepath.Join(t.TempDir(), "serve.conf")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	flags := func(args ...string) (*flag.FlagSet, *string, *bool, *int) {
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.String("config", "", "")
		addr, sync, limit := fs.String("addr", "127.0.0.1:8080", ""), fs.Bool("git-sync", false, ""), fs.Int("limit", 3, "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return fs, addr, sync, limit
	}

	// The file sets what the command line left alone; a name from a newer
	// version is skipped, so an older binary still starts.
	fs, addr, sync, limit := flags("--limit", "9")
	path := write("# 共通\n\naddr = 0.0.0.0:8080\ngit-sync=true\nlimit = 5\nfrom-the-future = yes\n")
	if _, err := applyConfig(fs, path, ""); err != nil {
		t.Fatal(err)
	}
	if *addr != "0.0.0.0:8080" || !*sync || *limit != 9 {
		t.Fatalf("addr=%q git-sync=%v limit=%d", *addr, *sync, *limit)
	}

	// A profile takes the common lines and its own on top; the others are
	// not applied, but can be looked up.
	shared := write("git-sync = true\nlimit = 5\n\n[work]\naddr = 0.0.0.0:8080\nurl = http://lacia-a\n\n[home]\naddr = 0.0.0.0:8081\nlimit = 7\nurl = http://lacia-b\n")
	fs, addr, sync, limit = flags()
	profiles, err := applyConfig(fs, shared, "home")
	if err != nil {
		t.Fatal(err)
	}
	if *addr != "0.0.0.0:8081" || !*sync || *limit != 7 || profiles["work"]["url"] != "http://lacia-a" || len(profiles) != 2 {
		t.Fatalf("addr=%q git-sync=%v limit=%d profiles=%v", *addr, *sync, *limit, profiles)
	}
	// Without a profile only the common lines hold.
	fs, addr, _, limit = flags()
	if _, err := applyConfig(fs, shared, ""); err != nil || *addr != "127.0.0.1:8080" || *limit != 5 {
		t.Fatalf("addr=%q limit=%d: %v", *addr, *limit, err)
	}

	for text, want := range map[string]string{
		"addr 0.0.0.0:8080\n":        "1行目",
		"git-sync = perhaps\n":       "git-sync",
		"\nconfig = other.conf":      "2行目",
		"[work]\nprofile = home\n":   "2行目",
		"[work]\n[work]\n":           "2行目",
		"[home]\naddr = 1.2.3.4:5\n": "プロファイル「work」",
	} {
		fs, _, _, _ := flags()
		profile := ""
		if strings.Contains(text, "[") {
			profile = "work"
		}
		if _, err := applyConfig(fs, write(text), profile); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", text, err)
		}
	}
	fs, _, _, _ = flags()
	if _, err := applyConfig(fs, filepath.Join(t.TempDir(), "missing.conf"), ""); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestWriteShims(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := writeShims(testDir(t, dir), "/opt/a place/lacia"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"pre-push":     "worker_push_denied",
		"lacia-browse": "playwright-cli",
	} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want) || strings.Contains(string(data), "@LACIA@") {
			t.Errorf("%s lacks %q", name, want)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o755 {
			t.Errorf("%s is not executable: %v", name, info.Mode())
		}
	}
	// A second start with another binary points the commands at it, and
	// leaves nothing else behind.
	if err := writeShims(testDir(t, dir), "/new/lacia"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "pre-push"))
	entries, _ := os.ReadDir(dir)
	if len(data) == 0 || len(entries) != 2 {
		t.Fatalf("after rewriting: %d files, %s", len(entries), data)
	}
}

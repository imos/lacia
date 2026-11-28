package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/effect/disk"
)

// runChild runs `lacia <args>` in a process of its own, with env, and
// returns what it printed and whether it failed.
func runChild(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(append([]string(nil), env...), serveChild+"="+string(encoded))
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// git runs git and returns what it printed.
func gitOut(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupEnv is serveEnv with its own home, whose path it also returns.
func setupEnv(t *testing.T) ([]string, string) {
	env := serveEnv(t)
	dir := t.TempDir()
	return append(env, "HOME="+dir), dir
}

// lacia setup makes the records in an empty repository, puts the binary and
// the settings in place, and later runs again without asking anything. A
// second machine takes the records as they are.
func TestSetupMakesRecords(t *testing.T) {
	env, home := setupEnv(t)
	remote := filepath.Join(t.TempDir(), "tanaka-lacia.git")
	gitOut(t, env, "", "init", "--quiet", "--bare", "-b", "main", remote)
	env = append(env, "LACIA_GOOGLE_CLIENT_ID=id.apps.example", "LACIA_GOOGLE_CLIENT_SECRET=not-a-secret")

	out, err := runChild(t, env, "setup", "--repo", remote, "--name", "田中さん", "--no-autostart", "--no-open")
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	records := filepath.Join(home, "tanaka-lacia")
	if got := gitOut(t, env, "", "--git-dir", remote, "show", "main:.lacia/config.json"); !strings.Contains(got, `"user": "田中さん"`) {
		t.Errorf("the pushed config.json:\n%s", got)
	}
	bin := filepath.Join(home, ".lacia", "bin", "lacia")
	if link, err := os.Readlink(filepath.Join(home, ".local", "bin", "lacia")); err != nil || link != bin {
		t.Errorf("~/.local/bin/lacia -> %q, %v", link, err)
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".lacia", "config", "lacia.conf"))
	if !strings.Contains(string(settings), "root = "+records+"\n") || !strings.Contains(string(settings), "git-sync = true") || !strings.Contains(string(settings), "google-client = ") {
		t.Errorf("lacia.conf:\n%s", settings)
	}
	if info, err := os.Stat(filepath.Join(home, ".lacia", "config", "google-client.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("google-client.json: %v %v", info, err)
	}
	if !strings.Contains(out, "夜の処理") {
		t.Errorf("setup did not tell of the night:\n%s", out)
	}

	// Again: nothing is asked, the binary is replaced and the old kept.
	if out, err := runChild(t, env, "setup", "--no-autostart", "--no-open"); err != nil {
		t.Fatalf("setup again: %v\n%s", err, out)
	}
	if _, err := os.Stat(bin + ".previous"); err != nil {
		t.Errorf("the previous binary was not kept: %v", err)
	}
	if out, err := runChild(t, env, "setup", "--repo", remote, "--no-autostart", "--no-open"); err == nil || !strings.Contains(out, "すでに") {
		t.Errorf("setup for other records on a set up machine: %v\n%s", err, out)
	}

	// A second machine clones the records and writes nothing.
	other, otherHome := setupEnv(t)
	if out, err := runChild(t, other, "setup", "--repo", remote, "--no-autostart", "--no-open"); err != nil {
		t.Fatalf("setup on a second machine: %v\n%s", err, out)
	}
	if n := gitOut(t, env, "", "--git-dir", remote, "rev-list", "--count", "main"); n != "1" {
		t.Errorf("the repository has %s commits, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(otherHome, "tanaka-lacia", "memory", "profile.md")); err != nil {
		t.Errorf("the second machine has no records: %v", err)
	}
}

// lacia setup refuses a repository with something else in it, and the
// places macOS guards.
func TestSetupRefuses(t *testing.T) {
	env, home := setupEnv(t)
	work := filepath.Join(t.TempDir(), "work")
	gitOut(t, env, "", "init", "--quiet", "-b", "main", work)
	os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644)
	gitOut(t, env, work, "add", ".")
	gitOut(t, env, work, "commit", "--quiet", "-m", "init")
	remote := filepath.Join(t.TempDir(), "other.git")
	gitOut(t, env, "", "clone", "--quiet", "--bare", work, remote)

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--repo", remote}, "レイシアの記録ではない"},
		{[]string{"--repo", remote, "--dir", filepath.Join(home, "Documents", "lacia")}, "Documents"},
		{[]string{"--repo", filepath.Join(t.TempDir(), "missing.git")}, "読めませんでした"},
		{[]string{"--repo", remote, "--dir", work}, "別のもの"},
	} {
		out, err := runChild(t, env, append([]string{"setup", "--name", "田中さん", "--no-autostart", "--no-open"}, c.args...)...)
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("setup %v: %v, want %q\n%s", c.args, err, c.want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "other")); err == nil {
		t.Error("the repository was cloned")
	}
}

func TestRepoName(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/tanaka/tanaka-lacia.git": "tanaka-lacia",
		"git@github.com:tanaka/tanaka-lacia.git":     "tanaka-lacia",
		"/tmp/records.git/":                          "records",
	} {
		if got := repoName(url); got != want {
			t.Errorf("repoName(%q) = %q, want %q", url, got, want)
		}
	}
}

// The definition of the server is a property list launchd can read, with
// the values escaped.
func TestServePlist(t *testing.T) {
	t.Setenv("PATH", "/a&b:/usr/bin")
	data := servePlist("jp.example.test", "/x/lacia", "/x/lacia.conf")
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	for {
		if _, err := decoder.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("not XML: %v\n%s", err, data)
		}
	}
	for _, want := range []string{"<string>jp.example.test</string>", "<string>/x/lacia</string><string>serve</string><string>--config</string><string>/x/lacia.conf</string>", "<string>/a&amp;b:/usr/bin</string>", "<key>LANG</key>"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("the definition lacks %s:\n%s", want, data)
		}
	}
}

// The log is set aside at start once it has grown, and what the server
// writes afterwards goes to a new file.
func TestRotateServeLog(t *testing.T) {
	tools, err := disk.OpenTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	if err := tools.WriteFile("logs/serve.log", []byte("old lines\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := tools.OpenAppend("logs/serve.log", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := rotateServeLog(tools, 1<<20, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tools.Path(), "logs", "serve.log.1")); err == nil {
		t.Fatal("a small log was set aside")
	}
	if err := rotateServeLog(tools, 4, out); err != nil {
		t.Fatal(err)
	}
	out.WriteString("new line\n")
	old, _ := os.ReadFile(filepath.Join(tools.Path(), "logs", "serve.log.1"))
	current, _ := os.ReadFile(filepath.Join(tools.Path(), "logs", "serve.log"))
	if string(old) != "old lines\n" || string(current) != "new line\n" {
		t.Errorf("serve.log.1 = %q, serve.log = %q", old, current)
	}
}

// The checks the server makes of its machine find an agent it cannot start.
func TestMachineIssues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	env := serveEnv(t)
	root := templateRecords(t, env)
	c, err := loadServeConfig([]string{"--root", root, "--codex", "/nowhere/codex", "--claude", fakeAgent(t)})
	if err != nil {
		t.Fatal(err)
	}
	issues := machineIssues(t.Context(), c, false)
	if len(issues) != 1 || !strings.Contains(issues[0].Problem, "/nowhere/codex") {
		t.Errorf("issues = %+v", issues)
	}
}

// Each part of the install guide an issue points to is a heading of the
// guide, by the anchor GitHub makes of it: lower case, with spaces made
// hyphens. Those headings carry no punctuation, which GitHub would drop.
func TestGuideSections(t *testing.T) {
	guide, err := os.ReadFile("../../docs/install-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]bool{}
	for _, line := range strings.Split(string(guide), "\n") {
		if heading, ok := strings.CutPrefix(strings.TrimLeft(line, "#"), " "); ok && strings.HasPrefix(line, "#") {
			anchors[strings.ReplaceAll(strings.ToLower(strings.Trim(heading, " ")), " ", "-")] = true
		}
	}
	for name, section := range guideSections {
		if !anchors[section] {
			t.Errorf("%s: the guide has no heading for #%s", name, section)
		}
	}
}

// The notifier is built from the source the binary carries, written into
// the tools directory for the build and taken away after it.
func TestNotifierSource(t *testing.T) {
	tools, err := disk.OpenTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	var seen []string
	err = withNotifierSource(tools, func(dir string) error {
		entries, err := os.ReadDir(dir)
		for _, e := range entries {
			seen = append(seen, e.Name())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, " "); got != "LaciaNotifier.swift brand.png manage.py render-icon.swift" {
		t.Errorf("source = %s", got)
	}
	if _, err := os.Stat(filepath.Join(tools.Path(), "tmp", "notifier")); err == nil {
		t.Error("the source was left behind")
	}
}

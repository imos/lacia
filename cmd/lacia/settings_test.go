package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var updateDocs = flag.Bool("update", false, "rewrite the tables of docs/design/settings.md from the settings table")

const settingsDoc = "../../docs/design/settings.md"

// The document's tables of the settings are the table of the code. With
// -update, the test writes them instead of comparing.
func TestSettingsDocumentFollowsTable(t *testing.T) {
	data, err := os.ReadFile(settingsDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, group := range settingGroups {
		heading := "\n### " + group + "\n"
		at := strings.Index(doc, heading)
		if at < 0 {
			t.Fatalf("the document has no section %q", group)
		}
		start := at + strings.Index(doc[at:], "\n| 名前 |") + 1
		end := start + strings.Index(doc[start:], "\n\n") + 1
		if want := settingsTable(group); doc[start:end] != want {
			if !*updateDocs {
				t.Errorf("the table of %s in %s differs from the code; run go test ./cmd/lacia -run TestSettingsDocumentFollowsTable -update\n--- document\n%s--- code\n%s", group, settingsDoc, doc[start:end], want)
			}
			doc = doc[:start] + want + doc[end:]
		}
	}
	line := recordsLine()
	start := strings.Index(doc, "記録のリポジトリーの `.lacia/serve.conf` で指定できるのは")
	end := start + strings.Index(doc[start:], "\n\n")
	if start < 0 || !strings.HasPrefix(doc[end+2:], "`") {
		t.Fatal("the document has no list of the settings the records may set")
	}
	end += 2 + strings.Index(doc[end+2:], "\n")
	if doc[start:end] != line {
		if !*updateDocs {
			t.Errorf("the list of the records' settings differs from the code:\n%s\n%s", doc[start:end], line)
		}
		doc = doc[:start] + line + doc[end:]
	}
	if *updateDocs && doc != string(data) {
		if err := os.WriteFile(settingsDoc, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func recordsLine() string {
	var names []string
	for _, s := range serveSettings {
		if s.where == records {
			names = append(names, "`"+s.name+"`")
		}
	}
	return "記録のリポジトリーの `.lacia/serve.conf` で指定できるのは、次の" + strconv.Itoa(len(names)) + "個です。\n\n" + strings.Join(names, "、")
}

// The table is sound: names are unique, defaults have a kind the flags
// take, and only what the records may set counts from the records.
func TestSettingsTable(t *testing.T) {
	seen := map[string]bool{}
	groups := map[string]bool{}
	for _, g := range settingGroups {
		groups[g] = true
	}
	for _, s := range serveSettings {
		if seen[s.name] || !groups[s.group] || s.help == "" {
			t.Errorf("%s: a repeated name, an unknown group, or no help", s.name)
		}
		seen[s.name] = true
		switch s.def.(type) {
		case nil, string, bool, int, time.Duration:
		default:
			t.Errorf("%s: a default of %T", s.name, s.def)
		}
		if s.path && s.where != records {
			t.Errorf("%s: only a records' setting counts from the records", s.name)
		}
	}
	// lacia serve -h lists every option with its help.
	o := serveOptions()
	var help bytes.Buffer
	o.fs.SetOutput(&help)
	o.fs.Usage()
	for _, s := range serveSettings {
		if !strings.Contains(help.String(), "-"+s.name+" ") && !strings.Contains(help.String(), "-"+s.name+"\n") {
			t.Errorf("lacia serve -h lacks %s", s.name)
		}
	}
}

// homeEnv makes a fresh home for the tool and the records, and clears what
// would point elsewhere.
func homeEnv(t *testing.T) (dir, root string) {
	dir = t.TempDir()
	t.Setenv("HOME", dir)
	for _, name := range []string{"LACIA_HOME", "LACIA_TASKS", "LACIA_CONFIG_DIR", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	root = filepath.Join(dir, "records")
	writeFile(t, filepath.Join(root, ".lacia", "config.json"), `{"format": 2}`+"\n")
	return dir, root
}

func TestLoadServeConfig(t *testing.T) {
	dir, root := homeEnv(t)
	// Without any file: the defaults of the tool's places and of the night.
	c, err := loadServeConfig([]string{"--root", root})
	if err != nil {
		t.Fatal(err)
	}
	if *c.str("state") != filepath.Join(dir, ".lacia", "state") || *c.str("self-at") != nightAt || *c.str("self") != "memory/self.md" || *c.str("config") != "" {
		t.Fatalf("state=%q self-at=%q self=%q config=%q", *c.str("state"), *c.str("self-at"), *c.str("self"), *c.str("config"))
	}

	// The machine's file of older versions is read while the new one is
	// missing; the records' file comes last and may set only its own part.
	old := writeFile(t, filepath.Join(dir, ".config", "lacia", "serve.conf"), "codex = codex\nlabel = 端末\nself-at = 05:00\n")
	writeFile(t, filepath.Join(root, ".lacia", "serve.conf"), "label = 記録\nself = off\naddr = 0.0.0.0:1\n")
	c, err = loadServeConfig([]string{"--root", root, "--addr", "127.0.0.1:2"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.stale || *c.str("config") != old || *c.str("label") != "端末" || *c.str("self") != off || *c.str("addr") != "127.0.0.1:2" {
		t.Fatalf("stale=%v config=%q label=%q self=%q addr=%q", c.stale, *c.str("config"), *c.str("label"), *c.str("self"), *c.str("addr"))
	}
	if c.origin["addr"] != "コマンドライン" || !strings.Contains(c.origin["label"], "serve.conf の2行目") || !strings.Contains(c.origin["self"], ".lacia/serve.conf の2行目") || c.origin["self-model"] != "" {
		t.Fatalf("origin = %v", c.origin)
	}
	if !strings.Contains(nightSummary(c), "self = off") {
		t.Errorf("night: %s", nightSummary(c))
	}

	// The new place wins once it is there.
	writeFile(t, filepath.Join(dir, ".lacia", "config", "lacia.conf"), "claude = claude\n[home]\ninbox = true\npeer = work\n[work]\nurl = http://work\n")
	os.Remove(filepath.Join(root, ".lacia", "serve.conf"))
	c, err = loadServeConfig([]string{"--root", root, "--profile", "home"})
	if err != nil {
		t.Fatal(err)
	}
	// The side that answers its peer rewrites first.
	if c.stale || *c.str("codex") != "" || *c.str("self-at") != nightAtInbox || c.origin["config"] != "既定の場所" {
		t.Fatalf("stale=%v codex=%q self-at=%q origin=%v", c.stale, *c.str("codex"), *c.str("self-at"), c.origin)
	}
	writeFile(t, filepath.Join(root, ".lacia", "serve.conf"), "self-at = off\n")
	if c, err = loadServeConfig([]string{"--root", root}); err != nil || *c.str("self-at") != off || !strings.Contains(nightSummary(c), "self-at = off") {
		t.Fatalf("self-at=%q: %v", *c.str("self-at"), err)
	}
}

func TestNightAgent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	if _, _, err := (nightAgent{}).find(); err == nil || !strings.Contains(err.Error(), filepath.Join(dir, ".local", "bin", "claude")) {
		t.Fatalf("without Claude Code: %v", err)
	}
	// Codex runs the night where Claude Code is not found.
	codex := writeFile(t, filepath.Join(dir, "codex-work"), "#!/bin/sh\n")
	os.Chmod(codex, 0o755)
	if kind, got, err := (nightAgent{codex: codex + " --yolo"}).find(); err != nil || kind != nightCodex || got != codex {
		t.Fatalf("Codex: %q %q: %v", kind, got, err)
	}
	// Where the installer puts it is found without PATH.
	installed := writeFile(t, filepath.Join(dir, ".local", "bin", "claude"), "#!/bin/sh\n")
	os.Chmod(installed, 0o755)
	if kind, got, err := (nightAgent{codex: codex}).find(); err != nil || kind != nightClaude || got != installed {
		t.Fatalf("found %q %q: %v", kind, got, err)
	}
	// A choice holds even when the other is there.
	if kind, _, _ := (nightAgent{choice: nightCodex, codex: codex}).find(); kind != nightCodex {
		t.Errorf("self-agent = codex ran on %q", kind)
	}
	if _, _, err := (nightAgent{choice: nightCodex}).find(); err == nil {
		t.Error("self-agent = codex fell back to Claude Code")
	}
	if _, _, err := (nightAgent{choice: "gpt"}).find(); err == nil || !strings.Contains(err.Error(), "self-agent") {
		t.Errorf("an unknown self-agent: %v", err)
	}
	// The conversations' command comes first, and a configured one alone counts.
	own := writeFile(t, filepath.Join(dir, "claude-work"), "#!/bin/sh\n")
	os.Chmod(own, 0o755)
	if _, got, _ := (nightAgent{conversation: own + " --verbose"}).find(); got != own {
		t.Errorf("conversation: %q", got)
	}
	if kind, _, err := (nightAgent{claude: filepath.Join(dir, "missing"), choice: nightClaude}).find(); err == nil {
		t.Errorf("a missing self-claude fell back to %s", kind)
	}
	// A night without either is recorded with why, and opens nothing.
	os.Remove(installed)
	if think, _, err := (nightAgent{claude: filepath.Join(dir, "missing")}).open("m", dir, false, dir)(t.Context()); err == nil || think != nil {
		t.Errorf("opened without Claude Code or Codex: %v", err)
	}
}

func TestNoticeOldPlaces(t *testing.T) {
	dir, _ := homeEnv(t)
	var out bytes.Buffer
	noticeOldPlaces([]string{"task", "list"}, &out, true)
	if out.Len() != 0 {
		t.Fatalf("told without old settings: %s", out.String())
	}
	writeFile(t, filepath.Join(dir, ".config", "lacia", "logins.json"), "{}")
	noticeOldPlaces([]string{"task", "list"}, &out, false)
	noticeOldPlaces([]string{"serve"}, &out, true)
	if out.Len() != 0 {
		t.Fatalf("told a conversation or the server: %s", out.String())
	}
	noticeOldPlaces([]string{"codex"}, &out, true)
	if !strings.Contains(out.String(), "mv "+filepath.Join(dir, ".config", "lacia")+" "+filepath.Join(dir, ".lacia", "config")) {
		t.Fatalf("notice: %s", out.String())
	}
	out.Reset()
	noticeOldPlaces([]string{"codex"}, &out, true)
	if out.Len() != 0 {
		t.Fatalf("told twice: %s", out.String())
	}
}

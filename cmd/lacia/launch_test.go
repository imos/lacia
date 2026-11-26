package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/chat"
	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/task"
)

func TestLaunchPrompt(t *testing.T) {
	t.Setenv("LACIA_USER", "")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.MkdirAll(filepath.Join(root, "memory"), 0o755)
	os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"user":"田中さん"}`), 0o644)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("`lacia rules` を実行して、決まりを読んでください。\n"), 0o644)
	os.WriteFile(filepath.Join(root, "memory", "now.md"), []byte("11月23日まで、出張で大阪にいます。\n"), 0o644)
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

	prompt, err := launchPrompt(root, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"田中さんの秘書として", root, "lacia rules list", "2026-11-22", "出張で大阪"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Error("a mark was left unfilled")
	}

	// The server can give a conversation a name that overrides the repository.
	t.Setenv("LACIA_USER", "鈴木さん")
	prompt, err = launchPrompt(root, now)
	if err != nil {
		t.Fatal(err)
	}
	core, err := rules.Load(root, rules.Core, "鈴木さん")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "鈴木さんの秘書として") || !strings.Contains(prompt, strings.TrimSpace(core)) {
		t.Error("the launch instructions and their embedded rules must both use LACIA_USER")
	}

	// A repository with rules of its own is pointed at them, and the
	// embedded rules are not laid on top.
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# 決まり\n\nここに全部書いてあります。\n"), 0o644)
	own, err := launchPrompt(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(own, "AGENTS.md を読み") || strings.Contains(own, "lacia rules list") || !strings.Contains(own, "出張で大阪") {
		t.Errorf("unexpected prompt for a repository with its own rules:\n%s", own)
	}
}

func TestRulesWithoutARepository(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("LACIA_TASKS", "")
	t.Setenv("LACIA_USER", "鈴木さん")
	root, owner := rulesFor()
	if root != "" || owner != "鈴木さん" {
		t.Errorf("rulesFor() = (%q, %q), want an empty root and the configured name", root, owner)
	}
}

func TestLaunchArgs(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "records")
	inside := launchArgs("claude", "指示", root, filepath.Join(root, "work"), []string{"--resume"}, false)
	if got := strings.Join(inside, " "); got != "--append-system-prompt 指示 --resume" {
		t.Errorf("claude inside the repository: %s", got)
	}
	outside := launchArgs("claude", "指示", root, filepath.Join(string(filepath.Separator), "code"), nil, false)
	if got := strings.Join(outside, " "); got != "--append-system-prompt 指示 --add-dir "+root {
		t.Errorf("claude outside the repository: %s", got)
	}
	codex := launchArgs("codex", "1行目\n\"2行目\"", root, root, []string{"resume", "--last"}, false)
	if got := strings.Join(codex, " "); got != `-c features.memories=false -c memories.generate_memories=false -c developer_instructions="1行目\n\"2行目\"" resume --last` {
		t.Errorf("codex: %s", got)
	}
}

// Run the real command dispatcher in a child, because a successful launch
// replaces the process with the selected agent. The mock prints just the
// memory settings and arguments that the agent received.
func TestLaunchMemoryProcess(t *testing.T) {
	const child = "LACIA_TEST_LAUNCH_MEMORY_CHILD"
	const claudeMemory = "CLAUDE_CODE_DISABLE_AUTO_MEMORY="
	if os.Getenv(child) == "1" {
		if err := run(os.Args[3:]); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	mock := filepath.Join(dir, "agent")
	script := `#!/bin/sh
printf '%s memory=%s\n' "$1" "${CLAUDE_CODE_DISABLE_AUTO_MEMORY-unset}"
shift
for arg do
  case "$arg" in
    features.memories=*|memories.*=*|--|--memory*) printf '%s\n' "$arg" ;;
  esac
done
`
	if err := os.WriteFile(mock, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	const config = `{"default":"work","logins":{
	"work":{"codex":["./agent","codex-work"],"claude":["./agent","claude-work"]},
	"personal":{"codex":["./agent","codex-personal"],"claude":["./agent","claude-personal"]}
}}`
	if err := os.WriteFile(filepath.Join(dir, login.ListFile), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, inherited, want string
		args                  []string
		invalid               bool
	}{
		{"codex off by default", "", "codex-work memory=unset\nfeatures.memories=false\nmemories.generate_memories=false\n",
			[]string{"codex"}, false},
		{"memory then login", "", "codex-personal memory=unset\nfeatures.memories=true\nmemories.generate_memories=true\nmemories.use_memories=true\n--\n--memory=false\n",
			[]string{"codex", "--memory", "--login", "personal", "--", "--memory=false"}, false},
		{"codex native override", "", "codex-personal memory=unset\nfeatures.memories=true\nmemories.generate_memories=true\nmemories.use_memories=true\nmemories.use_memories=false\n",
			[]string{"codex-personal", "--memory=true", "-c", "memories.use_memories=false"}, false},
		{"codex explicit off", "", "codex-personal memory=unset\nfeatures.memories=false\nmemories.generate_memories=false\n",
			[]string{"codex", "--login=personal", "--memory=false"}, false},
		{"claude off by default", "", "claude-work memory=1\n", []string{"claude"}, false},
		{"claude inherits override", "0", "claude-work memory=0\n", []string{"claude"}, false},
		{"login then memory", "1", "claude-personal memory=0\n",
			[]string{"claude", "--login", "personal", "--memory"}, false},
		{"claude explicit off", "0", "claude-personal memory=1\n",
			[]string{"claude-personal", "--memory=false"}, false},
		{"claude passes arguments after separator", "", "claude-personal memory=1\n--\n--memory\n",
			[]string{"claude-personal", "--", "--memory"}, false},
		{"malformed boolean", "", "--memory には true か false を指定してください",
			[]string{"claude", "--memory=maybe"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestLaunchMemoryProcess$", "--"}, tc.args...)
			cmd := exec.Command(self, args...)
			cmd.Dir = dir
			cmd.Env = slices.DeleteFunc(cmd.Environ(), func(entry string) bool {
				return strings.HasPrefix(entry, claudeMemory)
			})
			cmd.Env = append(cmd.Env, child+"=1", "LACIA_TASKS="+dir, "LACIA_CONFIG_DIR="+dir)
			if tc.inherited != "" {
				cmd.Env = append(cmd.Env, claudeMemory+tc.inherited)
			}
			if tc.invalid {
				got, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(got), tc.want) {
					t.Fatalf("got %q, %v, want an error saying %q", got, err, tc.want)
				}
				return
			}
			got, err := cmd.Output()
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestRecordsConfigIsLimited(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "serve.conf")
	os.WriteFile(conf, []byte("label = 田中さんのレイシア\naddr = 0.0.0.0:80\ncodex = /tmp/anything\ngoogle-client = oauth/client.json\ncalendar-write = true\n"), 0o644)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	label := fs.String("label", "仕事モード", "")
	addr := fs.String("addr", "127.0.0.1:10148", "")
	codex := fs.String("codex", "", "")
	client := fs.String("google-client", "", "")
	write := fs.Bool("calendar-write", false, "")
	if err := fs.Parse([]string{"--calendar-write=false"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (configSource{path: conf, allow: recordsSettings, base: root}).apply(fs); err != nil {
		t.Fatal(err)
	}
	if *label != "田中さんのレイシア" {
		t.Errorf("label = %q", *label)
	}
	if *addr != "127.0.0.1:10148" || *codex != "" {
		t.Errorf("the repository's file changed what it may not: addr=%q codex=%q", *addr, *codex)
	}
	if *client != filepath.Join(root, "oauth", "client.json") {
		t.Errorf("google-client = %q", *client)
	}
	if *write {
		t.Error("the command line must win over the file")
	}
}

func TestServerNote(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "work", "tasks"), 0o755)
	t.Setenv("LACIA_API", "")
	if got := apiForRoot(root); got != "" {
		t.Fatalf("no server, yet %q", got)
	}
	os.WriteFile(filepath.Join(root, ".git", "lacia-server"), []byte("http://127.0.0.1:10148\n"), 0o644)
	if got := apiForRoot(root); got != "http://127.0.0.1:10148" {
		t.Errorf("the note was not used: %q", got)
	}
	t.Setenv("LACIA_API", "http://example.test/")
	if got := apiForRoot(root); got != "http://example.test" {
		t.Errorf("LACIA_API must win: %q", got)
	}
}

func TestWindowInstructions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LACIA_USER", "")
	text := "あなたはレイシアです。" + rules.OwnerMark + "の秘書です。\n"
	// A repository that names nobody and keeps its own rules gets the
	// default name and the shared chat behavior, without the full core rules,
	// and is told where the parts the instructions leave to the rules are.
	if got := windowInstructions(text, root); got != chat.WithBehavior("あなたはレイシアです。オーナーさんの秘書です。\n\n"+ownRulesNote) {
		t.Errorf("changed: %q", got)
	}
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"user":"田中さん"}`), 0o644)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("`lacia rules` を読んでください。\n"), 0o644)
	got := windowInstructions(text, root)
	if !strings.HasPrefix(got, "あなたはレイシアです。田中さんの秘書です。") || !strings.Contains(got, "# レイシアの決まり") || strings.Contains(got, rules.OwnerMark) {
		t.Errorf("unexpected instructions:\n%.300s", got)
	}
}

// A repository that keeps no tasks gets the instructions and the core for
// that use: nothing tells the conversation to make a task.
func TestWindowInstructionsWithoutTasks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LACIA_USER", "")
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("`lacia rules` を読んでください。\n"), 0o644)
	for _, keeps := range []bool{true, false} {
		config := `{"format": 1}`
		if !keeps {
			config = `{"format": 1, "tasks": false}`
		}
		os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(config), 0o644)
		for name, text := range map[string]string{"window": codex.Instructions, "side": codex.SideInstructions} {
			got := windowInstructions(rules.FillPeer(text, "家庭"), root)
			if strings.Contains(got, "<!--") {
				t.Errorf("%s, tasks %v: a mark is left:\n%s", name, keeps, got)
			}
			if makes := strings.Contains(got, "lacia task new --area"); makes != keeps {
				t.Errorf("%s, tasks %v: the rules tell how to make a task: %v", name, keeps, makes)
			}
			if without := strings.Contains(got, "## タスクを持たない記録"); without == keeps {
				t.Errorf("%s, tasks %v: the core for no tasks is there: %v", name, keeps, without)
			}
		}
		launch, err := launchPrompt(root, time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(launch, "<!--") || strings.Contains(launch, "タスクの操作には") != keeps || strings.Contains(launch, "タスクを持たない使い方です") == keeps {
			t.Errorf("tasks %v: launch prompt:\n%.600s", keeps, launch)
		}
	}
}

func TestInit(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.MkdirAll(filepath.Join(root, "memory"), 0o755)
	os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"areas": ["work", "personal"], "user": "利用者さん"}`), 0o644)
	os.WriteFile(filepath.Join(root, "memory", "profile.md"), []byte("# 基本の情報\n\n- 呼び名：\n- 仕事：\n"), 0o644)
	if err := runInit([]string{"--root", root, "--user", "田中さん"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := task.LoadConfig(root)
	if err != nil || cfg.User != "田中さん" || len(cfg.Areas) != 2 {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "memory", "profile.md")); !strings.Contains(string(data), "- 呼び名：田中さん\n- 仕事：\n") {
		t.Errorf("profile = %q", data)
	}
	// A second run changes the settings, and leaves the profile alone.
	if err := runInit([]string{"--root", root, "--user", "佐藤さん"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "memory", "profile.md")); !strings.Contains(string(data), "田中さん") {
		t.Errorf("profile was rewritten: %q", data)
	}
	if err := runInit([]string{"--root", root, "--user", "a{b}"}); err == nil {
		t.Error("a name with braces was accepted")
	}
	// The calendar is written once, and a list that is there is kept.
	list := filepath.Join(root, ".lacia", "calendars.json")
	if _, err := os.Stat(list); err == nil {
		t.Error("a list of calendars was written without an account")
	}
	if err := runInit([]string{"--root", root, "--user", "田中さん", "--calendar", "not an address"}); err == nil {
		t.Error("a calendar account that is no address was accepted")
	}
	if err := runInit([]string{"--root", root, "--user", "田中さん", "--calendar", "tanaka@example.com"}); err != nil {
		t.Fatal(err)
	}
	if scope, err := calendar.LoadScope(list); err != nil || len(scope) != 1 || scope[0].Account != "tanaka@example.com" || scope[0].CalendarID != "primary" {
		t.Errorf("calendars = %+v, %v", scope, err)
	}
	if err := runInit([]string{"--root", root, "--user", "田中さん", "--calendar", "sato@example.com"}); err == nil {
		t.Error("the list of calendars was replaced")
	}
	// A copy only the server may change is refused.
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\n\tpushurl = disabled://\n"), 0o644)
	if err := runInit([]string{"--root", root, "--user", "田中さん"}); err == nil {
		t.Error("a read-only copy was written")
	}
}

// The instructions name the peer profile by its label, and say nothing of
// one where there is none.
func TestInstructionsNameThePeer(t *testing.T) {
	const line = "- 予定の変更が家庭モードの受け持つことに関わるときは、その範囲で、家庭モードへも伝えます。\n"
	if got := rules.FillPeer(codex.Instructions, "家庭モード"); !strings.Contains(got, line) || strings.Contains(got, rules.PeerMark) {
		t.Error("the line about the peer changed")
	}
	if got := rules.FillPeer(codex.Instructions, ""); strings.Contains(got, rules.PeerMark) || strings.Contains(got, "へも伝えます") || strings.Contains(got, "lacia rules peers") {
		t.Error("without a peer, the instructions still speak of one")
	}
	for _, text := range []string{codex.Instructions, codex.SideInstructions} {
		for _, word := range []string{"家庭モード", "仕事モード"} {
			if strings.Contains(text, word) {
				t.Errorf("the embedded instructions name a profile: %q", word)
			}
		}
	}
}

func TestChooseLogin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, login.ListFile)
	// A machine without the file starts the agent as it is installed.
	none, err := login.LoadList(path)
	if err != nil {
		t.Fatal(err)
	}
	if command, name, err := chooseLogin("codex", "", none); err != nil || name != "" || strings.Join(command, " ") != "codex" {
		t.Errorf("no list: %v %q %v", command, name, err)
	}
	os.WriteFile(path, []byte(`{"default":"work","logins":{
		"work":{"label":"仕事用","codex":["codex-work"],"codex_home":"~/.codex-work"},
		"personal":{"codex":["codex","--profile","personal"],"claude":["claude-p"]}}}`), 0o644)
	list, err := login.LoadList(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind, name string
		rest       []string
		command    string
		chosen     string
		remaining  string
	}{
		{"codex", "", nil, "codex-work", "work", ""},
		{"codex", "", []string{"--yolo"}, "codex-work", "work", "--yolo"},
		{"codex", "", []string{"--login", "personal", "--yolo"}, "codex --profile personal", "personal", "--yolo"},
		{"codex", "", []string{"--login=personal", "exec", "-"}, "codex --profile personal", "personal", "exec -"},
		{"codex", "personal", []string{"--yolo"}, "codex --profile personal", "personal", "--yolo"},
		{"claude", "personal", nil, "claude-p", "personal", ""},
		// Only a leading --login is lacia's; later ones belong to the agent.
		{"codex", "", []string{"exec", "--login", "personal"}, "codex-work", "work", "exec --login personal"},
	} {
		options, err := parseLaunchOptions(c.name, c.rest)
		if err != nil {
			t.Fatal(err)
		}
		command, chosen, err := chooseLogin(c.kind, options.login, list)
		if err != nil || strings.Join(command, " ") != c.command || chosen != c.chosen || strings.Join(options.args, " ") != c.remaining {
			t.Errorf("%s %q %v: %v %q %v %v", c.kind, c.name, c.rest, command, chosen, options.args, err)
		}
	}
	// The default login has no command for Claude Code: it starts as it is installed.
	if command, name, err := chooseLogin("claude", "", list); err != nil || name != "" || strings.Join(command, " ") != "claude" {
		t.Errorf("claude without a login: %v %q %v", command, name, err)
	}
	for _, c := range [][]string{{"codex", "other"}, {"claude", "work"}} {
		if _, _, err := chooseLogin(c[0], c[1], list); err == nil {
			t.Errorf("%v was started", c)
		}
	}
	if _, err := parseLaunchOptions("", []string{"--login"}); err == nil {
		t.Error("--login without a name was accepted")
	}
	if got := strings.Join(list.Names(), " "); got != "work personal" || list.Title("work") != "仕事用" || list.Title("personal") != "personal" {
		t.Errorf("names = %q", got)
	}
	// Names that would read as a command of lacia, and a default that is
	// not listed, are refused.
	for _, text := range []string{
		`{"logins":{"login":{"codex":["codex"]}}}`, `{"logins":{"Work":{"codex":["codex"]}}}`,
		`{"logins":{"work":{}}}`, `{"default":"x","logins":{"work":{"codex":["codex"]}}}`, `{`,
	} {
		os.WriteFile(path, []byte(text), 0o644)
		if _, err := login.LoadList(path); err == nil {
			t.Errorf("%s was accepted", text)
		}
	}
	env := map[string]string{"HOME": "/home/u"}
	if got := login.ListPath(func(k string) string { return env[k] }); got != "/home/u/.config/lacia/logins.json" {
		t.Errorf("path = %q", got)
	}
	env["LACIA_CONFIG_DIR"] = "/etc/lacia"
	if got := login.ListPath(func(k string) string { return env[k] }); got != "/etc/lacia/logins.json" {
		t.Errorf("path = %q", got)
	}
}

func TestYoloMeansTheSameForBothAgents(t *testing.T) {
	const off = `--dangerously-skip-permissions --settings {"sandbox":{"enabled":false}}`
	for _, c := range []struct {
		kind string
		rest []string
		want string
		yolo bool
	}{
		{"claude", []string{"--yolo"}, off, true},
		{"claude", []string{"--model", "x", "--yolo", "-p", "hello"}, "--model x " + off + " -p hello", true},
		{"claude", []string{"-p", "hello"}, "-p hello", false},
		// After "--", it is the user's text.
		{"claude", []string{"--", "--yolo"}, "-- --yolo", false},
		// Codex has the flag itself.
		{"codex", []string{"--yolo", "exec", "-"}, "--yolo exec -", true},
	} {
		got, yolo := yoloArgs(c.kind, c.rest)
		if strings.Join(got, " ") != c.want || yolo != c.yolo {
			t.Errorf("%s %v: %v %v", c.kind, c.rest, got, yolo)
		}
	}
}

// A custom family-style AGENTS.md does not opt into task management rules,
// but must still receive the same naming and recipient policy as a managed repository.
func TestEveryEntryPointReceivesSharedChatBehavior(t *testing.T) {
	for _, mode := range []string{"custom", "missing", "managed", "managed-core-override"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if mode != "missing" {
				agents := "# この家の決まり\n\nタスクは管理しません。\n"
				if strings.HasPrefix(mode, "managed") {
					agents = "`lacia rules` を読んでください。\n"
				}
				if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(agents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "managed-core-override" {
				if err := os.MkdirAll(filepath.Join(root, "rules"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "rules", "core.md"), []byte("# 独自の共通ルール\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			launch, err := launchPrompt(root, time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			for name, prompt := range map[string]string{
				"launch":        launch,
				"window":        windowInstructions(codex.Instructions, root),
				"side-window":   windowInstructions(codex.SideInstructions, root),
				"custom-window": windowInstructions("独自の起動指示です。", root),
			} {
				if got := strings.Count(prompt, strings.TrimSpace(chat.Behavior)); got != 1 {
					t.Errorf("%s has policy %d times: %s", name, got, prompt)
				}
				if !strings.Contains(prompt, "ふだん「さん」を付けます") || !strings.Contains(prompt, "役割を指すときは「オーナーさん」と書きます") {
					t.Errorf("%s lacks the naming policy", name)
				}
				if strings.Contains(prompt, "{{案内の直接の宛先}}") {
					t.Errorf("%s has an unfilled policy mark", name)
				}
				if mode != "managed" && strings.Contains(prompt, "# レイシアの決まり") {
					t.Errorf("%s imported unrelated core rules", name)
				}
				if chat.WithBehavior(prompt) != prompt {
					t.Errorf("%s duplicated its behavior", name)
				}
			}
		})
	}
}

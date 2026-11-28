package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/chat"
	"github.com/imos/lacia/internal/claudecode"
	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// launchManaged opens a conversation in a repository that leaves its rules
// to the binary. The rules themselves follow it. The lines for a repository
// with tasks and for one without are marked (rules.Fit).
const launchManaged = `あなたはレイシアです。` + rules.OwnerMark + `の秘書として、この会話で依頼を受けます。

<!-- tasks -->
記録のリポジトリーは %s です。タスクの操作には lacia コマンドを使います。
<!-- /tasks -->
<!-- no-tasks -->
記録のリポジトリーは %s です。記録の操作には lacia コマンドを使います。この記録は、タスクを持たない使い方です。
<!-- /no-tasks -->

いつも守る決まりを下に載せています。会話が圧縮されたあとは、lacia rules を実行して読み直してください。場面ごとの決まりは、lacia rules list で名前を調べ、lacia rules <名前> で読んでください。サブエージェントにも、lacia rules を読むよう伝えてください。

---

`

// launchOwn opens a conversation in a repository that keeps a full set of
// rules of its own in AGENTS.md.
const launchOwn = `あなたはレイシアです。記録のリポジトリーは %s です。最初に、このリポジトリーの AGENTS.md を読み、そこに書かれた決まりと参照先の文書に従ってください。会話が圧縮されたあとも、同じように読み直してください。タスクの操作には lacia コマンドを使います。
`

// launchPrompt is what a conversation started through `lacia codex` or
// `lacia claude` is told before the first message.
func launchPrompt(root string, now time.Time) (string, error) {
	var b strings.Builder
	cfg, err := task.LoadConfig(root)
	owner := cfg.OwnerName()
	if rules.Managed(root) {
		if err != nil {
			return "", err
		}
		core, err := rules.Load(root, rules.Core, owner)
		if err != nil {
			return "", err
		}
		b.WriteString(rules.Fit(strings.ReplaceAll(fmt.Sprintf(launchManaged, root, root), rules.OwnerMark, owner), cfg.KeepsTasks()))
		b.WriteString(strings.TrimRight(core, "\n"))
	} else {
		fmt.Fprintf(&b, launchOwn, root)
	}
	// The same note the server adds to scheduled jobs: today's date and
	// what is out of the ordinary.
	return chat.WithBehavior(b.String(), owner) + schedule.Situation(root, "memory/now.md", now), nil
}

type launchOptions struct {
	login  string
	memory string
	// here starts the conversation where it is run, even outside the
	// records.
	here bool
	args []string
}

// parseLaunchOptions reads Lacia's options at the start of the command line.
func parseLaunchOptions(name string, args []string) (launchOptions, error) {
	options := launchOptions{login: name}
	for len(args) > 0 {
		arg := args[0]
		switch {
		case options.login == "" && arg == "--login":
			if len(args) < 2 || args[1] == "--" {
				return launchOptions{}, fmt.Errorf("--login のあとに、ログインの名前を書いてください")
			}
			options.login = args[1]
			args = args[1:]
		case options.login == "" && strings.HasPrefix(arg, "--login="):
			options.login = strings.TrimPrefix(arg, "--login=")
		case arg == "--memory", arg == "--memory=true":
			options.memory = "true"
		case arg == "--memory=false":
			options.memory = "false"
		case strings.HasPrefix(arg, "--memory="):
			return launchOptions{}, fmt.Errorf("--memory には true か false を指定してください")
		case arg == "--here":
			options.here = true
		case arg == "--yolo":
			options.args = append(options.args, arg)
		default:
			options.args = append(options.args, args...)
			return options, nil
		}
		args = args[1:]
	}
	return options, nil
}

// chooseLogin uses the named login, the default of the list, or the agent as
// installed if the list has no default for this kind of agent.
func chooseLogin(kind, name string, list *login.List) (command []string, chosen string, err error) {
	if name == "" {
		if command, err := list.Command(list.Default, kind); list.Default != "" && err == nil {
			return command, list.Default, nil
		}
		return []string{kind}, "", nil
	}
	command, err = list.Command(name, kind)
	return command, name, err
}

// claudeYolo is what --yolo means to Claude Code: no permission prompts,
// and no sandbox around its commands. Skipping the permissions alone leaves
// the sandbox of the user's settings in place, so both are said.
var claudeYolo = []string{"--dangerously-skip-permissions", "--settings", `{"sandbox":{"enabled":false}}`}

// yoloArgs gives --yolo the same meaning for both agents: start without
// approvals and without a sandbox. Codex has the flag itself; for Claude
// Code it is replaced with the options that do the same. What follows "--"
// is not an option and is left alone.
func yoloArgs(kind string, rest []string) ([]string, bool) {
	var out []string
	found := false
	for i, arg := range rest {
		if arg == "--" {
			return append(out, rest[i:]...), found
		}
		if arg != "--yolo" {
			out = append(out, arg)
			continue
		}
		found = true
		if kind == caller.Claude {
			out = append(out, claudeYolo...)
		} else {
			out = append(out, arg)
		}
	}
	return out, found
}

// launchArgs puts the prompt where each agent takes one. Both take it as
// an addition to their own instructions, not as the first message, so it
// survives when the conversation is compacted.
func launchArgs(kind, prompt, root, dir string, rest []string, memory bool) []string {
	var args []string
	switch kind {
	case caller.Claude:
		args = append(args, "--append-system-prompt", prompt)
		if !within(root, dir) {
			// Started elsewhere, such as in a code repository: the records
			// must still be readable.
			args = append(args, "--add-dir", root)
		}
	case caller.Codex:
		// A TOML basic string; Go's quoting of valid UTF-8 text is one.
		args = append(codex.WithMemory(args, memory), "-c", "developer_instructions="+strconv.Quote(prompt))
	}
	return append(args, rest...)
}

// within reports whether dir is root or lies under it.
func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// runLaunch starts Codex or Claude Code as Lacia: with the rules, the day's
// situation, and an environment in which the lacia command finds the
// repository. The process becomes the agent, so `lacia who` and the task
// records see the conversation as they see one started by hand.
//
// name is the login to start it with, from `lacia codex-<name>`; a leading
// `--login <name>` says the same. Without either, the default
// login of this machine's list is used, and without a list the agent is
// started as it is installed.
//
// Run outside the records, it starts in them, unless --here says to start
// where it is.
func runLaunch(kind, name string, rest []string) error {
	options, err := parseLaunchOptions(name, rest)
	if err != nil {
		return err
	}
	root, err := findRoot("")
	if err != nil {
		return err
	}
	if err := requireCurrentRecords(root); err != nil {
		return err
	}
	list, err := login.LoadList(login.ListPath(os.Getenv))
	if err != nil {
		return err
	}
	command, name, err := chooseLogin(kind, options.login, list)
	if err != nil {
		return err
	}
	if name != "" {
		// Said every time: with more than one login, which one this is
		// must not be a guess.
		fmt.Fprintf(os.Stderr, "「%s」のログインで起動します。\n", list.Title(name))
	}
	rest, yolo := yoloArgs(kind, options.args)
	if yolo {
		fmt.Fprintln(os.Stderr, "承認とサンドボックスを外して起動します。")
	}
	agent := program.Codex(command[0])
	if kind == caller.Claude {
		agent = program.ClaudeCode(command[0])
	}
	if _, err := agent.Find(); err != nil {
		return fmt.Errorf("%s が見つかりません。別の名前でインストールしているときは、%s にログイン名と起動用のコマンドを指定してください", command[0], login.ListPath(os.Getenv))
	}
	prompt, err := launchPrompt(root, time.Now())
	if err != nil {
		return err
	}
	dir, _ := os.Getwd()
	env := os.Environ()
	if !within(root, dir) && !options.here {
		if err := os.Chdir(root); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "記録の場所（%s）で始めます。いまの場所で始めるときは、--here を付けてください。\n", root)
		dir = root
		env = append(env, "PWD="+root)
	}
	args := append(append([]string{}, command[1:]...), launchArgs(kind, prompt, root, dir, rest, options.memory == "true")...)
	env = append(env, "LACIA_TASKS="+root, "LACIA_LAUNCH="+kind)
	env = append(env, rulesNoticeEnvironment(root)...)
	if name != "" {
		env = append(env, "LACIA_LOGIN="+name)
	}
	if self, err := os.Executable(); err == nil {
		// So that the conversation runs this binary, whatever else is
		// installed under the same name.
		env = append(env, "LACIA_BIN="+self, "PATH="+filepath.Dir(self)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if kind == caller.Claude {
		if options.memory == "" {
			env = claudecode.WithMemoryDefault(env)
		} else {
			env = claudecode.WithMemory(env, options.memory == "true")
		}
	}
	return agent.Replace(args, lastWins(env))
}

// lastWins keeps the last value of each variable. The list is handed to
// the program as it is, and most programs read the first value.
func lastWins(env []string) []string {
	seen := map[string]bool{}
	var out []string
	for i := len(env) - 1; i >= 0; i-- {
		key, _, _ := strings.Cut(env[i], "=")
		if !seen[key] {
			seen[key] = true
			out = append(out, env[i])
		}
	}
	slices.Reverse(out)
	return out
}

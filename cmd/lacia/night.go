package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/self"
)

// nightAgent chooses, each night, what the nightly rewrite runs on, so that
// Claude Code or Codex installed after the server started is used from the
// next night (docs/design/self.md).
type nightAgent struct {
	choice       string // self-agent: auto, claude or codex
	claude       string // self-claude
	conversation string // the claude command of the conversations
	codex        string // the codex command of the conversations
	codexModel   string
	codexEffort  string
}

const (
	nightAuto   = "auto"
	nightClaude = "claude"
	nightCodex  = "codex"
)

func nightAgentOf(c *serveConfig) nightAgent {
	return nightAgent{choice: *c.str("self-agent"), claude: strings.TrimSpace(*c.str("self-claude")), conversation: *c.str("claude"),
		codex: *c.str("codex"), codexModel: *c.str("codex-model"), codexEffort: *c.str("codex-effort")}
}

// claudeCandidates are the executables of Claude Code tried, in order.
func (n nightAgent) claudeCandidates() []string {
	if n.claude != "" {
		return []string{n.claude}
	}
	var names []string
	if fields := strings.Fields(n.conversation); len(fields) > 0 {
		names = append(names, fields[0])
	}
	names = append(names, "claude")
	// Where the installer of Claude Code puts it, which a server started at
	// login may not have on its PATH.
	if dir, err := os.UserHomeDir(); err == nil {
		names = append(names, filepath.Join(dir, ".local", "bin", "claude"))
	}
	return names
}

// codexCandidates take the executable alone: the arguments the conversations
// start it with may loosen the sandbox the night depends on.
func (n nightAgent) codexCandidates() []string {
	var names []string
	if fields := strings.Fields(n.codex); len(fields) > 0 {
		names = append(names, fields[0])
	}
	return append(names, "codex")
}

func firstFound(names []string, find func(string) program.Program) (string, bool) {
	for _, name := range names {
		if path, err := find(name).Find(); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				return abs, true
			}
		}
	}
	return "", false
}

func (n nightAgent) checkChoice() error {
	switch n.choice {
	case nightAuto, nightClaude, nightCodex, "":
		return nil
	}
	return fmt.Errorf("self-agent には auto、claude、codex のどれかを書いてください（いまの値: %s）", n.choice)
}

// find tells what tonight runs on: nightClaude or nightCodex, and where it is.
func (n nightAgent) find() (string, string, error) {
	if err := n.checkChoice(); err != nil {
		return "", "", err
	}
	claudes, codexes := n.claudeCandidates(), n.codexCandidates()
	switch n.choice {
	case nightAuto, "":
		if path, ok := firstFound(claudes, program.ClaudeCode); ok {
			return nightClaude, path, nil
		}
		if path, ok := firstFound(codexes, program.Codex); ok {
			return nightCodex, path, nil
		}
		return "", "", fmt.Errorf("夜の処理に使う Claude Code も Codex も見つかりませんでした（探したもの: %s）。どちらかを入れると、次の夜から動きます", strings.Join(append(claudes, codexes...), "、"))
	case nightClaude:
		if path, ok := firstFound(claudes, program.ClaudeCode); ok {
			return nightClaude, path, nil
		}
		return "", "", fmt.Errorf("夜の処理に使う Claude Code が見つかりませんでした（探したもの: %s）。Claude Code を入れると、次の夜から動きます", strings.Join(claudes, "、"))
	default:
		if path, ok := firstFound(codexes, program.Codex); ok {
			return nightCodex, path, nil
		}
		return "", "", fmt.Errorf("夜の処理に使う Codex が見つかりませんでした（探したもの: %s）。Codex を入れると、次の夜から動きます", strings.Join(codexes, "、"))
	}
}

// open starts the night's conversation on what find chose. scratch is a
// directory outside the records the night on Codex may work in.
func (n nightAgent) open(model, root string, readRecords bool, scratch string, private ...string) func(context.Context) (self.Think, func(), error) {
	return func(ctx context.Context) (self.Think, func(), error) {
		kind, executable, err := n.find()
		if err != nil {
			return nil, nil, err
		}
		if kind == nightCodex {
			return self.CodexNight{Executable: executable, Model: n.codexModel, Effort: n.codexEffort, Root: root,
				ReadRecords: readRecords, Private: private, Scratch: scratch}.Open(ctx)
		}
		return self.Claude(executable, model, root, readRecords, private...)(ctx)
	}
}

// nightSummary tells people whether the nightly rewrite runs with these
// settings, on what, what it sends where, and how to stop it.
func nightSummary(c *serveConfig) string {
	switch at := *c.str("self-at"); {
	case *c.str("codex") == "" && *c.str("claude") == "":
		return "依頼を受け付ける設定（codex か claude）がないので、夜の処理は動きません。"
	case *c.str("self") == "" || *c.str("self") == off:
		return "self = off なので、夜の処理と、書き留めと、「レイシアのこと」のページを止めています。"
	case at == "" || at == off:
		return "self-at = off なので、夜の処理を止めています。書き留めとページは使えます。"
	default:
		kind, path, err := nightAgentOf(c).find()
		if err != nil {
			return "毎日 " + at + " に夜の処理を行う設定ですが、いまは動きません。" + err.Error() + "。"
		}
		sent := "書き留めと、前の版の「わたしのこと」"
		if *c.on("self-exchanges") {
			sent = "その日のやり取り（最長72時間分）と、記録から読んだファイル"
		}
		name, provider, other := "Claude Code", "Anthropic", "Codex で動かしたいときは、self-agent = codex と書きます。"
		if kind == nightCodex {
			name, provider, other = "Codex", "OpenAI", "Codex で記録の読ませない場所を守れない端末では、動かさずに、理由を夜の記録に残します。"
		}
		return fmt.Sprintf("毎日 %s に、%s（%s）で夜の処理を行います。%sを、%s へ送ります。止めるときは、記録の .lacia/serve.conf に self-at = off と書きます。%s", at, name, path, sent, provider, other)
	}
}

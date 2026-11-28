package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/chat"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/task"
)

// sessionRulesWait bounds how long a conversation waits for the replacement
// before it starts with the embedded rules.
var sessionRulesWait = 2 * time.Second

// The path supplied by the server is its own absolute executable path. Using
// it instead of PATH lets an atomic deployment supply the replacement binary
// without invoking a shim or a command from the task repository.
func sessionRules(ctx context.Context, root, executable string) rules.SessionRules {
	usingReplacement := filepath.IsAbs(executable)
	if usingReplacement {
		ctx, cancel := context.WithTimeout(ctx, sessionRulesWait)
		defer cancel()
		cmd := program.Lacia(executable).Command(ctx, "rules", "--session-json")
		cmd.Dir = root
		cmd.Env = []string{"LACIA_TASKS=" + root, "LACIA_USER=" + os.Getenv("LACIA_USER")}
		cmd.WaitDelay = 250 * time.Millisecond
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil && out.Len() <= 2<<20 {
			var current rules.SessionRules
			if err := json.Unmarshal(out.Bytes(), &current); err == nil && len(current.Revision) != 0 && current.Behavior != "" {
				return current
			}
		}
	}
	cfg, _ := task.LoadConfig(root)
	current, err := rules.ReadSessionRules(root, cfg.OwnerName())
	if err != nil {
		return rules.SessionRules{Behavior: chat.BehaviorFor(cfg.OwnerName())}
	}
	if usingReplacement {
		// An unavailable replacement is not evidence of a rollback. The
		// embedded prompt is still enough to start a new conversation.
		current.Revision = nil
	}
	return current
}

func resolveWindowInstructions(text, root string, current rules.SessionRules) string {
	cfg, _ := task.LoadConfig(root)
	text = rules.Fit(strings.ReplaceAll(text, rules.OwnerMark, cfg.OwnerName()), cfg.KeepsTasks())
	if !rules.Managed(root) {
		text = strings.TrimRight(text, "\n") + "\n\n" + ownRulesNote
	} else if current.Core != "" {
		text = strings.TrimRight(text, "\n") + "\n\n---\n\nここからは、作業ディレクトリーの `AGENTS.md` が案内する、いつも守る決まりです。`lacia rules` でも読み直せます。\n\n" + current.Core
	}
	return current.AddBehavior(text)
}

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/task"
)

const (
	rulesBaselineEnv = "LACIA_RULES_BASELINE"
	rulesLaunchEnv   = "LACIA_RULES_LAUNCH"
)

// rulesNoticeEnvironment records the embedded rules observed at launch.
// Subagents inherit the baseline and keep their own notice receipt.
func rulesNoticeEnvironment(root string) []string {
	cfg, _ := task.LoadConfig(root)
	current, err := rules.ReadSessionRules(root, cfg.OwnerName())
	if err != nil {
		return nil
	}
	data, err := json.Marshal(current.Revision)
	if err != nil {
		return nil
	}
	dir, err := disk.TempDir("lacia-rule-notices-")
	if err != nil {
		return nil
	}
	return []string{rulesBaselineEnv + "=" + string(data), rulesLaunchEnv + "=" + filepath.Base(dir)}
}

func noticeRulesChange(args []string, out io.Writer) {
	launch := os.Getenv(rulesLaunchEnv)
	if !strings.HasPrefix(launch, "lacia-rule-notices-") || filepath.Base(launch) != launch || len(args) == 0 {
		return
	}
	switch args[0] {
	case "rules", "serve", "agent", "help", "-h", "--help":
		return
	}
	var baseline rules.Revision
	if json.Unmarshal([]byte(os.Getenv(rulesBaselineEnv)), &baseline) != nil || len(baseline) == 0 {
		return
	}
	root, err := findRoot("")
	if err != nil {
		return
	}
	cfg, _ := task.LoadConfig(root)
	current, err := rules.ReadSessionRules(root, cfg.OwnerName())
	if err != nil {
		return
	}
	dir := filepath.Join(os.TempDir(), launch)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return
	}
	invoker := me()
	if path := os.Getenv("LACIA_SERVER_THREAD"); path != "" && invoker.Known() && invoker.Agents[0].Session != "" {
		var thread struct {
			ID string `json:"thread_id"`
		}
		data, _ := os.ReadFile(path)
		if json.Unmarshal(data, &thread) == nil && thread.ID == invoker.Agents[0].Session {
			// The server queues the notice for its main conversation.
			return
		}
	}
	notices, err := disk.OpenTempDir(launch)
	if err != nil {
		return
	}
	defer notices.Close()
	key := sha256.Sum256([]byte(root + "\n" + invoker.Key()))
	writeRulesNotice(notices, fmt.Sprintf("%x.json", key), baseline, current.Revision, out)
}

func writeRulesNotice(notices *disk.Dir, name string, baseline, current rules.Revision, out io.Writer) {
	if _, err := notices.Lstat(name); os.IsNotExist(err) && current.Notice(baseline) == "" {
		return
	}
	file, err := notices.OpenLock(name, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	if syscall.Flock(int(file.Fd()), syscall.LOCK_EX) != nil {
		return
	}
	var previous rules.Revision
	if json.NewDecoder(io.LimitReader(file, 256<<10)).Decode(&previous) != nil {
		previous = baseline
	}
	if note := current.Notice(previous); note != "" {
		fmt.Fprintln(out, note)
		if _, err := file.Seek(0, io.SeekStart); err == nil && file.Truncate(0) == nil {
			_ = json.NewEncoder(file).Encode(current)
		}
	}
}

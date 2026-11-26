package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/claudecode"
	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/schedule"
)

// agentScheduleConfig uses the notifier's transport and repository. It
// contains only the device's enrollment and an optional read-only probe.
type agentScheduleConfig struct {
	Device     string   `json:"device"`
	SecretFile string   `json:"secret_file"`
	AuthCheck  []string `json:"auth_check,omitempty"`
}

type scheduleConversations struct {
	root, state, base, place string
	logins                   *login.List
}

func (r scheduleConversations) run(ctx context.Context, execution schedule.Execution, prompt string) (string, error) {
	kind := execution.Kind
	if kind == "" {
		// The old device registered before this field was stored. Its
		// prepared runs were launched by Codex.
		kind = caller.Codex
	}
	place := r.place
	if execution.To != "" {
		destination, err := caller.ParseDestination(execution.To)
		if err != nil || destination.Place == "" {
			return "", errors.New("予約の宛先を確かめられませんでした")
		}
		place = destination.Place
	}
	name, command, err := r.logins.ExecutionCommand(execution.Job.Profile, kind, execution.Job.Side)
	if err != nil {
		slog.Warn("scheduled login unavailable", "id", execution.ID, "error", err)
		return "", err
	}
	conversation := "main"
	if execution.Job.Side {
		conversation = "side"
	}
	dir := filepath.Join(r.state, place, kind, name, conversation)
	if execution.Job.Profile != "" {
		dir = filepath.Join(dir, "profiles", execution.Job.Profile)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	instructions, err := launchPrompt(r.root, time.Now())
	if err != nil {
		return "", err
	}
	env := []string{
		"LACIA_API=" + r.base,
		"LACIA_TASKS=" + r.root,
		"LACIA_LAUNCH=" + kind,
		"LACIA_PROFILE=" + place,
		"LACIA_LOGIN=" + name,
		"LACIA_SCHEDULE_RUN=" + execution.ID,
		"LACIA_EXECUTION_PROFILE=" + execution.Job.Profile,
		"LACIA_SCHEDULE_CONVERSATION=" + conversation,
	}
	if path, err := os.Executable(); err == nil {
		env = append(env, "LACIA_BIN="+path)
	}
	if kind == caller.Claude {
		config := claudecode.Config{Command: command, Workspace: r.root, StateDir: dir, Env: env,
			Ephemeral: execution.Job.Side, Instructions: instructions + claudecode.Note}
		reply, _, err := claudecode.New(config).Run(ctx, prompt)
		return reply[:min(len(reply), scheduleReplyLimit)], err
	}
	return r.codex(ctx, dir, command, env, instructions, prompt, execution.Job.Side)
}

const scheduleReplyLimit = 1 << 20

var scheduleThreadID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func (r scheduleConversations) codex(ctx context.Context, dir string, command, env []string, instructions, prompt string, side bool) (string, error) {
	threadFile := filepath.Join(dir, "thread")
	var threadID string
	if !side {
		data, err := os.ReadFile(threadFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		threadID = strings.TrimSpace(string(data))
		if threadID != "" && !scheduleThreadID.MatchString(threadID) {
			return "", errors.New("予約の会話IDを読めません")
		}
	}
	out, err := os.CreateTemp(dir, "reply-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(out.Name())
	if err := out.Close(); err != nil {
		return "", err
	}
	args := []string{"exec"}
	if threadID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--output-last-message", out.Name(), "--skip-git-repo-check")
	if side {
		args = append(args, "--ephemeral")
	}
	if threadID != "" {
		args = append(args, threadID)
	}
	args = append(args, "-")
	argv := append(append([]string{}, command[1:]...), launchArgs(caller.Codex, instructions, r.root, r.root, args, false)...)
	cmd := exec.CommandContext(ctx, command[0], argv...)
	cmd.Dir = r.root
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = io.Discard
	var events codexScheduleEvents
	cmd.Stdout = &events
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return "", err
	}
	if !side && threadID == "" {
		if events.threadID == "" {
			return "", errors.New("予約の会話IDを受け取れませんでした")
		}
		// A single worker holds the state lock. Rename keeps an interrupted
		// write from leaving a partly written conversation ID.
		tmp, err := os.CreateTemp(dir, "thread-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(tmp.Name())
		_, writeErr := fmt.Fprintln(tmp, events.threadID)
		if err := errors.Join(writeErr, tmp.Close()); err != nil {
			return "", err
		}
		if err := os.Rename(tmp.Name(), threadFile); err != nil {
			return "", err
		}
	}
	f, err := os.Open(out.Name())
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, scheduleReplyLimit))
	return string(data), err
}

// Only the thread ID is needed from the event stream. Never keep the rest
// of the conversation in an extra log.
type codexScheduleEvents struct {
	threadID string
	line     []byte
	skipping bool
}

func (e *codexScheduleEvents) Write(data []byte) (int, error) {
	n := len(data)
	for len(data) > 0 && e.threadID == "" {
		chunk, rest, ended := bytes.Cut(data, []byte{'\n'})
		if len(e.line)+len(chunk) > 32<<10 {
			e.skipping = true
		}
		if !e.skipping {
			e.line = append(e.line, chunk...)
		}
		if ended && !e.skipping {
			var event struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
			}
			if json.Unmarshal(e.line, &event) == nil && event.Type == "thread.started" && scheduleThreadID.MatchString(event.Thread) {
				e.threadID = event.Thread
			}
		}
		if ended {
			e.line = e.line[:0]
			e.skipping = false
		}
		data = rest
	}
	return n, nil
}

// selection records the resolved local configuration before launching a process.
func (r scheduleConversations) selection(e schedule.Execution) (string, string, error) {
	kind := e.Kind
	if kind == "" {
		kind = caller.Codex
	}
	name, _, err := r.logins.ExecutionCommand(e.Job.Profile, kind, e.Job.Side)
	if err != nil {
		return "", "", err
	}
	profile := e.Job.Profile
	if profile == "" {
		profile = "default"
	}
	return profile, name, nil
}

package self

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/imos/lacia/internal/claudecode"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
)

type Think func(context.Context, string) (string, error)

// Claude opens a lazy stream for the night. A successful result leaves the
// process open so the next prompt sees the same conversation.
func Claude(executable, model, root string, readRecords bool, privatePaths ...string) func(context.Context) (Think, func(), error) {
	flags := []string{"--print", "--verbose", "--model", model, "--safe-mode", "--restricted",
		"--permission-mode", "dontAsk", "--no-session-persistence", "--strict-mcp-config", "--disallowedTools", "mcp__*",
		"--input-format", "stream-json", "--output-format", "stream-json"}
	if readRecords {
		settings, _ := json.Marshal(map[string]any{
			"includeGitInstructions": false,
			"permissions":            map[string]any{"deny": recordReadDeny(root, privatePaths)},
		})
		flags = append(flags, "--tools", "Read,Glob,Grep", "--settings", string(settings))
	} else {
		flags = append(flags, "--tools", "")
	}

	return func(ctx context.Context) (Think, func(), error) {
		var session *claudeStream
		var temporary string
		closeSession := func() {
			if session != nil {
				session.close()
				session = nil
			}
			if temporary != "" {
				disk.RemoveTempDir(temporary)
				temporary = ""
			}
		}
		return func(_ context.Context, prompt string) (string, error) {
			if session == nil {
				workspace := root
				if !readRecords {
					dir, err := disk.TempDir("lacia-self-")
					if err != nil {
						return "", err
					}
					temporary, workspace = dir, dir
				}
				var err error
				session, err = startClaudeStream(ctx, executable, flags, workspace)
				if err != nil {
					closeSession()
					return "", err
				}
			}
			result, err := session.turn(prompt)
			if err != nil {
				// A broken conversation cannot accept the next phase. Let it
				// start fresh instead of losing both phases to one failure.
				s := session
				closeSession()
				if detail := strings.TrimSpace(s.stderr.String()); detail != "" {
					err = fmt.Errorf("%w: %s", err, clip(detail, 600))
				}
			}
			return result, err
		}, closeSession, nil
	}
}

// recordReadDeny adds repository-local exclusions to the restricted mode
// boundary. State and machine configuration can also be configured inside
// the repository, so exclude the actual paths when applicable.
func recordReadDeny(root string, privatePaths []string) []string {
	var patterns []string
	for _, dir := range privateTop {
		patterns = append(patterns, "./"+dir, "./"+dir+"/**")
	}
	patterns = append(patterns, privateAnywhere...)
	for _, rel := range privateRel(root, privatePaths) {
		patterns = append(patterns, "./"+rel, "./"+rel+"/**")
	}
	rules := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		rules = append(rules, "Read("+pattern+")")
	}
	return rules
}

type claudeStream struct {
	cmd    *program.Cmd
	cancel context.CancelFunc
	input  io.WriteCloser
	events *json.Decoder
	stderr *bytes.Buffer
}

func startClaudeStream(ctx context.Context, executable string, flags []string, root string) (*claudeStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := program.ClaudeCode(executable).Command(ctx, flags...)
	cmd.Dir = root
	const disableGit = "CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS="
	env := slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, disableGit) })
	cmd.Env = append(claudecode.WithMemory(env, false), disableGit+"1")
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		cancel()
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		cancel()
		return nil, err
	}
	return &claudeStream{cmd: cmd, cancel: cancel, input: input, events: json.NewDecoder(output), stderr: &stderr}, nil
}

func (s *claudeStream) close() {
	s.input.Close()
	s.cancel()
	s.cmd.Wait()
}

func (s *claudeStream) turn(prompt string) (string, error) {
	timer := time.AfterFunc(20*time.Minute, s.cancel)
	defer timer.Stop()
	message := map[string]any{
		"type": "user", "message": map[string]string{"role": "user", "content": prompt}, "parent_tool_use_id": nil,
	}
	if err := json.NewEncoder(s.input).Encode(message); err != nil {
		return "", err
	}
	for {
		var event struct {
			Type    string   `json:"type"`
			Subtype string   `json:"subtype"`
			Result  string   `json:"result"`
			IsError bool     `json:"is_error"`
			Errors  []string `json:"errors"`
		}
		if err := s.events.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return "", errors.New("Claude Code が返事を返す前に終了しました")
			}
			return "", err
		}
		if event.Type != "result" {
			continue
		}
		if event.IsError || event.Subtype != "success" {
			detail := strings.TrimSpace(strings.Join(event.Errors, " "))
			if detail == "" {
				detail = strings.TrimSpace(event.Result)
			}
			return "", fmt.Errorf("Claude Code の応答を得られませんでした（%s）: %s", event.Subtype, clip(detail, 600))
		}
		return event.Result, nil
	}
}

package claudecode

import (
	"slices"
	"strings"
)

const disableAutoMemory = "CLAUDE_CODE_DISABLE_AUTO_MEMORY="

// WithMemoryDefault disables Claude Code's auto memory unless the caller
// explicitly set its environment variable.
func WithMemoryDefault(env []string) []string {
	for _, entry := range env {
		if strings.HasPrefix(entry, disableAutoMemory) {
			return env
		}
	}
	return append(env, disableAutoMemory+"1")
}

// WithMemory sets the child process's auto memory, replacing any inherited value.
func WithMemory(env []string, enabled bool) []string {
	env = slices.DeleteFunc(env, func(entry string) bool {
		return strings.HasPrefix(entry, disableAutoMemory)
	})
	if enabled {
		return append(env, disableAutoMemory+"0")
	}
	return append(env, disableAutoMemory+"1")
}

package codex

import "strconv"

// WithMemoryDefaults disables cross-session memories. Explicitly mark new
// threads ineligible for extraction by another Codex using the same home.
func WithMemoryDefaults(args []string) []string {
	return WithMemory(args, false)
}

// WithMemory sets the cross-session memory feature and generation. Enabling
// it also lets Codex read memories even if the user's configuration says not to.
func WithMemory(args []string, enabled bool) []string {
	value := strconv.FormatBool(enabled)
	args = append(args, "-c", "features.memories="+value, "-c", "memories.generate_memories="+value)
	if enabled {
		args = append(args, "-c", "memories.use_memories=true")
	}
	return args
}

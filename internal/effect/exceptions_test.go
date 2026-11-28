package effect

// exceptions are the places that still act outside the effect packages, as
// counted by TestBoundary: per kind, the file and how many acts it has.
// Moving them into the packages lowers the numbers; none may rise, and no
// file may be added.
var exceptions = map[kind]map[string]int{
	command: {
		"internal/claudecode/claudecode.go": 1,
		"internal/codex/bridge.go":          2,
		"internal/gitsync/gitsync.go":       1,
		"internal/login/login.go":           1,
		"internal/self/claude.go":           2,
	},
	write: {
		"cmd/lacia/agent.go":                  2,
		"cmd/lacia/agent_schedules.go":        6,
		"cmd/lacia/idle_restart.go":           2,
		"cmd/lacia/init.go":                   5,
		"cmd/lacia/routine_actions.go":        1,
		"cmd/lacia/rules_notice.go":           3,
		"cmd/lacia/serve.go":                  3,
		"cmd/lacia/shims.go":                  5,
		"internal/agent/mirror.go":            4,
		"internal/agent/waits.go":             2,
		"internal/claudecode/claudecode.go":   4,
		"internal/codex/bridge.go":            6,
		"internal/daylog/daylog.go":           2,
		"internal/discord/attachments.go":     6,
		"internal/discord/incoming_images.go": 6,
		"internal/login/login.go":             4,
		"internal/routine/store.go":           5,
		"internal/schedule/runs.go":           2,
		"internal/schedule/worker.go":         2,
		"internal/self/claude.go":             2,
		"internal/task/detail.go":             1,
		"internal/task/files.go":              10,
		"internal/task/history.go":            1,
		"internal/task/routine_task.go":       2,
		"internal/task/store.go":              5,
		"internal/testrecords/records.go":     3,
		"internal/todo/todo.go":               1,
		"internal/web/web.go":                 1,
	},
	network: {},
}

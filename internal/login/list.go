package login

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/imos/lacia/internal/home"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The list of a machine says how each login is started: by a command of
// one's own, by a flag, or by a variable (home.Logins says where it lies).

type Entry struct {
	Label string `json:"label"` // for people, such as "仕事用"
	// Codex and Claude start the agent with this login. What the caller
	// wants to pass on is appended. A login may have either or both.
	Codex  []string `json:"codex"`
	Claude []string `json:"claude"`
	// CodexHome is the directory this login keeps the settings of Codex in
	// (CODEX_HOME). A running conversation says which directory it uses,
	// and that is how its login is told.
	CodexHome string `json:"codex_home"`
	// Unattended lets a conversation nobody watches (the continuation after
	// a wait) start without approvals or a sandbox.
	Unattended bool `json:"unattended"`
}

type List struct {
	// Default is the login used when none is named. Without it, the agent
	// is started as it is installed.
	Default string           `json:"default"`
	Logins  map[string]Entry `json:"logins"`
	// Schedule optionally selects a login by agent kind and conversation.
	Schedule          map[string]ScheduleLogins   `json:"schedule,omitempty"`
	ExecutionProfiles map[string]ExecutionProfile `json:"execution_profiles,omitempty"`
}

// ExecutionProfile is registered on the executing machine, never supplied by a job.
type ExecutionProfile struct {
	Login string   `json:"login"`
	Kind  string   `json:"kind"`
	Args  []string `json:"args,omitempty"`
}

// ValidExecutionProfile accepts only names, never paths or command fragments.
func ValidExecutionProfile(name string) bool {
	return loginName.MatchString(name) && name != "default"
}

// ExecutionCommand resolves a named machine-local profile or preserves the old selection.
func (l *List) ExecutionCommand(profile, kind string, side bool) (name string, command []string, err error) {
	if profile == "" {
		return l.ScheduleCommand(kind, side)
	}
	if !ValidExecutionProfile(profile) {
		return "", nil, fmt.Errorf("実行プロファイルの名前が正しくありません")
	}
	p, ok := l.ExecutionProfiles[profile]
	if !ok {
		return "", nil, fmt.Errorf("実行プロファイル「%s」は、この端末に登録されていません", profile)
	}
	if p.Kind != kind {
		return "", nil, fmt.Errorf("実行プロファイル「%s」は %s 用です", profile, p.Kind)
	}
	command, err = l.Command(p.Login, kind)
	if err != nil {
		return "", nil, err
	}
	return p.Login, append(append([]string{}, command...), p.Args...), nil
}

type ScheduleLogins struct {
	Main string `json:"main"`
	Side string `json:"side"`
}

// reserved are the names that would read as a command of lacia itself
// behind "codex-".
var reserved = map[string]bool{"login": true, "stop": true, "probe": true}

func ListPath(getenv func(string) string) string {
	return home.Logins(getenv).Path()
}

// LoadList reads the list. A machine without the file has no logins to
// choose from, which is not an error.
func LoadList(path string) (*List, error) {
	list := &List{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return list, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, list); err != nil {
		return nil, fmt.Errorf("%s を読めません: %w", path, err)
	}
	for name, entry := range list.Logins {
		if !loginName.MatchString(name) || reserved[name] {
			return nil, fmt.Errorf("%s: ログインの名前「%s」は使えません。英小文字、数字、-、_ で書き、login、stop、probe は避けてください", path, name)
		}
		if len(entry.Codex) == 0 && len(entry.Claude) == 0 {
			return nil, fmt.Errorf("%s: ログイン「%s」に、起動するコマンド（codex か claude）を書いてください", path, name)
		}
	}
	if _, ok := list.Logins[list.Default]; list.Default != "" && !ok {
		return nil, fmt.Errorf("%s: default の「%s」が、logins にありません", path, list.Default)
	}
	for kind, selection := range list.Schedule {
		if kind != "codex" && kind != "claude" {
			return nil, fmt.Errorf("%s: schedule の種類は codex か claude で書いてください", path)
		}
		for _, name := range []string{selection.Main, selection.Side} {
			if name != "" {
				if _, err := list.Command(name, kind); err != nil {
					return nil, fmt.Errorf("%s: schedule: %w", path, err)
				}
			}
		}
	}
	for name, profile := range list.ExecutionProfiles {
		if !ValidExecutionProfile(name) || (profile.Kind != "codex" && profile.Kind != "claude") {
			return nil, fmt.Errorf("%s: 実行プロファイルの名前か種類が正しくありません", path)
		}
		if _, _, err := list.ExecutionCommand(name, profile.Kind, false); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return list, nil
}

// ScheduleCommand selects the configured login without guessing between accounts.
func (l *List) ScheduleCommand(kind string, side bool) (name string, command []string, err error) {
	if kind != "codex" && kind != "claude" {
		return "", nil, fmt.Errorf("予約の実行には codex か claude を指定してください")
	}
	if selection := l.Schedule[kind]; side {
		name = selection.Side
	} else {
		name = selection.Main
	}
	if name != "" {
		command, err = l.Command(name, kind)
		return name, command, err
	}
	return l.DefaultCommand(kind)
}

// DefaultCommand selects one account without guessing among multiple logins.
func (l *List) DefaultCommand(kind string) (name string, command []string, err error) {
	if len(l.Logins) == 0 {
		return "", []string{kind}, nil
	}
	if command, err := l.Command(l.Default, kind); err == nil {
		return l.Default, command, nil
	}
	for candidate := range l.Logins {
		args, err := l.Command(candidate, kind)
		if err != nil {
			continue
		}
		if name != "" {
			return "", nil, fmt.Errorf("%s のログインが複数あります。使うログインの名前を logins.json の default に指定してください", kind)
		}
		name, command = candidate, args
	}
	if name == "" {
		return "", []string{kind}, nil
	}
	return name, command, nil
}

// Names lists the logins, the default one first.
func (l *List) Names() []string {
	var names []string
	for name := range l.Logins {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == l.Default) != (names[j] == l.Default) {
			return names[i] == l.Default
		}
		return names[i] < names[j]
	})
	return names
}

// CommandDirs returns the directories of the explicitly configured absolute
// launchers, in login order. A bare or relative command still uses PATH.
func (l *List) CommandDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	for _, name := range l.Names() {
		entry := l.Logins[name]
		for _, command := range [][]string{entry.Codex, entry.Claude} {
			if len(command) == 0 || !filepath.IsAbs(command[0]) {
				continue
			}
			dir := filepath.Dir(command[0])
			if !seen[dir] {
				dirs = append(dirs, dir)
				seen[dir] = true
			}
		}
	}
	return dirs
}

// Command is how a login starts an agent of a kind ("codex" or "claude").
func (l *List) Command(name, kind string) ([]string, error) {
	entry, ok := l.Logins[name]
	if !ok {
		return nil, fmt.Errorf("ログイン「%s」は、登録されていません（登録済みのログイン: %s）", name, strings.Join(l.Names(), "、"))
	}
	command := entry.Codex
	if kind == "claude" {
		command = entry.Claude
	}
	if len(command) == 0 {
		return nil, fmt.Errorf("ログイン「%s」に、%s を起動するコマンドが書かれていません", name, kind)
	}
	return command, nil
}

// Title is how a login is named to people: its label, or its name.
func (l *List) Title(name string) string {
	if label := l.Logins[name].Label; label != "" {
		return label
	}
	return name
}

package main

import (
	"bufio"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// resolvePath reads a path written in a settings file: "~/" is the home
// directory, and a relative path starts at base.
func resolvePath(base, value string) string {
	if rest, ok := strings.CutPrefix(value, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(base, value)
}

// applyConfig reads the settings of `lacia serve` from a file. A line is
// "name = value", where name is the name of an option without the dashes;
// empty lines and lines that start with # are skipped. An option given on
// the command line wins over the file.
//
// The file is the one place that says how a form of Lacia differs from the
// plain local one: where it listens, what it connects to, which jobs it runs.
//
// A name this binary does not know is reported and skipped, not refused: an
// older binary, put back after a bad build, must still start with the file
// written for the newer one. No option widens what is accepted by being
// absent, so skipping one never opens anything.
//
// A line "[name]" starts a profile: the lines below it hold for that profile
// alone, on top of the common ones above the first such line. The file may
// describe several profiles that run side by side, each in its own process;
// profile names the one this process is. The settings of every profile are
// returned, so that one can look up another, such as the address of its peer.
func applyConfig(fs *flag.FlagSet, path, profile string) (map[string]map[string]string, error) {
	return configSource{path: path, profile: profile}.apply(fs)
}

// recordsConfig is the settings file a task repository carries, relative to
// its root. It is read after the file named by --config, which wins.
const recordsConfig = ".lacia/serve.conf"

// recordsSettings are the options the repository's own file may set. The
// conversations write to the repository, so the file must not be a way for
// them to loosen what holds them: where the server listens, which commands
// it starts, the sandbox, pushing, and what it connects to stay with the
// command line and the file outside the repository.
var recordsSettings = map[string]bool{
	"label": true, "google-client": true, "calendar-write": true, "calendar": true, "calendars": true, "calendar-overrides": true, "calendar-holidays": true,
	"now": true, "self": true, "self-at": true, "self-model": true, "self-exchanges": true,
	"notice-log": true, "instructions-extra": true,
}

// pathSettings name files outside the control of the repository layout, so
// a relative value is taken from the directory of the settings.
var pathSettings = map[string]bool{"google-client": true, "calendar": true, "calendars": true, "instructions-extra": true}

// configSource is one settings file and how far it is trusted.
type configSource struct {
	path, profile string
	// allow limits the options the file may set; nil allows every option.
	allow map[string]bool
	// base, when set, is where relative paths of pathSettings start.
	base string
	// only is used by a command that reads a subset of the server settings.
	only string
}

func (c configSource) apply(fs *flag.FlagSet) (map[string]map[string]string, error) {
	path, profile := c.path, c.profile
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type setting struct {
		name, value string
		line        int
	}
	var mine []setting
	profiles := map[string]map[string]string{}
	section := ""
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" || profiles[section] != nil {
				return nil, fmt.Errorf("%s の%d行目: プロファイル名が空か、同じ名前の節がすでにあります", path, n)
			}
			profiles[section] = map[string]string{}
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			return nil, fmt.Errorf("%s の%d行目を読めません。「名前 = 値」か「[プロファイルの名前]」の形で書いてください", path, n)
		}
		if name == "config" || name == "profile" {
			return nil, fmt.Errorf("%s の%d行目: %s はコマンドラインで指定してください", path, n, name)
		}
		if section != "" {
			profiles[section][name] = value
		}
		// A later line wins, so the profile's own lines go over the common ones.
		if section == "" || section == profile {
			mine = append(mine, setting{name, value, n})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if profile != "" && profiles[profile] == nil {
		return nil, fmt.Errorf("%s に、プロファイル「%s」がありません", path, profile)
	}
	given := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { given[fl.Name] = true })
	for _, s := range mine {
		if c.only != "" && s.name != c.only {
			continue
		}
		switch {
		case fs.Lookup(s.name) == nil:
			slog.Warn("config: unknown setting skipped", "file", path, "line", s.line, "name", s.name)
		case c.allow != nil && !c.allow[s.name]:
			slog.Warn("config: setting not allowed in this file skipped", "file", path, "line", s.line, "name", s.name)
		case !given[s.name]:
			if c.base != "" && pathSettings[s.name] && s.value != "" {
				s.value = resolvePath(c.base, s.value)
			}
			if err := fs.Set(s.name, s.value); err != nil {
				return nil, fmt.Errorf("%s の%d行目（%s）: %w", path, s.line, s.name, err)
			}
		}
	}
	return profiles, nil
}

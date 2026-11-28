package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/home"
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

// serveConfig is the settings of lacia serve as read from every place, and
// where each value came from.
type serveConfig struct {
	options
	// origin names, by option, the place its value came from. An option
	// that is not in it has its default.
	origin   map[string]string
	profiles map[string]map[string]string
	root     string
	// stale is the machine's file read from the place of older versions.
	stale bool
}

// loadServeConfig reads the command line, the file of the machine and the
// records' own file, in that order; an earlier place wins.
func loadServeConfig(args []string) (*serveConfig, error) {
	c := &serveConfig{options: serveOptions(), origin: map[string]string{}}
	if err := c.fs.Parse(args); err != nil {
		return nil, err
	}
	c.fs.Visit(func(f *flag.Flag) { c.origin[f.Name] = "コマンドライン" })
	path, profile := c.str("config"), c.str("profile")
	if machine := home.Settings(os.Getenv); *path == "" && fileExists(machine.Path()) {
		*path, c.stale = machine.Path(), machine.Stale()
		c.origin["config"] = "既定の場所"
	}
	if *path != "" {
		profiles, err := configSource{path: *path, profile: *profile, origin: c.origin}.apply(c.fs)
		if err != nil {
			return nil, err
		}
		c.profiles = profiles
	} else if *profile != "" || *c.str("peer") != "" {
		return nil, errors.New("--profile と --peer は、設定のファイルと一緒に使ってください")
	}
	root, err := findRoot(*c.str("root"))
	if err != nil {
		return nil, err
	}
	c.root = root
	if c.origin["root"] == "" {
		c.origin["root"] = "見つけた場所"
	}
	*c.str("root") = root
	if conf := filepath.Join(root, recordsConfig); fileExists(conf) {
		if _, err := (configSource{path: conf, recordsOnly: true, base: root, origin: c.origin}).apply(c.fs); err != nil {
			return nil, err
		}
	}
	if state := c.str("state"); *state == "" {
		*state = home.State(os.Getenv)
	}
	if at := c.str("self-at"); *at == "" && c.origin["self-at"] == "" {
		*at = nightAt
		if *c.on("inbox") && *c.str("peer") != "" {
			*at = nightAtInbox
		}
	}
	return c, nil
}

// recordsConfig is the settings file a task repository carries, relative to
// its root. It is read after the file of the machine, which wins, and only
// the options the table marks as records may be set in it.
const recordsConfig = ".lacia/serve.conf"

// configSource is one settings file and how far it is trusted.
type configSource struct {
	path, profile string
	// recordsOnly limits the options to those the records may set.
	recordsOnly bool
	// base, when set, is where relative paths of path options start.
	base string
	// only is used by a command that reads a subset of the server settings.
	only string
	// origin, when set, takes the file and line each option was set from.
	origin map[string]string
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
		if opt, ok := settingNamed(name); ok && opt.where == commandLine {
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
			slog.Warn("config: unknown setting skipped", "detail", "この版では使えない設定項目を読み飛ばしました。項目名を確かめてください。", "file", path, "line", s.line, "name", s.name)
		case c.recordsOnly && !recordsMay(s.name):
			slog.Warn("config: setting not allowed in this file skipped", "detail", "このファイルでは指定できない設定項目を読み飛ばしました。", "file", path, "line", s.line, "name", s.name)
		case !given[s.name]:
			if opt, _ := settingNamed(s.name); c.base != "" && opt.path && s.value != "" {
				s.value = resolvePath(c.base, s.value)
			}
			if err := fs.Set(s.name, s.value); err != nil {
				return nil, fmt.Errorf("%s の%d行目（%s）: %w", path, s.line, s.name, err)
			}
			if c.origin != nil {
				c.origin[s.name] = fmt.Sprintf("%s の%d行目", path, s.line)
			}
		}
	}
	return profiles, nil
}

func recordsMay(name string) bool {
	opt, ok := settingNamed(name)
	return ok && opt.where == records
}

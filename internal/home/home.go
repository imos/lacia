// Package home says where the tool keeps its own files on a machine: the
// binary, the settings of the machine, the state and the logs of the server,
// and the source of a clone. They lie under one directory, ~/.lacia, apart
// from the records of the owner (docs/design/settings.md).
//
// Versions before ~/.lacia kept the settings of the machine in
// ~/.config/lacia. A file found only there is still read, so that no machine
// stops working, and Moves says how to bring it over.
package home

import (
	"os"
	"path/filepath"
	"strings"
)

// Dir is the directory of the tool: LACIA_HOME, or ~/.lacia.
func Dir(getenv func(string) string) string {
	if dir := getenv("LACIA_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(getenv("HOME"), ".lacia")
}

// Config holds the settings of the machine, and State what the server keeps
// between starts. bin/, logs/ and src/ lie beside them.
func Config(getenv func(string) string) string { return filepath.Join(Dir(getenv), "config") }
func State(getenv func(string) string) string  { return filepath.Join(Dir(getenv), "state") }

// oldConfig is where versions before ~/.lacia kept the settings of the machine.
func oldConfig(getenv func(string) string) string {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(getenv("HOME"), ".config")
	}
	return filepath.Join(base, "lacia")
}

// File is a settings file of the machine, in the place of this version and
// in the place of the versions before it.
type File struct{ New, Old string }

// Settings is the file of the settings of lacia serve that stay with the
// machine, such as where the records are and which commands start the
// conversations. It was named serve.conf, as the records' own file is.
func Settings(getenv func(string) string) File {
	return File{New: filepath.Join(Config(getenv), "lacia.conf"), Old: filepath.Join(oldConfig(getenv), "serve.conf")}
}

// Logins is the list of the logins of the machine (docs/design/logins.md).
// LACIA_CONFIG_DIR, where it is set, names the one directory to read.
func Logins(getenv func(string) string) File {
	if dir := getenv("LACIA_CONFIG_DIR"); dir != "" {
		path := filepath.Join(dir, "logins.json")
		return File{New: path, Old: path}
	}
	return File{New: filepath.Join(Config(getenv), "logins.json"), Old: filepath.Join(oldConfig(getenv), "logins.json")}
}

// Stale reports whether the file lies only in the old place.
func (f File) Stale() bool {
	return f.Old != f.New && !exists(f.New) && exists(f.Old)
}

// Path is the file to read: the new one, unless only the old one exists.
func (f File) Path() string {
	if f.Stale() {
		return f.Old
	}
	return f.New
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Moves returns the shell commands that bring the settings of the machine
// over from the old place, or nothing when there is nothing to bring. The
// old directory is left as a link to the new one: a server registered to
// start at login names its file by the old path, and so does an older
// binary put back after a bad update.
func Moves(getenv func(string) string) []string {
	settings, logins := Settings(getenv), Logins(getenv)
	if !settings.Stale() && !logins.Stale() {
		return nil
	}
	old, dir := oldConfig(getenv), Config(getenv)
	if exists(dir) {
		// Moving the directory would put it inside the new one.
		return []string{"# " + dir + " がすでにあります。" + old + " の中身を、1つずつ移してください。"}
	}
	moves := []string{
		"mkdir -p " + quote(Dir(getenv)),
		"mv " + quote(old) + " " + quote(dir),
		"ln -s " + quote(dir) + " " + quote(old),
	}
	if settings.Stale() {
		moves = append(moves, "mv "+quote(filepath.Join(dir, "serve.conf"))+" "+quote(settings.New)+" && ln -s lacia.conf "+quote(filepath.Join(dir, "serve.conf")))
	}
	return moves
}

// quote makes a path one word for the shell.
func quote(path string) string {
	if !strings.ContainsAny(path, " '\"\\$`!&;|<>()*?[]#~{}") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

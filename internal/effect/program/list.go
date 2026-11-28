package program

import (
	"os"
	"path/filepath"
)

// This file lists every program lacia may start. A Program can only be made
// here, and no other package imports os/exec (internal/effect/boundary_test.go).

// System tools are started by absolute path, so PATH cannot replace them.
func Caffeinate() Program  { return Program{"/usr/bin/caffeinate"} }
func Launchctl() Program   { return Program{"/bin/launchctl"} }
func XcodeSelect() Program { return Program{"/usr/bin/xcode-select"} }
func Open() Program        { return Program{"/usr/bin/open"} }
func Curl() Program        { return Program{"/usr/bin/curl"} }
func Sh() Program          { return Program{"/bin/sh"} }
func Make() Program        { return Program{"/usr/bin/make"} }
func Python3() Program     { return Program{"/usr/bin/python3"} }

// Git is found in PATH, as it always has been: owners install newer versions.
func Git() Program { return Program{"git"} }

// The owner chooses where these are installed. The path comes from a flag
// or a setting outside the records, or is the bare name to find in PATH.
func Kubectl(path string) Program    { return Program{path} }
func Codex(path string) Program      { return Program{path} }
func ClaudeCode(path string) Program { return Program{path} }

// AuthCheck is the command the owner configures to run before scheduled
// work, to see whether a login has expired.
func AuthCheck(path string) Program { return Program{path} }

// Lacia is this binary, or the replacement a deployment has put in its place.
func Lacia(path string) Program { return Program{path} }

// Shell is the owner's login shell, opened in the records by lacia shell.
func Shell() Program {
	if shell := os.Getenv("SHELL"); filepath.IsAbs(shell) {
		return Program{shell}
	}
	return Program{"/bin/sh"}
}

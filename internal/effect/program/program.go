// Package program is the only package in lacia that starts other programs.
// The programs it may start are listed in list.go.
package program

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// Program is one entry of list.go. Other packages make one only through the
// constructors there, so every start names what the program is for.
type Program struct{ name string }

// Cmd lets other packages hold a running program without importing os/exec.
type Cmd = exec.Cmd

func (p Program) Name() string { return p.name }

// Command prepares the program to run. The caller sets the directory, the
// environment and the streams on the returned command.
func (p Program) Command(ctx context.Context, args ...string) *Cmd {
	return exec.CommandContext(ctx, p.name, args...)
}

// Output runs the program in dir and returns what it printed. The error
// names the program, its arguments and what it printed on stderr, so the
// arguments must be fit to show in a log.
func (p Program) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := p.Command(ctx, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		err = fmt.Errorf("%s %s: %w", p.name, strings.Join(args, " "), err)
		if message := strings.TrimSpace(stderr.String()); message != "" {
			err = fmt.Errorf("%w: %s", err, message)
		}
	}
	return out, err
}

// Find reports where the program is, or why it cannot be started.
func (p Program) Find() (string, error) { return exec.LookPath(p.name) }

// Replace runs the program in place of this process, as a shell's exec
// does. It returns only when the program could not be started.
func (p Program) Replace(args, env []string) error {
	path, err := p.Find()
	if err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{p.name}, args...), env)
}

// ExitCode reports the exit code when err says the program ran and failed.
func ExitCode(err error) (int, bool) {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), true
	}
	return 0, false
}

package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// shims are the small commands a conversation runs beside lacia itself:
// the hook that refuses a worker's pushes (docs/design/logins.md), and
// lacia-browse (docs/design/safety.md).
// They travel inside the binary, so every form of Lacia has the same ones.
//
//go:embed shims
var shims embed.FS

// writeShims puts the commands into dir, pointing them at the binary self.
// Each file is replaced in one step: a script that is running keeps reading
// the copy it started with.
func writeShims(dir, self string) error {
	entries, err := shims.ReadDir("shims")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		data, err := shims.ReadFile("shims/" + e.Name())
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), "@LACIA@", self)
		target := filepath.Join(dir, e.Name())
		if old, err := os.ReadFile(target); err == nil && string(old) == text {
			continue
		}
		tmp, err := os.CreateTemp(dir, "."+e.Name()+".*")
		if err != nil {
			return err
		}
		_, err = tmp.WriteString(text)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Chmod(tmp.Name(), 0o755)
		}
		if err == nil {
			err = os.Rename(tmp.Name(), target)
		}
		if err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	return nil
}

// runShims writes the commands into a directory of the caller's choice, for
// a machine where no server does it.
func runShims(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("使い方: lacia shims <置き場所のディレクトリー>")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if err := writeShims(dir, self); err != nil {
		return err
	}
	fmt.Printf("%s にコマンドを置きました。PATH に足すと、lacia-browse などが使えます。\n", dir)
	return nil
}

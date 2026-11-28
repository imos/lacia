package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/effect/disk"
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
func writeShims(dir *disk.Dir, self string) error {
	entries, err := shims.ReadDir("shims")
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, err := shims.ReadFile("shims/" + e.Name())
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), "@LACIA@", self)
		if old, err := dir.ReadFile(e.Name()); err == nil && string(old) == text {
			continue
		}
		if err := dir.WriteFileNoSync(e.Name(), []byte(text), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// runShims writes the commands into a directory of the caller's choice, for
// a machine where no server does it.
func runShims(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return usageError("shims")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	dir, err := disk.OpenShimsDir(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := writeShims(dir, self); err != nil {
		return err
	}
	fmt.Printf("%s にコマンドを置きました。PATH に足すと、lacia-browse などが使えます。\n", path)
	return nil
}

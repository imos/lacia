package main

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/imos/lacia/internal/effect/disk"
)

// maxServeLog is how large the server's log may grow before it is set
// aside at the next start.
const maxServeLog = 10 << 20

// rotateServeLog sets the log aside as serve.log.1, replacing the one set
// aside before, when the outputs write into logs/serve.log of the tools
// directory and it has grown past max. launchd opens the file for the
// server, so the server points its outputs at the new file itself. Output
// sent anywhere else is left alone.
func rotateServeLog(tools *disk.Dir, max int64, outputs ...*os.File) error {
	const name, old = "logs/serve.log", "logs/serve.log.1"
	info, err := os.Stat(tools.Path() + "/" + name)
	if err != nil || info.Size() < max {
		return nil
	}
	for _, out := range outputs {
		if mine, err := out.Stat(); err != nil || !os.SameFile(info, mine) {
			return nil
		}
	}
	if err := tools.Rename(name, old); err != nil {
		return err
	}
	f, err := tools.OpenAppend(name, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, out := range outputs {
		if err := unix.Dup2(int(f.Fd()), int(out.Fd())); err != nil {
			return err
		}
	}
	return nil
}

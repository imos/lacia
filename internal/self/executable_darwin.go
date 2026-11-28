package self

import (
	"bytes"
	"errors"

	"golang.org/x/sys/unix"
)

// executableOf is the path the process was started from, as it was given,
// so a symbolic link stays one.
func executableOf(pid int) (string, error) {
	args, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", err
	}
	if len(args) < 5 {
		return "", errors.New("kern.procargs2 is too short")
	}
	path, _, _ := bytes.Cut(args[4:], []byte{0})
	return string(path), nil
}

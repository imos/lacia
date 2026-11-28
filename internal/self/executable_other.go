//go:build !darwin

package self

import (
	"os"
	"strconv"
)

func executableOf(pid int) (string, error) {
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
}

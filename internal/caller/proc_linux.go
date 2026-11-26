package caller

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func lookup(pid int) (process, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return process{}, err
	}
	p := process{pid: pid}
	var ok bool
	if p.parent, p.started, ok = parseStat(string(stat)); !ok {
		return process{}, errors.New("プロセスの状態を読み取れません")
	}
	p.exe, _ = os.Readlink(dir + "/exe")
	if cmdline, err := os.ReadFile(dir + "/cmdline"); err == nil && len(cmdline) > 0 {
		p.args = strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	}
	return p, nil
}

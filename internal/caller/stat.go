package caller

import (
	"strconv"
	"strings"
)

// parseStat reads the parent PID and the start time from /proc/<pid>/stat.
// The command name sits in parentheses and may itself contain spaces and
// parentheses, so the fields are counted from the last ")".
func parseStat(stat string) (parent int, started int64, ok bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(stat[end+1:])
	// fields[0] is the state (field 3); the parent is field 4 and the start
	// time field 22.
	if len(fields) < 20 {
		return 0, 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	started, err = strconv.ParseInt(fields[19], 10, 64)
	return parent, started, err == nil
}

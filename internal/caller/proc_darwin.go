package caller

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func lookup(pid int) (process, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return process{}, err
	}
	if int(info.Proc.P_pid) != pid {
		return process{}, errors.New("そのプロセスは見つかりません")
	}
	p := process{pid: pid, parent: int(info.Eproc.Ppid), started: info.Proc.P_starttime.Sec}
	// Processes of other users refuse this; the name is then all there is.
	if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil {
		p.exe, p.args = parseArgs(raw)
	}
	if p.exe == "" {
		p.exe = unix.ByteSliceToString(info.Proc.P_comm[:])
	}
	return p, nil
}

// parseArgs reads the argument count, the program path and the arguments
// from a kern.procargs2 buffer. The environment follows the arguments in
// the same buffer; it is not read.
func parseArgs(raw []byte) (exe string, args []string) {
	if len(raw) < 4 {
		return "", nil
	}
	count := int(binary.LittleEndian.Uint32(raw))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return string(rest), nil
	}
	exe, rest = string(rest[:end]), bytes.TrimLeft(rest[end:], "\x00")
	for ; count > 0 && len(rest) > 0; count-- {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			args = append(args, string(rest))
			break
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return exe, args
}

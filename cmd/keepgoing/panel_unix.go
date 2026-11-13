//go:build unix

package main

import (
	"os"
	"strconv"
	"syscall"
)

// panelRestrictUmask makes files created until the returned function is
// called private to this user from the moment they exist. A socket takes its
// mode from the umask at bind, so this is what keeps it from being reachable
// by others even for an instant.
func panelRestrictUmask() (restore func()) {
	previous := syscall.Umask(0o077)
	return func() { syscall.Umask(previous) }
}

// panelCloseInheritedOnExec marks every descriptor this process holds beyond
// stdin, stdout and stderr to be closed in a program it starts. Go's own
// descriptors already are; ones inherited from whatever started keepgoing
// (a shell's redirections, an editor's pipes) are not, and the background
// server must not keep them open.
func panelCloseInheritedOnExec() {
	limit := 4096
	var rl syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl) == nil && rl.Cur > 0 && rl.Cur < uint64(limit) {
		limit = int(rl.Cur)
	}
	if entries, err := os.ReadDir("/dev/fd"); err == nil {
		for _, e := range entries {
			if fd, err := strconv.Atoi(e.Name()); err == nil && fd > 2 {
				syscall.CloseOnExec(fd)
			}
		}
		return
	}
	for fd := 3; fd < limit; fd++ {
		syscall.CloseOnExec(fd)
	}
}

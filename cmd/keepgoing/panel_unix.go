//go:build unix

package main

import (
	"os"
	"strconv"
	"syscall"
)

// panelRestrictUmask makes every file the server creates, its socket
// included, private from the moment it exists.
func panelRestrictUmask() { syscall.Umask(0o077) }

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

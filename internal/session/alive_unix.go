//go:build unix

package session

import "syscall"

// alive reports whether a process exists. A process of another user counts.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

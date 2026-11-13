//go:build unix

package session

import (
	"os"
	"syscall"
)

// alive reports whether a process exists. A process of another user counts.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// ownedByMe reports whether a file belongs to the user running keepgoing.
func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// heldByOther reports whether some process holds the claim lock at path. It
// asks for a shared lock without waiting, and lets it go at once.
func heldByOther(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return err == syscall.EWOULDBLOCK
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

//go:build darwin

package session

import (
	"syscall"
	"unsafe"
)

// canonical returns the file system's own path for an existing directory or
// file. On a case-insensitive volume "lab4/project" and "LAB4/PROJECT" name
// the same directory; the name stored on disk is what both resolve to.
func canonical(path string) string {
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		return path
	}
	defer syscall.Close(fd)
	const fGetPath = 50 // F_GETPATH
	buf := make([]byte, 1024)
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fGetPath, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return path
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return path
}

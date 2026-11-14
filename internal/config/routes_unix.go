//go:build unix

package config

import (
	"os"
	"syscall"
)

// openRoutesFile opens the list for reading without blocking, so a FIFO or
// device at the path cannot hang a check.
func openRoutesFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func ownedByUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

func privateMode(info os.FileInfo) bool { return info.Mode().Perm()&0o077 == 0 }

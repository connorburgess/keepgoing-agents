//go:build !windows

package state

import (
	"os"
	"syscall"
)

func fileIDs(info os.FileInfo) (uint64, uint64, uint64, bool) {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, false
	}
	return uint64(s.Dev), uint64(s.Ino), uint64(s.Nlink), true
}

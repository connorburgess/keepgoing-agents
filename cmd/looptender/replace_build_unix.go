//go:build !windows

package main

import (
	"os"
	"syscall"
)

// fileOwnedByUser says whether this user owns the file.
func fileOwnedByUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

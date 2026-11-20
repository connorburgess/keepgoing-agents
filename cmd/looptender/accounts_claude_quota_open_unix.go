//go:build darwin || linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// A replaced selection must not turn admission into a blocking pipe/device open.
// The caller fstats the returned handle before reading; symlinks are refused.
func accountClaudeQuotaOpen(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

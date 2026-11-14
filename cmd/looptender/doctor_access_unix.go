//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func doctorOSAccess(path string, mode uint32) error {
	// access(2) checks real credentials. Restrict it to ordinary identities
	// whose real/effective IDs match; do not claim to check set-ID or root
	// capability semantics. The kernel evaluates ACLs as part of this check,
	// but this does not audit who else has access to the path.
	if os.Getuid() == 0 || os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() {
		return errAccessUnavailable
	}
	return syscall.Access(path, mode)
}

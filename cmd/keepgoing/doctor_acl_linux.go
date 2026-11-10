//go:build linux

package main

import (
	"errors"
	"syscall"
)

func doctorACLMetadata(path string, directory bool) aclMetadata {
	return linuxACLMetadata(path, directory, func(path, name string) (int, error) {
		// A nil buffer asks for size only: ACL values and principal IDs are
		// never loaded. getxattr follows the configured path like doctorFile.
		return syscall.Getxattr(path, name, nil)
	})
}

func linuxACLMetadata(path string, directory bool, size func(string, string) (int, error)) aclMetadata {
	query := func(name string) string {
		n, err := size(path, name)
		switch {
		case errors.Is(err, syscall.ENODATA):
			// ENODATA can mean absent OR inaccessible; never claim private.
			return aclNotReported
		case err != nil || n <= 0:
			return aclUnavailable
		default:
			return aclPresent
		}
	}
	a := aclMetadata{Supported: true, Access: query("system.posix_acl_access")}
	if directory {
		a.Default = query("system.posix_acl_default")
	}
	return a
}

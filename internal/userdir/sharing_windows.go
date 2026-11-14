//go:build windows

package userdir

import (
	"errors"
	"syscall"
)

// isSharingViolation reports ERROR_SHARING_VIOLATION and ERROR_LOCK_VIOLATION:
// another program has a file open in the directory.
func isSharingViolation(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == 32 || errno == 33)
}

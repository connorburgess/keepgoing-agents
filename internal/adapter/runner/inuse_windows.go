package runner

import (
	"errors"
	"syscall"
)

// openElsewhere is ERROR_SHARING_VIOLATION: the file is open in a process
// that did not allow it to be moved.
func openElsewhere(err error) bool { return errors.Is(err, syscall.Errno(32)) }

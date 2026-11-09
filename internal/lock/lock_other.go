//go:build !unix && !windows

package lock

import (
	"errors"
	"os"
)

func lockFile(*os.File) error   { return errors.New("locking is not supported on this platform") }
func unlockFile(*os.File) error { return nil }

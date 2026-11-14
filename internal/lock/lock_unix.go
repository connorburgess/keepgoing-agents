//go:build unix

package lock

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(f *os.File) error       { return flock(f, syscall.LOCK_EX) }
func lockFileShared(f *os.File) error { return flock(f, syscall.LOCK_SH) }
func flock(f *os.File, mode int) error {
	err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrHeld
	}
	return err
}

func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

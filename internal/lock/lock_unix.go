//go:build unix

// Package lock gives one tick at a time ownership of a loop's state
// directory. It uses an advisory flock that the kernel releases when the
// holder exits, so a crashed tick never leaves a stale lock and nothing
// ever has to steal one by age.
package lock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("lock held by another process")

// Lock is a held lock.
type Lock struct{ f *os.File }

// Acquire takes the lock without waiting.
func Acquire(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "tick.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	cerr := l.f.Close()
	l.f = nil
	if err != nil {
		return err
	}
	return cerr
}

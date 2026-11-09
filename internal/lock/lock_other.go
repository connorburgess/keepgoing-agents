//go:build !unix

package lock

import "errors"

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("lock held by another process")

// Lock is a held lock.
type Lock struct{}

// Acquire is not yet supported on this platform.
func Acquire(stateDir string) (*Lock, error) {
	return nil, errors.New("locking is not supported on this platform yet")
}

// Release drops the lock.
func (l *Lock) Release() error { return nil }

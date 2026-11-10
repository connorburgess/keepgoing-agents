// Package lock gives one process at a time ownership of a named lock in a
// loop's state directory. The operating system releases the lock when the
// holder exits, so a crashed process never leaves a stale lock and nothing
// ever has to steal one by age.
package lock

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("lock held by another process")

// Names of the locks keepgoing uses.
const (
	Tick    = "tick"
	Daemon  = "daemon"
	Control = "control"
	Service = "service"
)

// Lock is a held lock.
type Lock struct{ f *os.File }

// Acquire takes the tick lock without waiting.
func Acquire(stateDir string) (*Lock, error) { return AcquireNamed(stateDir, Tick) }

// AcquireNamed takes the named lock without waiting.
func AcquireNamed(stateDir, name string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	cerr := l.f.Close()
	l.f = nil
	if err != nil {
		return err
	}
	return cerr
}

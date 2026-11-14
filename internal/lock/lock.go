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

// Held reports whether a process holds the named lock in dir right now. It
// creates nothing: a directory or lock file that does not exist is not
// held. It takes the lock for an instant to find out, so a check that
// starts at that instant may skip; use it only where that is acceptable.
// An error means it could not tell.
func Held(dir, name string) (bool, error) {
	return HeldFile(filepath.Join(dir, name+".lock"))
}

// HeldFile is Held for a lock file given by its path.
func HeldFile(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		if errors.Is(err, ErrHeld) {
			return true, nil
		}
		return false, err
	}
	return false, unlockFile(f)
}

// HoldFile takes the lock in an existing lock file without waiting and
// without creating anything. It returns ErrHeld when another holder has it,
// and an error satisfying os.ErrNotExist when there is no such file.
func HoldFile(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

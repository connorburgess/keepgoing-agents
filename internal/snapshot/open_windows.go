package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Open opens path read-only. The handle allows read/write/delete sharing, so
// holding it does not block Replace. It remains the old snapshot afterwards.
//
// While another process is replacing path, Windows can briefly refuse to open
// it with an access-denied or sharing error. Only those two codes are retried,
// a few times over about thirty milliseconds; a lasting denial is returned
// after that short delay, and any other error at once.
func Open(path string) (*os.File, error) {
	name, err := snapshotWindowsPath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// No create, truncate, write access, privilege adjustment or inheritable
	// handle. DELETE sharing permits replacement; it does not grant delete access.
	handle, err := openWithRetry(func() (syscall.Handle, error) {
		return syscall.CreateFile(&name[0], syscall.GENERIC_READ,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	}, time.Sleep)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EINVAL}
	}
	return file, nil
}

const openAttempts = 5

func openWithRetry(open func() (syscall.Handle, error), sleep func(time.Duration)) (syscall.Handle, error) {
	delay := 2 * time.Millisecond
	for attempt := 1; ; attempt++ {
		handle, err := open()
		if err == nil || attempt == openAttempts || !replacementInProgress(err) {
			return handle, err
		}
		sleep(delay)
		delay *= 2
	}
}

// replacementInProgress classifies by Windows error code, never by text.
// replacementInProgress reports the answers Windows can give a reader that
// arrives while another process replaces the file: access denied, a sharing
// violation, or the file not found at all. A record a reader finds missing
// is otherwise believed absent and read as new, so "not found" is retried
// the same bounded way before it is believed (CI saw an existing state.json
// reported missing during its replacement). A missing directory is not: no
// replacement makes the directory vanish.
func replacementInProgress(err error) bool {
	var code syscall.Errno
	return errors.As(err, &code) && (code == syscall.ERROR_ACCESS_DENIED || code == 32 /* ERROR_SHARING_VIOLATION */ || code == syscall.ERROR_FILE_NOT_FOUND)
}

// Lstat is os.Lstat for a record that is replaced by rename. A "not found"
// answer is asked again, with openWithRetry's backoff (about 30 ms in all),
// before it is returned, so an existing record is not taken for a missing
// one while it is being replaced.
func Lstat(path string) (os.FileInfo, error) {
	return lstatWithRetry(func() (os.FileInfo, error) { return os.Lstat(path) }, time.Sleep)
}

func lstatWithRetry(lstat func() (os.FileInfo, error), sleep func(time.Duration)) (os.FileInfo, error) {
	delay := 2 * time.Millisecond
	for attempt := 1; ; attempt++ {
		info, err := lstat()
		if err == nil || attempt == openAttempts || !missingFile(err) {
			return info, err
		}
		sleep(delay)
		delay *= 2
	}
}

// missingFile is a "not found" for the file itself, not for its directory.
func missingFile(err error) bool {
	var code syscall.Errno
	return errors.As(err, &code) && code == syscall.ERROR_FILE_NOT_FOUND
}

func snapshotWindowsPath(path string) ([]uint16, error) {
	// CreateFile does not apply os.Open's extended-path conversion. Preserve
	// support for long local/UNC paths, including relative and non-ASCII inputs.
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if len(absolute) >= 248 && !strings.HasPrefix(absolute, `\\?\`) && !strings.HasPrefix(absolute, `\\.\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + absolute[2:]
		} else {
			absolute = `\\?\` + absolute
		}
	}
	return syscall.UTF16FromString(absolute)
}

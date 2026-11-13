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
	return open(path, syscall.FILE_ATTRIBUTE_NORMAL)
}

// OpenRegular opens path as Open does, but only when it is a regular file on
// disk: a directory, a device or a pipe is refused. With noFollow a symbolic
// link or other reparse point is refused as well, not followed. The check is
// made on the opened handle, so a file swapped in after an earlier Lstat
// cannot slip past it.
func OpenRegular(path string, noFollow bool) (*os.File, error) {
	attrs := uint32(syscall.FILE_ATTRIBUTE_NORMAL)
	if noFollow {
		attrs |= syscall.FILE_FLAG_OPEN_REPARSE_POINT
	}
	f, err := open(path, attrs)
	if err != nil {
		return nil, err
	}
	handle := syscall.Handle(f.Fd())
	// A device or pipe is refused on its type, before asking for file
	// information it may not have.
	kind, err := syscall.GetFileType(handle)
	if err == nil && kind != syscall.FILE_TYPE_DISK {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	var info syscall.ByHandleFileInformation
	if err == nil {
		err = syscall.GetFileInformationByHandle(handle, &info)
	}
	if err != nil {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	notRegular := uint32(syscall.FILE_ATTRIBUTE_DIRECTORY)
	if noFollow {
		notRegular |= syscall.FILE_ATTRIBUTE_REPARSE_POINT
	}
	if info.FileAttributes&notRegular != 0 {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return f, nil
}

func open(path string, attrs uint32) (*os.File, error) {
	name, err := snapshotWindowsPath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// No create, truncate, write access, privilege adjustment or inheritable
	// handle. DELETE sharing permits replacement; it does not grant delete access.
	handle, err := openWithRetry(func() (syscall.Handle, error) {
		return syscall.CreateFile(&name[0], syscall.GENERIC_READ,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.OPEN_EXISTING, attrs, 0)
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
func replacementInProgress(err error) bool {
	var code syscall.Errno
	return errors.As(err, &code) && (code == syscall.ERROR_ACCESS_DENIED || code == 32 /* ERROR_SHARING_VIOLATION */)
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

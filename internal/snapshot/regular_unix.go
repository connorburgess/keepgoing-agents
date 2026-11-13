//go:build unix

package snapshot

import (
	"errors"
	"os"
	"syscall"
)

// OpenRegular opens path read-only, as Open does, but only when it is a
// regular file, and it never waits: the open does not block, so a named pipe
// or a device put in its place is refused at once instead of hanging the
// caller until a writer appears. With noFollow a symbolic link is refused as
// well, not followed. The check is made on the opened descriptor, so a file
// swapped in after an earlier Lstat cannot slip past it.
func OpenRegular(path string, noFollow bool) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	if noFollow {
		flags |= syscall.O_NOFOLLOW
	}
	var fd int
	var err error
	for {
		fd, err = syscall.Open(path, flags, 0)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		if noFollow && errors.Is(err, syscall.ELOOP) {
			err = ErrNotRegular
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return nil, &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		syscall.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	// Reads of a regular file are ordinary blocking reads.
	if err := syscall.SetNonblock(fd, false); err != nil {
		syscall.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

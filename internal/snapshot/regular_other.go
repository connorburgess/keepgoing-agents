//go:build !unix && !windows

package snapshot

import "os"

// OpenRegular opens path read-only only when it is a regular file.
func OpenRegular(path string, noFollow bool) (*os.File, error) {
	if noFollow {
		if info, err := os.Lstat(path); err != nil {
			return nil, err
		} else if !info.Mode().IsRegular() {
			return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return f, nil
}

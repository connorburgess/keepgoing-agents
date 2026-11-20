package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ExecutableImage binds a process's startup pathname to the opened image.
// Keeping the handle prevents its inode from being recycled during the process.
// A check must never discover a fresh pathname identity and attach its old label.
type ExecutableImage struct {
	Path     string
	Identity FileIdentity
	file     *os.File
	sha      string
}

func CaptureExecutableImage() *ExecutableImage {
	path, e := os.Executable()
	if e != nil || !filepath.IsAbs(path) {
		return nil
	}
	f, e := os.Open(path)
	if e != nil {
		return nil
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil
	}
	identity, ok := IdentityOf(info)
	if !ok {
		f.Close()
		return nil
	}
	sum, e := executableDigest(f)
	if e != nil {
		f.Close()
		return nil
	}
	after, e := f.Stat()
	if e != nil {
		f.Close()
		return nil
	}
	afterID, ok := IdentityOf(after)
	if !ok || afterID != identity {
		f.Close()
		return nil
	}
	return &ExecutableImage{Path: path, Identity: identity, file: f, sha: sum}
}
func (image *ExecutableImage) Record(dir, label string, at time.Time) error {
	if image == nil || image.file == nil {
		return ErrCheckExecutable
	}
	info, e := image.file.Stat()
	if e != nil {
		return ErrCheckExecutable
	}
	id, ok := IdentityOf(info)
	if !ok || id != image.Identity {
		return ErrCheckExecutable
	}
	if sum, e := executableDigest(image.file); e != nil || sum != image.sha {
		return ErrCheckExecutable
	}
	return SaveCheckExecutable(dir, image.Path, label, at, image.Identity)
}

const maxExecutableImage = 128 << 20

func executableDigest(f *os.File) (string, error) {
	h := sha256.New()
	n, e := io.Copy(h, io.NewSectionReader(f, 0, maxExecutableImage+1))
	if e != nil || n == 0 || n > maxExecutableImage {
		return "", errors.New("executing image unavailable")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExecutableReader is a borrowed read-only view. Close never closes the
// retained process handle, which stays live until process exit.
type ExecutableReader struct{ file *os.File }

func (r *ExecutableReader) ReadAt(p []byte, off int64) (int, error) { return r.file.ReadAt(p, off) }
func (r *ExecutableReader) Stat() (os.FileInfo, error)              { return r.file.Stat() }
func (r *ExecutableReader) Close() error                            { return nil }
func (image *ExecutableImage) OpenReader() (*ExecutableReader, error) {
	if image == nil || image.file == nil {
		return nil, ErrCheckExecutable
	}
	info, e := image.file.Stat()
	if e != nil {
		return nil, e
	}
	id, ok := IdentityOf(info)
	if !ok || id != image.Identity {
		return nil, ErrCheckExecutable
	}
	sum, e := executableDigest(image.file)
	if e != nil || sum != image.sha {
		return nil, ErrCheckExecutable
	}
	return &ExecutableReader{file: image.file}, nil
}

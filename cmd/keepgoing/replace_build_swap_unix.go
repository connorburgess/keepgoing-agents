//go:build !windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/state"
	"golang.org/x/sys/unix"
)

type buildTarget struct {
	dir           *os.File
	path, name    string
	config, scope string
	identity      state.FileIdentity
	mode          os.FileMode
}

func openBuildTarget(path string, want state.FileIdentity, configPath ...string) (*buildTarget, error) {
	parent, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	fd, e := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	var config, scope string
	if len(configPath) > 0 {
		config = configPath[0]
		resolved, e := filepath.EvalSymlinks(config)
		if e != nil {
			unix.Close(fd)
			return nil, e
		}
		scope = filepath.Dir(resolved)
		rel, e := filepath.Rel(scope, parent)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			unix.Close(fd)
			return nil, errors.New("target moved outside configuration directory")
		}
	}
	t := &buildTarget{config: config, scope: scope, dir: os.NewFile(uintptr(fd), parent), path: parent, name: filepath.Base(path), identity: want}
	if e = t.verify(); e != nil {
		t.close()
		return nil, e
	}
	return t, nil
}
func (t *buildTarget) close() { t.dir.Close() }
func (t *buildTarget) open() (*os.File, error) {
	fd, e := unix.Openat(int(t.dir.Fd()), t.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), t.name), nil
}
func (t *buildTarget) verify() error {
	canonical, e := filepath.EvalSymlinks(t.path)
	if e != nil || canonical != t.path {
		return errors.New("directory path changed")
	}
	if t.config != "" {
		cfg, e := filepath.EvalSymlinks(t.config)
		if e != nil || filepath.Dir(cfg) != t.scope {
			return errors.New("configuration directory changed")
		}
	}

	d, e := os.Stat(t.path)
	own, e2 := t.dir.Stat()
	if e != nil || e2 != nil || !os.SameFile(d, own) {
		return errors.New("directory changed")
	}
	f, e := t.open()
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	id, ok := state.IdentityOf(info)
	if !ok || id != t.identity || !info.Mode().IsRegular() || id.Nlink != 1 {
		return errors.New("target changed")
	}
	t.mode = info.Mode()
	return nil
}
func (t *buildTarget) read() ([]byte, error) {
	f, e := t.open()
	if e != nil {
		return nil, e
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, serviceBinaryLimit+1))
	if e != nil || int64(len(data)) > serviceBinaryLimit {
		return nil, errors.New("target unreadable")
	}
	if e = t.verify(); e != nil {
		return nil, e
	}
	return data, nil
}
func (t *buildTarget) swap(ctx context.Context, data []byte) error {
	var random [16]byte
	if _, e := rand.Read(random[:]); e != nil {
		return e
	}
	name := ".keepgoing-replacing-" + hex.EncodeToString(random[:])
	fd, e := unix.Openat(int(t.dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	defer unix.Unlinkat(int(t.dir.Fd()), name, 0)
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Chmod(t.mode.Perm()); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = t.verify(); e != nil {
		return e
	}
	// Rename only relative to the verified, retained directory. A renamed or
	// substituted ancestor cannot redirect this operation to a different tree.
	if e = unix.Renameat(int(t.dir.Fd()), name, int(t.dir.Fd()), t.name); e != nil {
		return e
	}
	_ = t.dir.Sync()
	return nil
}

func (t *buildTarget) stat() (os.FileInfo, error) {
	f, e := t.open()
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return f.Stat()
}

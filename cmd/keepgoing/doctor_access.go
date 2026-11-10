package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

var errAccessUnavailable = errors.New("OS access check unavailable for this platform or identity")

const (
	accessWrite  uint32 = 2
	accessRead   uint32 = 4
	accessSearch uint32 = 1
)

// doctorStateAccess asks the OS about permissions without opening files for
// writing, creating a directory, acquiring locks, or altering control holds.
// Atomic records need directory access; only append/lock files need direct
// file access. An access check is not a guarantee that a later write succeeds.
func doctorStateAccess(r *doctorReport, dir string, check func(string, uint32) error) {
	const id = "state.write_access"
	p, err := filepath.Abs(dir)
	if err != nil {
		r.add(id, "unavailable", "Runtime directory path could not be resolved for an access check.", "")
		return
	}
	missing := false
	for {
		info, err := os.Stat(p)
		if err == nil {
			if !info.IsDir() {
				r.add(id, "error", "Runtime directory or an ancestor is not a directory.", "Review the configured state directory.")
				return
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			doctorAccessResult(r, id, err, "")
			return
		}
		// Do not climb past a dangling symlink and claim its parent can
		// create the configured directory through that broken link.
		if _, linkErr := os.Lstat(p); linkErr == nil {
			r.add(id, "error", "Runtime directory path contains an unresolved symbolic link.", "Review the configured state directory and its symlink targets.")
			return
		} else if !errors.Is(linkErr, os.ErrNotExist) {
			doctorAccessResult(r, id, linkErr, "")
			return
		}
		parent := filepath.Dir(p)
		if parent == p {
			r.add(id, "unavailable", "No existing ancestor was found for the runtime directory.", "")
			return
		}
		p, missing = parent, true
	}
	message := "The OS permits write and search access to the runtime directory for this caller. No write was attempted."
	if missing {
		message = "Runtime directory is missing; the OS permits write and search access to its nearest existing ancestor. Directory creation was not attempted."
	}
	doctorAccessResult(r, id, check(p, accessWrite|accessSearch), message)
	if missing {
		return
	}
	for _, file := range []struct {
		name string
		mode uint32
	}{{state.LogName, accessWrite}, {"tick.lock", accessRead | accessWrite}, {"daemon.lock", accessRead | accessWrite}} {
		path := filepath.Join(p, file.name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // Creating these files is covered by the directory check.
		}
		fileID := "state." + file.name + ".write_access"
		if err != nil {
			doctorAccessResult(r, fileID, err, "")
			continue
		}
		if !info.Mode().IsRegular() {
			r.add(fileID, "unavailable", "Runtime append/lock path is not a regular file; its access was not checked.", "Review the path; this check does not follow append/lock symlinks.")
			continue
		}
		doctorAccessResult(r, fileID, check(path, file.mode), "The OS permits the access needed to open this runtime append/lock file for this caller. No write or lock was attempted.")
	}
}

func doctorAccessResult(r *doctorReport, id string, err error, success string) {
	switch {
	case err == nil:
		r.add(id, "ok", success, "Permission checks cannot guarantee future writes, available space, atomic replacement, or lock acquisition; a scheduler may use a different identity.")
	case errors.Is(err, errAccessUnavailable):
		r.add(id, "unavailable", "Read-only OS access checks are supported only for ordinary matching real/effective non-root identities on macOS and Linux.", "Run doctor as the same ordinary user as the supervisor. Windows effective write access and privileged-identity checks are not implemented; DACL metadata is inspected separately.")
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EROFS), errors.Is(err, syscall.ENOTDIR):
		r.add(id, "error", "The OS denied required runtime path access, or the path is read-only or has a non-directory ancestor.", "Review runtime path permissions and mount settings for the supervisor's user. Doctor never changes them.")
	default:
		r.add(id, "unavailable", "Runtime path access could not be established; filesystem error details are withheld.", "Review the path in the supervisor's execution context and retry.")
	}
}

// Package control owns control.json, the operator's gate. A missing file
// means disabled: the supervisor must never wake an agent nobody asked for.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// FileName of the control file inside the state directory.
const FileName = "control.json"

// File is the on-disk control record.
type File struct {
	PendingTransfer *PendingBBTransfer `json:"pendingTransfer,omitempty"`
	BBTarget        string             `json:"bbTarget,omitempty"`
	Ownership       *Ownership         `json:"ownership,omitempty"`
	Enabled         bool               `json:"enabled"`
	Paused          bool               `json:"paused"`
	Stop            bool               `json:"stop"`
	AwaitingUser    bool               `json:"awaitingUser"`
	Completed       bool               `json:"completed,omitempty"`
	Note            string             `json:"note,omitempty"`
	UpdatedAt       time.Time          `json:"updatedAt"`
}

// Load reads the control file. A missing file yields a disabled record and
// no error.
func Load(stateDir string) (File, error) {
	var f File
	reader, err := OpenSnapshot(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	defer reader.Close()
	b, err := io.ReadAll(reader)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return f, nil
}

// OpenSnapshot opens the current control file for reading. On Windows the
// handle allows read/write/delete sharing so observing control does not block
// a cooperating writer's atomic replacement. Callers must close the file. The
// opened file remains the old snapshot after replacement; in-place external
// writes are not isolated. Missing files return an error (unlike Load).
func OpenSnapshot(stateDir string) (*os.File, error) {
	return openSnapshotFile(filepath.Join(stateDir, FileName))
}

// Save writes the control file atomically.
func Save(stateDir string, f File) error {
	return saveAt(stateDir, f, time.Now().UTC())
}

func saveAt(stateDir string, f File, now time.Time) error {
	// A repeated hold must change the loaded record even when the wall clock
	// repeats (coarse platform clocks) or moves backwards. Resume tickets bind
	// to those bytes, so an identical timestamp could accept stale evidence.
	if !now.After(f.UpdatedAt) {
		now = f.UpdatedAt.Add(time.Nanosecond)
	}
	f.UpdatedAt = now.UTC()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(stateDir, FileName), append(b, '\n'), 0o600)
}

// Gate converts the file into the decision engine's view of it.
func (f File) Gate() decide.Control {
	return decide.Control{Enabled: f.Enabled, Paused: f.Paused, Stop: f.Stop, AwaitingUser: f.AwaitingUser, Completed: f.Completed}
}

// Summary is a one-line human description.
func (f File) Summary() string {
	switch {
	case !f.Enabled:
		return "disabled"
	case f.Stop:
		return "stop requested"
	case f.Paused:
		return "paused"
	case f.AwaitingUser:
		return "awaiting user"
	case f.Completed:
		return "completed"
	default:
		return "enabled"
	}
}

// Update serializes a read-modify-write with other control writers. Callers
// that also need the tick lock acquire it first; stop/pause need only this short
// control lock and can interrupt a long-running tick.
func Update(stateDir string, fn func(*File)) (File, error) {
	return UpdateChecked(stateDir, func(f *File) error { fn(f); return nil })
}

// UpdateChecked leaves control unchanged if the callback rejects the update.
func UpdateChecked(stateDir string, fn func(*File) error) (File, error) {
	l, err := lock.AcquireNamed(stateDir, lock.Control)
	if err != nil {
		return File{}, err
	}
	defer l.Release()
	if err := CheckControlPath(stateDir); err != nil {
		return File{}, err
	}
	f, err := Load(stateDir)
	if err != nil {
		return f, err
	}
	if err := fn(&f); err != nil {
		return f, err
	}
	return f, Save(stateDir, f)
}

// CheckControlPath refuses file aliases whose lock would belong to a different
// state directory. A symlink to the state directory itself shares its locks.
func CheckControlPath(stateDir string) error {
	info, err := os.Lstat(filepath.Join(stateDir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("control.json must not be a symlink; use the original state directory")
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceSnapshotFile(name, path)
}

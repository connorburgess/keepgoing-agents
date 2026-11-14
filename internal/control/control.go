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
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
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
	// StopLanesAt is when a person asked for the stop to cover the lanes as
	// well (keepgoing stop --lanes): the conversations the supervised one
	// started, and theirs. Each such request is carried out once; asking
	// again makes a new one. A resume clears it with the stop.
	StopLanesAt *time.Time `json:"stopLanesAt,omitempty"`
	// StopLanesDropQueued: that request also asked for the messages queued
	// in lanes that have not started, or are idle, to be deleted
	// (--drop-queued). Their text is not kept. A new request sets it again
	// or clears it; the supervised thread's own queue is never touched.
	StopLanesDropQueued bool   `json:"stopLanesDropQueued,omitempty"`
	AwaitingUser        bool   `json:"awaitingUser"`
	Completed           bool   `json:"completed,omitempty"`
	Note                string `json:"note,omitempty"`
	// EpisodeHold says a check raised the awaiting-user hold because a
	// per-episode allowance was spent (re-prompts, nudges, the patience for a
	// silent provider retry). Only a resume of such a hold starts a new
	// episode. No command sets it, so holding and resuming by hand refunds
	// nothing. A resume clears it.
	EpisodeHold bool `json:"episodeHold,omitempty"`
	// BriefHold lists the briefs, to another conversation, that a check
	// found recorded and not confirmed when it raised the awaiting-user
	// hold. A resume settles them: the loop stays on its current target and
	// no route is briefed on the next check.
	BriefHold []Brief `json:"briefHold,omitempty"`
	// OpenQuestion is the awaiting-user hold that was outstanding when the
	// task was declared complete, with its note. Completion ends the hold,
	// but what a person was being asked is kept until a resume.
	OpenQuestion *OpenQuestion `json:"openQuestion,omitempty"`
	UpdatedAt    time.Time     `json:"updatedAt"`
}

// Brief is a brief to another conversation: Action is handoff or
// switch_back, Target the route it went to.
type Brief struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

// OpenQuestion records an awaiting-user hold ended by a completion.
type OpenQuestion struct {
	Note string    `json:"note,omitempty"`
	At   time.Time `json:"at"`
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
	return snapshot.Open(filepath.Join(stateDir, FileName))
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

// writeAtomic syncs before publishing, as the send ledger does: a hold must not
// be replaced by an empty file after a crash. Not a power-loss guarantee.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return writeAtomicSync(path, data, mode, true)
}

// writeAtomicSync optionally flushes the temporary file before it replaces the
// target, so a power loss cannot leave the target name on unwritten data.
func writeAtomicSync(path string, data []byte, mode os.FileMode, sync bool) error {
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
	if sync {
		if err := snapshot.Sync(tmp); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return snapshot.ReplaceStrict(name, path)
}

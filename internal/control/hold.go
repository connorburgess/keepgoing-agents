package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// HoldFileName is the hold marker: a stop or pause that could not be saved in
// control.json because control.json could not be read.
//
// Stop and pause win over every other rule, so they must work when the
// control record is damaged. When control.json cannot be read, `stop` and
// `pause` write this small file beside it instead, and every check honours it
// before anything else, whatever control.json says or fails to say. It is
// cleared only by `resume`, which refuses while control.json is unreadable: a
// person restores control.json first. keepgoing never rewrites or moves a
// damaged control.json itself.
const HoldFileName = "control-hold.json"

// Hold is the content of the hold marker.
type Hold struct {
	Version int       `json:"version"`
	Stop    bool      `json:"stop"`
	Paused  bool      `json:"paused"`
	At      time.Time `json:"at"`
}

// maxHoldBytes bounds the hold marker. A real one is well under 200 bytes.
const maxHoldBytes = 4096

// ErrHoldNotValid says the hold marker exists but is not exactly a version 1
// stop or pause. Such a marker is a stop: the marker may only ever make
// keepgoing do less.
var ErrHoldNotValid = errors.New(HoldFileName + " is not a valid hold; treated as a stop")

// LoadHold reads the hold marker. It returns nil when there is none.
//
// The marker may only ever make keepgoing do less. A marker is a pause or a
// stop only when it is a small regular file holding exactly version 1 with
// stop or paused set. Anything else that exists under its name (a link, a
// pipe, a directory, an oversized or unreadable file, other versions or
// fields, or neither hold set) is returned as a stop together with an error,
// without being followed or opened when it is not a regular file. Callers
// that would let a marker stand in for the control record (to skip the owner
// and transfer checks) must require a nil error.
func LoadHold(stateDir string) (*Hold, error) {
	path := filepath.Join(stateDir, HoldFileName)
	stop := &Hold{Version: 1, Stop: true}
	// The open neither follows a link nor waits on a pipe, and the type is
	// checked on what was opened, so nothing swapped in can hang a check.
	f, err := snapshot.OpenRegular(path, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, snapshot.ErrNotRegular) {
		return stop, ErrHoldNotValid
	}
	if err != nil {
		return stop, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > maxHoldBytes {
		return stop, ErrHoldNotValid
	}
	b, err := io.ReadAll(io.LimitReader(f, maxHoldBytes+1))
	if err != nil || len(b) > maxHoldBytes {
		return stop, ErrHoldNotValid
	}
	var h Hold
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&h) != nil || d.More() || h.Version != 1 || !(h.Stop || h.Paused) {
		return stop, ErrHoldNotValid
	}
	return &h, nil
}

// SaveHold adds a stop or a pause to the hold marker, under the control lock.
// A stop or pause already in the marker is kept.
func SaveHold(stateDir string, stop, paused bool, now time.Time) (Hold, error) {
	l, err := lock.AcquireNamed(stateDir, lock.Control)
	if err != nil {
		return Hold{}, err
	}
	defer l.Release()
	h := Hold{Version: 1}
	if old, _ := LoadHold(stateDir); old != nil {
		h = *old // an unusable marker is a stop, and stays one
	}
	h.Version, h.At = 1, now.UTC()
	h.Stop = h.Stop || stop
	h.Paused = h.Paused || paused
	b, _ := json.Marshal(h)
	if err := writeAtomic(filepath.Join(stateDir, HoldFileName), append(b, '\n'), 0o600); err != nil {
		return Hold{}, err
	}
	return h, nil
}

// ReleaseHold deletes the hold marker, under the control lock, and only
// while control.json can be read: the marker stands in for the control
// record, so it is released only when the record is back. Only resume calls
// it.
func ReleaseHold(stateDir string) error {
	l, err := lock.AcquireNamed(stateDir, lock.Control)
	if err != nil {
		return err
	}
	defer l.Release()
	if _, err := Load(stateDir); err != nil {
		return err
	}
	err = os.Remove(filepath.Join(stateDir, HoldFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Apply adds the marker's holds to a control record.
func (h *Hold) Apply(f *File) {
	if h == nil {
		return
	}
	f.Stop = f.Stop || h.Stop
	f.Paused = f.Paused || h.Paused
}

// Summary names the hold for a loop whose control.json cannot be read.
func (h *Hold) Summary() string {
	if h.Stop {
		return "stopped (control record unreadable)"
	}
	return "paused (control record unreadable)"
}

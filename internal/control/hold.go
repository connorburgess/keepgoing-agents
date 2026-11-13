package control

import (
	"encoding/json"
	"errors"
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

// LoadHold reads the hold marker. It returns nil when there is none. A marker
// that exists but cannot be read is a stop: it was written to hold the loop,
// and a damaged one must not release it.
func LoadHold(stateDir string) (*Hold, error) {
	b, err := snapshot.ReadFile(filepath.Join(stateDir, HoldFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	h := &Hold{}
	if err == nil {
		err = json.Unmarshal(b, h)
	}
	if err != nil {
		return &Hold{Version: 1, Stop: true}, err
	}
	return h, nil
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
		h = *old
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

// RemoveHold deletes the hold marker. Only resume calls it, once
// control.json can be read again.
func RemoveHold(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, HoldFileName))
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

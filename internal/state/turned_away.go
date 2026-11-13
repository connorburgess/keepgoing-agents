package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// TurnedAwayName records the latest check that exited without running
// because an earlier check still held the tick lock. It is written without
// the lock, by the check that could not take it, and read by the overdue
// rule: checks that are being started but turned away mean the scheduler is
// running and an earlier check may be hung.
const TurnedAwayName = "turned-away.json"

type turnedAway struct {
	At time.Time `json:"at"`
}

// NoteTurnedAway records that a check was turned away at at. It replaces
// the file in one step, so two checks turned away at once leave one valid
// record. Best effort: an error is returned but a caller may ignore it.
func NoteTurnedAway(stateDir string, at time.Time) error {
	b, err := json.Marshal(turnedAway{At: at.UTC()})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".turned-away-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(stateDir, TurnedAwayName))
}

// TurnedAwayAt is the latest recorded turned-away check; zero when there is
// none or the record cannot be read.
func TurnedAwayAt(stateDir string) time.Time {
	b, err := os.ReadFile(filepath.Join(stateDir, TurnedAwayName))
	if err != nil {
		return time.Time{}
	}
	var r turnedAway
	if json.Unmarshal(b, &r) != nil {
		return time.Time{}
	}
	return r.At
}

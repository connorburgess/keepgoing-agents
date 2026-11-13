package main

import (
	"errors"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
)

// Unlike an update error, this means the control mutation was saved. Callers
// must not undo it or suppress already-authorized completion/report processing.
var errControlHistory = errors.New("control saved; incident history write failed; diagnostic evidence is incomplete")

func updateControlWithHistory(dir, operation string, fn func(*control.File) error, routine ...rd.RoutineRef) (control.File, error) {
	return updateControlAnnotated(dir, operation, fn, nil, routine...)
}

// updateControlAnnotated is updateControlWithHistory for a command that
// records who ran it and what else the save did. annotate runs after fn has
// succeeded, inside the control update.
func updateControlAnnotated(dir, operation string, fn func(*control.File) error, annotate func(control.File, *incident.ControlChange), routine ...rd.RoutineRef) (control.File, error) {
	var r incident.Record
	f, err := control.UpdateChecked(dir, func(f *control.File) error {
		r = incident.Record{Kind: "control", CheckStartedAt: time.Now().UTC(), Build: version,
			ControlBefore: incident.Gate(f.Gate()), ControlChange: &incident.ControlChange{Operation: operation}}
		owner, generation := ownerIdentity(*f)
		r.ControlChange.OwnerPresentBefore = f.Ownership != nil
		if err := fn(f); err != nil {
			return err
		}
		nextOwner, nextGeneration := ownerIdentity(*f)
		r.ControlAfter = incident.Gate(f.Gate())
		r.ControlChange.OwnerPresentAfter = f.Ownership != nil
		r.ControlChange.OwnershipChanged = owner != nextOwner || generation != nextGeneration
		if annotate != nil {
			annotate(*f, r.ControlChange)
		}
		return nil
	})
	if err != nil {
		return f, err // A rejected or failed save is not a persisted transition.
	}
	recordRunBoundary(dir, operation, routine...)
	// This bounds observation of the successful save, not its exact timestamp.
	// The control lock is released before acquiring the diagnostic lock.
	r.ControlChange.SaveObservedAt = time.Now().UTC()
	if err := incident.Append(dir, r); err != nil {
		return f, errControlHistory
	}
	return f, nil
}

func ownerIdentity(f control.File) (string, uint64) {
	if f.Ownership == nil {
		return "", 0
	}
	return f.Ownership.ThreadID, f.Ownership.Generation
}

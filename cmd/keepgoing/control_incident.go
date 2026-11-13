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
	return updateControlFromAnnotated(dir, operation, "", fn, nil, routine...)
}

// updateControlAnnotated is updateControlWithHistory for a command that
// records who ran it and what else the save did. annotate runs after fn has
// succeeded, inside the control update.
func updateControlAnnotated(dir, operation string, fn func(*control.File) error, annotate func(control.File, *incident.ControlChange), routine ...rd.RoutineRef) (control.File, error) {
	return updateControlFromAnnotated(dir, operation, "", fn, annotate, routine...)
}

// controlOrigins are where a control change can come from besides a command:
// "panel" is the keepgoing panel's own pause, stop and resume. The history
// records it in the operation's value ("panel_pause"), never as a field of
// its own, because the journal is decoded strictly and a binary from before
// that field would refuse the whole journal. Older binaries read an
// operation value they do not know as "unavailable" instead.
var controlOrigins = map[string]bool{"panel": true}

// updateControlFrom is updateControlWithHistory for a change from origin
// ("" for a command).
func updateControlFrom(dir, operation, origin string, fn func(*control.File) error, routine ...rd.RoutineRef) (control.File, error) {
	return updateControlFromAnnotated(dir, operation, origin, fn, nil, routine...)
}

// updateControlFromAnnotated is the one implementation: the history records
// the operation with its origin, the run graph is given the plain operation.
func updateControlFromAnnotated(dir, operation, origin string, fn func(*control.File) error, annotate func(control.File, *incident.ControlChange), routine ...rd.RoutineRef) (control.File, error) {
	recorded := operation
	if controlOrigins[origin] {
		recorded = origin + "_" + operation
	}
	var r incident.Record
	f, err := control.UpdateChecked(dir, func(f *control.File) error {
		r = incident.Record{Kind: "control", CheckStartedAt: time.Now().UTC(), Build: version,
			ControlBefore: incident.Gate(f.Gate()), ControlChange: &incident.ControlChange{Operation: recorded}}
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

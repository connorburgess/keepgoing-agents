package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
)

// recordRunBoundary records the run boundary implied by saved control state
// after a control change. Control is read again inside the graph lock, so two
// commands that record in the opposite order to their saves cannot apply an
// older state over a newer one. It is best effort and silent: the control
// change already succeeded, and the next check records a missed boundary.
func recordRunBoundary(dir, operation string, routine ...rd.RoutineRef) {
	id, err := rungraph.NewID("run_")
	if err != nil {
		return
	}
	var supplied rd.RoutineRef
	if len(routine) > 0 {
		supplied = routine[0]
	}
	now := time.Now().UTC()
	_ = rungraph.Update(dir, now, func(g *rungraph.Graph) (bool, error) {
		f, err := control.Load(dir)
		if err != nil {
			return false, nil
		}
		changed, err := g.Sync(rungraph.Boundary{Gate: rungraph.GateOf(f), Operation: operation, At: now, NewRunID: id, Routine: supplied})
		if err != nil || supplied == (rd.RoutineRef{}) {
			return changed, err
		}
		// The invocation is on the run if this operation opened it. If the
		// run was already open, it is a trigger of that run.
		triggered, err := g.Trigger(supplied, now)
		return changed || triggered, err
	})
}

// recordRegistration records that a control operation reached this loop
// through a registry reference.
func recordRegistration(stateDir, id string) {
	now := time.Now().UTC()
	_ = rungraph.Update(stateDir, now, func(g *rungraph.Graph) (bool, error) { return g.Register(id, now) })
}

// recordRegistrationInExistingGraph maps a registry reference at
// `registry add` when the loop already has a graph. Registry commands never
// create a runtime file: with no graph or no graph lock there is nothing to
// claim and nothing is written. Repeating `registry add` after the loop's
// first run records the reference then.
func recordRegistrationInExistingGraph(stateDir, id string) {
	if _, availability := rungraph.Load(stateDir); availability != rungraph.Available {
		return
	}
	if info, err := os.Lstat(filepath.Join(stateDir, "graph.lock")); err != nil || !info.Mode().IsRegular() {
		return
	}
	recordRegistration(stateDir, id)
}

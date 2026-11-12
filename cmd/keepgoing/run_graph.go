package main

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
)

// recordRunBoundary records the run boundary implied by a saved control
// change. It is best effort and silent: the control change already succeeded,
// and the next check records a missed boundary from saved control state.
func recordRunBoundary(dir, operation string, f control.File) {
	id, err := rungraph.NewID("run_")
	if err != nil {
		return
	}
	now := time.Now().UTC()
	_ = rungraph.Update(dir, now, func(g *rungraph.Graph) (bool, error) {
		return g.Sync(rungraph.Boundary{Gate: rungraph.GateOf(f), Operation: operation, At: now, NewRunID: id})
	})
}

// recordRegistration records that a control operation reached this loop
// through a registry reference. Registry add/remove never touch a runtime
// directory, so this is the only place the mapping is written.
func recordRegistration(stateDir, id string) {
	now := time.Now().UTC()
	_ = rungraph.Update(stateDir, now, func(g *rungraph.Graph) (bool, error) { return g.Register(id, now) })
}

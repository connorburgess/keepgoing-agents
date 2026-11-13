package main

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
)

// recordRunBoundary records the run boundary implied by saved control state
// after a control change. Control is read again inside the graph lock, so two
// commands that record in the opposite order to their saves cannot apply an
// older state over a newer one. It is best effort and silent: the control
// change already succeeded, and the next check records a missed boundary.
func recordRunBoundary(dir, operation string) {
	id, err := rungraph.NewID("run_")
	if err != nil {
		return
	}
	now := time.Now().UTC()
	_ = rungraph.Update(dir, now, func(g *rungraph.Graph) (bool, error) {
		f, err := control.Load(dir)
		if err != nil {
			return false, nil
		}
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

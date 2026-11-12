package rungraph

import (
	"time"

	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
)

// Gate is the saved control state a run boundary is recorded from.
type Gate struct {
	Enabled, Paused, Stop, AwaitingUser, Completed, PendingTransfer bool
}

// Boundary describes one recording pass over saved control state. Operation
// is the accepted control operation, or empty when a later check reconciles.
// NewRunID is consumed only if this pass opens a run. Routine is the optional
// externally supplied invocation carried by an explicit enable/start/resume.
type Boundary struct {
	Gate      Gate
	Operation string
	At        time.Time
	NewRunID  string
	Routine   rd.RoutineRef
}

// Sync records the run boundary implied by saved control state. It converges:
// repeating it changes nothing, and a boundary whose write was lost is recorded
// by the next pass with the control_state source instead of being guessed
// later from timestamps.
//
//   - Completion saved and the run open: close it.
//   - Completion cleared after it was recorded: open a new run that reopens it.
//   - No run yet: an accepted enable/start opens an explicit run. Otherwise an
//     enabled loop with no hold or pending transfer is adopted.
func (g *Graph) Sync(b Boundary) (bool, error) {
	s := g.state()
	source := SourceReconciled
	if b.Operation != "" {
		source = SourceControl
	}
	event := rd.Event{At: b.At}
	explicit := b.Operation == "enable" || b.Operation == "start"
	switch {
	case b.Gate.Completed && !s.Completed:
		event.Kind = rd.Complete
	case !b.Gate.Completed && s.Completed:
		event.Kind, event.NewID = rd.Resume, b.NewRunID
		if b.Operation == "resume" {
			event.Routine = b.Routine
		}
	case s.Current.ID != "" || s.Completed || !b.Gate.Enabled:
		return false, nil
	case explicit:
		event.Kind, event.NewID, event.Routine = rd.Enable, b.NewRunID, b.Routine
	case !b.Gate.Paused && !b.Gate.Stop && !b.Gate.AwaitingUser && !b.Gate.PendingTransfer:
		event.Kind, event.NewID, event.AdoptionEligible = rd.Adopt, b.NewRunID, true
	default:
		return false, nil
	}
	next, err := rd.Apply(s, event)
	if err != nil {
		return false, err
	}
	g.Completed = next.Completed
	if next.Current.ID == s.Current.ID {
		if r := g.run(s.Current.ID); r != nil && r.ClosedAt.IsZero() && !next.Current.ClosedAt.IsZero() {
			r.ClosedAt, r.ClosedSource = next.Current.ClosedAt, source
		}
		return true, nil
	}
	e := next.Current
	g.Runs = append(g.Runs, Run{ID: e.ID, Origin: e.Origin, RecordedAt: e.RecordedAt, Source: source, Reopens: e.Reopens, Routine: routineOf(e.Routine)})
	g.CurrentRun = e.ID
	return true, nil
}

// Begin records a send intent in the open run before the adapter is called.
func (g *Graph) Begin(a Attempt) error {
	a.Run, a.Outcome, a.Source, a.ReturnedAt = g.CurrentRun, rd.Intent, SourceAdapter, time.Time{}
	retained := make([]rd.Attempt, len(g.Attempts))
	for i, prior := range g.Attempts {
		retained[i] = prior.contract()
	}
	if _, err := rd.BeginAttempt(g.state(), a.contract(), retained...); err != nil || a.Target == "" || len(a.Target) > maxTarget {
		return rd.ErrInvalid
	}
	g.Attempts = append(g.Attempts, a)
	return nil
}

// Finish records the adapter's return for a retained attempt.
func (g *Graph) Finish(id string, outcome rd.Outcome, at time.Time) (bool, error) {
	for i := range g.Attempts {
		if g.Attempts[i].ID != id {
			continue
		}
		done, err := rd.FinishAttempt(g.Attempts[i].contract(), outcome, at)
		if err != nil {
			return false, err
		}
		changed := g.Attempts[i].Outcome != done.Outcome
		g.Attempts[i].Outcome, g.Attempts[i].ReturnedAt = done.Outcome, done.ReturnedAt
		return changed, nil
	}
	return false, rd.ErrInvalid
}

// Relate records a link for the current run. A link already recorded for the
// same run, payload and target is kept with its first time and source.
func (g *Graph) Relate(l Link) (bool, error) {
	l.Run = g.CurrentRun
	if _, err := rd.Relate(g.state(), l.contract()); err != nil || !l.Source.valid() || len(l.Target) > maxTarget || (l.Kind != rd.SessionLink && l.Target != "") {
		return false, rd.ErrInvalid
	}
	want := l.contract()
	for _, prior := range g.Links {
		have := prior.contract()
		have.RecordedAt, have.AttemptID = want.RecordedAt, want.AttemptID
		if have == want && prior.Target == l.Target {
			return false, nil
		}
	}
	g.Links = append(g.Links, l)
	return true, nil
}

// Register records that an accepted control operation reached this loop
// through a registry reference. It is evidence that the reference named this
// loop then, not that it is still registered: the registry owns that. When
// the list is full the oldest reference is dropped.
func (g *Graph) Register(id string, at time.Time) (bool, error) {
	if _, err := rd.Register(rd.State{LoopID: g.LoopID}, id, at); err != nil {
		return false, err
	}
	for _, r := range g.Registrations {
		if r.ID == id {
			return false, nil
		}
	}
	if len(g.Registrations) >= MaxRegistrations {
		g.Registrations = g.Registrations[1:]
	}
	g.Registrations = append(g.Registrations, Registration{ID: id, RecordedAt: at, Source: SourceRegistered})
	return true, nil
}

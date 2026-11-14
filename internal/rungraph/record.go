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
	// The picture of what a run waits on belongs to that open run only.
	g.Waiting = nil
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

// Discard removes a send intent whose adapter call returned positive evidence
// that the provider did nothing: no failed turn to retry, or a retry already
// queued. It applies only to a child_retry intent that has no outcome yet;
// any other attempt, and one with an outcome, is kept.
func (g *Graph) Discard(id string) (bool, error) {
	for i := range g.Attempts {
		if g.Attempts[i].ID != id {
			continue
		}
		if g.Attempts[i].Action != "child_retry" || g.Attempts[i].Outcome != rd.Intent {
			return false, rd.ErrInvalid
		}
		g.Attempts = append(g.Attempts[:i], g.Attempts[i+1:]...)
		return true, nil
	}
	return false, rd.ErrInvalid
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

// Register records a registry reference for this loop. source says why:
// SourceRegistered when an accepted control operation reached the loop through
// the reference, SourceRegistryAdd when `registry add` found an existing
// graph. A reference first recorded by an add is upgraded when a control
// operation later uses it. It is evidence that the reference named this loop
// then, not that it is still registered: the registry owns that.
//
// When the list is full the oldest add-only reference is dropped first, so
// repeated remove/add cannot push out a reference a control operation used.
// With none left, the oldest reference goes. Drops are counted.
func (g *Graph) Register(id string, source Source, at time.Time) (bool, error) {
	if _, err := rd.Register(rd.State{LoopID: g.LoopID}, id, at); err != nil || (source != SourceRegistered && source != SourceRegistryAdd) {
		return false, rd.ErrInvalid
	}
	for i, r := range g.Registrations {
		if r.ID == id {
			if r.Source == SourceRegistryAdd && source == SourceRegistered {
				g.Registrations[i].Source = SourceRegistered
				return true, nil
			}
			return false, nil
		}
	}
	if len(g.Registrations) >= MaxRegistrations {
		drop := 0
		for i, r := range g.Registrations {
			if r.Source == SourceRegistryAdd {
				drop = i
				break
			}
		}
		g.Registrations = append(g.Registrations[:drop], g.Registrations[drop+1:]...)
		g.Dropped.Registrations++
	}
	g.Registrations = append(g.Registrations, Registration{ID: id, RecordedAt: at, Source: source})
	return true, nil
}

// Skip records that evidence for the current run could not be stored. It is
// kept once per run, target and reason; the caller reports it the first time.
func (g *Graph) Skip(target, reason string, at time.Time) (bool, error) {
	if g.run(g.CurrentRun) == nil || target == "" || len(target) > maxTarget || reason != ReasonSessionID || at.IsZero() {
		return false, rd.ErrInvalid
	}
	for _, u := range g.Unrecorded {
		if u.Run == g.CurrentRun && u.Target == target && u.Reason == reason {
			return false, nil
		}
	}
	if len(g.Unrecorded) >= MaxUnrecorded {
		g.Unrecorded = g.Unrecorded[1:]
		g.Dropped.Unrecorded++
	}
	g.Unrecorded = append(g.Unrecorded, Unrecorded{Run: g.CurrentRun, Target: target, Reason: reason, RecordedAt: at})
	return true, nil
}

// Trigger records that an externally identified routine invocation arrived
// while a run was open. It never opens, reopens or closes a run: with no open
// run nothing is recorded. The invocation that opened the run is already on
// the run and is not recorded again. A routine that supplies no invocation ID
// has one link per run, because its triggers cannot be told apart.
//
// Triggers have their own bound. A run keeps its first trigger and its newest
// MaxTriggersPerRun-1; the ones between are dropped and counted, so a
// scheduler that sends a new ID on every check cannot push out a chat link,
// a goal link or an earlier run.
func (g *Graph) Trigger(r rd.RoutineRef, at time.Time) (bool, error) {
	run := g.run(g.CurrentRun)
	if run == nil || g.Completed {
		return false, nil
	}
	if run.Routine != nil && routineRef(run.Routine) == r {
		return false, nil
	}
	changed, err := g.Relate(Link{Kind: rd.InvocationLink, Routine: routineOf(r), Source: SourceExternal, RecordedAt: at})
	if err != nil || !changed {
		return changed, err
	}
	var own []int
	for i, l := range g.Links {
		if l.Run == run.ID && l.Kind == rd.InvocationLink {
			own = append(own, i)
		}
	}
	if len(own) > MaxTriggersPerRun {
		drop := own[1] // the oldest after the first
		g.Links = append(g.Links[:drop], g.Links[drop+1:]...)
		g.Dropped.Triggers++
	}
	return true, nil
}

// Wait replaces the record of what the open run's target is waiting on with
// one check's verified child listing. An incomplete listing stores no child:
// the answer is then unknown, not the part that happened to be verified. With
// no open run nothing is recorded. Keys are short hashes, never thread IDs.
func (g *Graph) Wait(target, listing string, children []Child, at time.Time) (bool, error) {
	if g.run(g.CurrentRun) == nil || g.Completed {
		return false, nil
	}
	if listing != ListingComplete {
		listing, children = ListingIncomplete, nil
	}
	w := &Waiting{Run: g.CurrentRun, Target: target, CheckedAt: at, Listing: listing, Children: append([]Child{}, children...), Source: SourceChildren}
	// The record needs schema 2; an older graph is upgraded by this write.
	previous, version := g.Waiting, g.SchemaVersion
	g.Waiting, g.SchemaVersion = w, SchemaVersion
	if !g.valid() {
		g.Waiting, g.SchemaVersion = previous, version
		return false, rd.ErrInvalid
	}
	return true, nil
}

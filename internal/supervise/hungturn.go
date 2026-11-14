package supervise

import (
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// hungTurnEvent is the report sent once when a hung-turn wait begins.
const hungTurnEvent = "hung_turn"

// hungTurn watches an active turn through an outage and reports whether it
// now shows every sign of having hung. A host that sleeps in the middle of a
// model request leaves the turn active and silent for good, and nothing would
// ever end it: the stall rule would queue nudges behind it.
//
// All of these must hold, from structured evidence only:
//
//  0. The loop tracks Git progress, and the check is inside the operating
//     window. Otherwise the watch does nothing and is left as it is.
//  1. An outage happened while this turn was active: a connectivity check
//     found the network down, or this is the first check after a gap the
//     missed-time rule judges missed (the host slept or the scheduler was
//     down).
//  2. It is still the same turn.
//  3. It has logged nothing since the outage began, or, after a gap, since
//     the computer woke (taken as one usual interval before this check);
//     it has been silent for stall_after; and the stall clock has reached
//     stall_after.
//  4. It is waiting on the model, not on a tool: no tool item it started is
//     still open, and the item bookkeeping is not in doubt.
//  5. Nothing else explains the silence: nothing queued, no background
//     agents, no children running or unverified, no person asked, no
//     provider retry, no Git change.
//
// While they hold, the stall rule decides as always, and a nudge it would
// send is held back: counted and recorded as sent, not sent. So its hold
// after its nudges comes at the same check as without the watch, and the
// watch never checks connectivity where the stall rule would not. The wait
// is reported once. A watched turn that logs again is handed back to the
// stall rule: the watch is dropped. A turn whose activity cannot be read
// leaves the watch as it was: unknown never acts. Nothing is evaluated while
// the network is down.
//
// in is the decision input without the watch.
func (t *tick) hungTurn(obs decide.Observation, progressed bool, in decide.Input) bool {
	stall := t.loop.Progress.StallAfter.Duration
	w := t.st.HungTurn
	keep := func(w *state.HungTurn) {
		if !t.opts.DryRun {
			t.st.HungTurn = w
		}
	}
	if !in.InWindow || (obs.State == decide.Active && obs.TurnUnknown) {
		return false // outside the window, or the turn could not be read
	}
	if len(t.loop.Progress.Repositories) == 0 || obs.State != decide.Active || obs.Turn == nil || progressed {
		keep(nil) // no Git tracking, the turn ended, it has no turn, or work was done
		return false
	}
	last := obs.Turn.LastActivityAt
	if last.IsZero() {
		return false // when it last did anything is not known
	}
	if w != nil && w.Turn != obs.Turn.ID {
		w = nil // another turn: whatever the old one did, this one is new
	}
	signs := !obs.Turn.ToolsRunning && obs.Queued == 0 && obs.Background == 0 && obs.ActiveChildren == 0 && !obs.ChildrenUnverified &&
		!obs.AwaitingInput && !obs.Retrying && !obs.RetryUnverified
	if w == nil && signs && stallRuleActs(in) {
		t.networkDown() // the stall rule checks connectivity on this check too
	}
	// The outage this check found, if any: when the silence must have begun.
	var outageAt time.Time
	switch {
	case t.networkGated:
		outageAt = t.st.NetworkDownSince
		if outageAt.IsZero() {
			outageAt = t.now
		}
	case t.missedTime() > 0:
		// After a gap, the computer woke at most one usual interval before
		// this check. Silence that began before then counts.
		if outageAt = t.now.Add(-t.usualInterval()); outageAt.Before(t.st.CheckedAt) {
			outageAt = t.st.CheckedAt
		}
	}
	if w == nil {
		if outageAt.IsZero() || !last.Before(outageAt) {
			return false // no outage, or the turn has logged since it
		}
		w = &state.HungTurn{Turn: obs.Turn.ID, OutageAt: outageAt}
	} else {
		copy := *w
		w = &copy
		if !outageAt.IsZero() && last.Before(outageAt) {
			w.Since = time.Time{} // another outage: the wait starts again
		}
	}
	if t.networkGated {
		keep(w)
		return false
	}
	if !last.Before(w.OutageAt) {
		keep(nil) // the watched turn logged again: back to the stall rule
		return false
	}
	if !signs || in.UnchangedFor < stall || t.now.Sub(last) < stall {
		w.Since = time.Time{}
		keep(w)
		return false
	}
	if w.Since.IsZero() {
		w.Since = t.now
		if !w.Reported && (t.opts.DryRun || t.reserveReport(hungTurnEvent)) {
			w.Reported = true
			ask := "no time is set, because the stall rule would not hold it within a year"
			after := in
			if d := decide.Decide(withoutWatch(in)); d.Action == decide.Nudge {
				after.Nudges, after.SinceWake = in.Nudges+1, 0 // the nudge this check holds back
			}
			askAt := decide.StallHoldAt(after, t.now)
			if !askAt.IsZero() {
				ask = "it asks a person at about " + askAt.UTC().Format(time.RFC3339) + " if the turn is still silent, when the stall rule would hold it"
			}
			data := map[string]any{"target": t.res.Target, "silentSince": last, "outageAt": w.OutageAt}
			if !askAt.IsZero() {
				data["askAt"] = askAt
			}
			t.report(hungTurnEvent, fmt.Sprintf("%s: the turn on %s has logged nothing since %s, before an outage or the computer sleeping, and may be hung. keepgoing holds back the nudges the stall rule would send it, and has not stopped it; %s.",
				t.loop.Name, t.res.Target, last.UTC().Format(time.RFC3339), ask), data)
		}
	}
	keep(w)
	return true
}

func withoutWatch(in decide.Input) decide.Input {
	in.HungTurn = false
	return in
}

// stallRuleActs says the stall rule would nudge, or hold after its nudges,
// on this check: the only checks on which it looks at the network.
func stallRuleActs(in decide.Input) bool {
	d := decide.Decide(withoutWatch(in))
	return d.Action == decide.Nudge || d.StallHold
}

package supervise

import (
	"fmt"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// hungTurnEvent is the report sent once when a hung-turn wait begins.
const hungTurnEvent = "hung_turn"

// hungTurn watches an active turn through an outage and reports whether it
// now shows every sign of having hung. A host that sleeps in the middle of a
// model request leaves the turn active and silent for good, and nothing would
// ever end it: the stall rule would queue nudges behind it and then hold,
// with a reason that does not say what happened.
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
//     the computer woke (taken as one usual interval before this check), and
//     the stall clock has reached stall_after: the moment the stall rule
//     would first nudge.
//  4. It is waiting on the model, not on a tool: no tool item it started is
//     still open, and the item bookkeeping is not in doubt.
//  5. Nothing else explains the silence: nothing queued, no background
//     agents, no children running or unverified, no person asked, no
//     provider retry, no Git change.
//
// While they hold, the session is waited on, not nudged, and the wait is
// reported once. A person is asked when the stall rule would have held it.
// A watched turn that logs again has come back: it is not nudged until it has
// been silent for stall_after again (grace), and is then waited on again; the
// hold stays where the stall rule would have put it. A turn whose activity
// cannot be read leaves the watch as it was: unknown never acts. Nothing is
// evaluated while the network is down.
//
// The watch never makes a request the stall rule would not: connectivity is
// checked for it only on a check where the stall rule would nudge or hold.
// in is the decision input without the watch.
func (t *tick) hungTurn(obs decide.Observation, progressed bool, in decide.Input) (hung, grace bool) {
	stall, unchanged, inWindow := t.loop.Progress.StallAfter.Duration, in.UnchangedFor, in.InWindow
	w := t.st.HungTurn
	keep := func(w *state.HungTurn) {
		if !t.opts.DryRun {
			t.st.HungTurn = w
		}
	}
	if !inWindow || (obs.State == decide.Active && obs.TurnUnknown) {
		return false, false // outside the window, or the turn could not be read
	}
	if len(t.loop.Progress.Repositories) == 0 || obs.State != decide.Active || obs.Turn == nil || progressed {
		keep(nil) // no Git tracking, the turn ended, it has no turn, or work was done
		return false, false
	}
	last := obs.Turn.LastActivityAt
	if last.IsZero() {
		return false, false // when it last did anything is not known
	}
	if w != nil && w.Turn != obs.Turn.ID {
		w = nil // another turn: whatever the old one did, this one is new
	}
	signs := !obs.Turn.ToolsRunning && obs.Queued == 0 && obs.Background == 0 && obs.ActiveChildren == 0 && !obs.ChildrenUnverified &&
		!obs.AwaitingInput && !obs.Retrying && !obs.RetryUnverified
	if w == nil && signs && stallRuleActs(in) {
		t.networkDown() // the stall rule checks connectivity before this nudge too
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
			return false, false // no outage, or the turn has logged since it
		}
		w = &state.HungTurn{Turn: obs.Turn.ID, OutageAt: outageAt}
	} else {
		copy := *w
		w = &copy
		if !outageAt.IsZero() && last.Before(outageAt) {
			// Another outage, with the turn silent since before it: the
			// wait starts again from it.
			w.OutageAt, w.Since, w.Revived = outageAt, time.Time{}, false
		}
	}
	if t.networkGated {
		keep(w)
		return false, false
	}
	if !w.Revived && !last.Before(w.OutageAt) {
		w.Revived, w.Since = true, time.Time{} // the watched turn logged again: it came back
	}
	silent := unchanged >= stall && (!w.Revived || t.now.Sub(last) >= stall)
	if !signs || !silent {
		w.Since = time.Time{}
		keep(w)
		// A turn that came back is not nudged until it has been silent for
		// stall_after again.
		return false, signs && w.Revived && t.now.Sub(last) < stall
	}
	if w.Since.IsZero() {
		w.Since = t.now
		if !w.Reported && (t.opts.DryRun || t.reserveReport(hungTurnEvent)) {
			w.Reported = true
			askAt := t.now.Add(decide.HungTurnHoldIn(in))
			t.report(hungTurnEvent, fmt.Sprintf("%s: the turn on %s has logged nothing since %s, before a network outage or a time in which no check ran, and may be hung. keepgoing is waiting on it, not nudging it, and has not stopped it; it asks a person at %s if the turn is still silent, when the stall rule would have held it.",
				t.loop.Name, t.res.Target, last.UTC().Format(time.RFC3339), askAt.UTC().Format(time.RFC3339)),
				map[string]any{"target": t.res.Target, "silentSince": last, "outageAt": w.OutageAt, "askAt": askAt})
		}
	}
	keep(w)
	return true, false
}

// stallRuleActs says the stall rule would nudge, or hold after its nudges,
// on this check: the only checks on which it looks at the network.
func stallRuleActs(in decide.Input) bool {
	in.HungTurn, in.HungTurnGrace = false, false
	d := decide.Decide(in)
	return d.Action == decide.Nudge || (d.Action == decide.AwaitUser && strings.HasPrefix(d.Reason, "no progress after"))
}

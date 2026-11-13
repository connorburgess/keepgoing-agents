package supervise

import (
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// hungTurnEvent is the report sent once when a hung-turn wait begins.
const hungTurnEvent = "hung_turn"

// hungTurn watches an active turn through a network outage, or a gap in
// which no check ran, and reports whether it now shows every sign of having
// hung. A host that sleeps in the middle of a model request leaves the turn
// active and silent for good, and nothing would ever end it: the stall rule
// would queue nudges behind it and then hold, with a reason that does not
// say what happened.
//
// All of these must hold, from structured evidence only:
//
//  1. A connectivity check found the network down while this turn was
//     active, or a gap with no check was judged missed time.
//  2. It is still the same turn.
//  3. It has logged nothing since before the outage, and the network has
//     been back for a full stall_after.
//  4. It is waiting on the model, not on a tool: no tool item it started is
//     still open, and the item bookkeeping is not in doubt.
//  5. Nothing else explains the silence: nothing queued, no children
//     running or unverified, no person asked, no provider retry, no Git
//     change.
//
// While they hold, the session is waited on, not nudged, and the wait is
// reported once. It ends by itself the moment any of them stops holding.
// Nothing is evaluated while the network is down: the watch only records
// that the outage happened.
func (t *tick) hungTurn(obs decide.Observation, progressed bool) (bool, time.Duration) {
	w := t.st.HungTurn
	keep := func(w *state.HungTurn) {
		if !t.opts.DryRun {
			t.st.HungTurn = w
		}
	}
	if obs.State != decide.Active || obs.Turn == nil || progressed {
		keep(nil) // the turn ended, its activity is unknown, or work was done
		return false, 0
	}
	if w != nil && w.Turn != obs.Turn.ID {
		w = nil // another turn: whatever the old one did, this one is new
	}
	if w == nil {
		outageAt := time.Time{}
		switch {
		case t.networkGated:
			outageAt = t.st.NetworkDownSince
			if outageAt.IsZero() {
				outageAt = t.now
			}
		case t.missedTime() > 0 && !t.st.CheckedAt.IsZero():
			outageAt = t.st.CheckedAt // the last check before the gap
		default:
			return false, 0
		}
		w = &state.HungTurn{Turn: obs.Turn.ID, OutageAt: outageAt}
	} else {
		copy := *w
		w = &copy
	}
	if t.networkGated {
		keep(w)
		return false, 0
	}
	if w.BackAt.IsZero() {
		w.BackAt = t.now
	}
	last := obs.Turn.LastActivityAt
	if last.IsZero() || !last.Before(w.OutageAt) {
		keep(nil) // the turn did something after the outage: it is alive
		return false, 0
	}
	holds := !obs.Turn.ToolsRunning && obs.Queued == 0 && obs.ActiveChildren == 0 && !obs.ChildrenUnverified &&
		!obs.AwaitingInput && !obs.Retrying && !obs.RetryUnverified &&
		t.now.Sub(w.BackAt) >= t.loop.Progress.StallAfter.Duration
	if !holds {
		w.Since = time.Time{}
		keep(w)
		return false, 0
	}
	if w.Since.IsZero() {
		w.Since = t.now
		if !w.Reported {
			w.Reported = true
			t.report(hungTurnEvent, fmt.Sprintf("%s: the turn on %s has logged nothing since %s, before a network outage, and may be hung. keepgoing is waiting on it, not nudging it, and has not stopped it; it asks a person if it is still silent after %s.",
				t.loop.Name, t.res.Target, last.UTC().Format(time.RFC3339), t.hungTurnPatience()),
				map[string]any{"target": t.res.Target, "silentSince": last, "outageAt": w.OutageAt})
		}
	}
	keep(w)
	return true, t.now.Sub(w.Since)
}

// hungTurnPatience is how long a hung-turn wait lasts before a person is
// asked: as long as the nudges it replaces would have taken.
func (t *tick) hungTurnPatience() time.Duration {
	return time.Duration(max(t.loop.Progress.MaxNudges, 1)) * t.loop.Progress.StallAfter.Duration
}

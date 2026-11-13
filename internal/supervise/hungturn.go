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
// ever end it: the stall rule would queue nudges behind it and then hold,
// with a reason that does not say what happened.
//
// All of these must hold, from structured evidence only:
//
//  0. The loop tracks Git progress. Without it the stall rule never acts on
//     an active turn, and neither does this.
//  1. An outage happened while this turn was active: a connectivity check
//     found the network down, or a check found a gap the missed-time rule
//     judges missed (the host slept or the scheduler was down).
//  2. It is still the same turn.
//  3. It has logged nothing since before the outage or gap began, and the
//     stall clock, which restarts when the network is back, has reached
//     stall_after: the moment the stall rule would first nudge.
//  4. It is waiting on the model, not on a tool: no tool item it started is
//     still open, and the item bookkeeping is not in doubt.
//  5. Nothing else explains the silence: nothing queued, no background
//     agents, no children running or unverified, no person asked, no
//     provider retry, no Git change.
//
// While they hold, the session is waited on, not nudged, and the wait is
// reported once. It ends by itself the moment any of them stops holding. A
// turn whose activity could not be read leaves the watch as it was: unknown
// never acts. Nothing is evaluated while the network is down: the watch only
// records that the outage happened.
//
// revived says the watched turn logged something after the outage: the
// caller restarts the stall clock at this check, so a turn that comes back
// gets a full stall_after before any nudge.
func (t *tick) hungTurn(obs decide.Observation, progressed bool, unchanged time.Duration) (hung, revived bool) {
	w := t.st.HungTurn
	keep := func(w *state.HungTurn) {
		if !t.opts.DryRun {
			t.st.HungTurn = w
		}
	}
	if obs.State == decide.Active && obs.TurnUnknown {
		return false, false // the turn could not be read: keep the watch as it was
	}
	if len(t.loop.Progress.Repositories) == 0 || obs.State != decide.Active || obs.Turn == nil || progressed {
		keep(nil) // no Git tracking, the turn ended, it has no turn, or work was done
		return false, false
	}
	if w != nil && w.Turn != obs.Turn.ID {
		w = nil // another turn: whatever the old one did, this one is new
	}
	gap := t.missedTime() > 0
	if w == nil {
		// A turn that has been silent for stall_after would be nudged, and
		// a nudge checks connectivity first; check now, once, so a silent
		// turn's outage is seen whatever is decided next.
		if since := obs.Turn.LastActivityAt; !since.IsZero() && t.now.Sub(since) >= t.loop.Progress.StallAfter.Duration {
			t.networkDown()
		}
		var outageAt time.Time
		switch {
		case t.networkGated:
			outageAt = t.st.NetworkDownSince
			if outageAt.IsZero() {
				outageAt = t.now
			}
		case gap:
			outageAt = t.st.CheckedAt // the last check before the gap
		default:
			return false, false // no outage seen during this turn
		}
		w = &state.HungTurn{Turn: obs.Turn.ID, OutageAt: outageAt}
	} else {
		copy := *w
		w = &copy
		if t.networkGated || gap {
			// Another outage: the time since the network came back starts
			// again, and so does any wait.
			w.BackAt, w.Since = time.Time{}, time.Time{}
		}
	}
	if t.networkGated {
		keep(w)
		return false, false
	}
	if w.BackAt.IsZero() {
		w.BackAt = t.now
	}
	last := obs.Turn.LastActivityAt
	if last.IsZero() || !last.Before(w.OutageAt) {
		keep(nil) // the turn did something after the outage: it is alive
		return false, true
	}
	holds := !obs.Turn.ToolsRunning && obs.Queued == 0 && obs.Background == 0 && obs.ActiveChildren == 0 && !obs.ChildrenUnverified &&
		!obs.AwaitingInput && !obs.Retrying && !obs.RetryUnverified &&
		unchanged >= t.loop.Progress.StallAfter.Duration
	if !holds {
		w.Since = time.Time{}
		keep(w)
		return false, false
	}
	if w.Since.IsZero() {
		w.Since = t.now
		if !w.Reported && (t.opts.DryRun || t.reserveReport(hungTurnEvent)) {
			w.Reported = true
			askAt := t.now.Add(max(0, t.hungTurnPatience()-unchanged))
			t.report(hungTurnEvent, fmt.Sprintf("%s: the turn on %s has logged nothing since %s, before a network outage or a time in which no check ran, and may be hung. keepgoing is waiting on it, not nudging it, and has not stopped it; it asks a person at %s if the turn is still silent, when the nudges it replaces would have ended.",
				t.loop.Name, t.res.Target, last.UTC().Format(time.RFC3339), askAt.UTC().Format(time.RFC3339)),
				map[string]any{"target": t.res.Target, "silentSince": last, "outageAt": w.OutageAt, "askAt": askAt})
		}
	}
	keep(w)
	return true, false
}

// hungTurnPatience is how long the stall clock runs before a person is asked
// about a hung turn: the time the stall rule takes to reach its own hold
// under the same configuration (one stall_after, then a nudge every
// min_wake_interval), so the hold never comes later than it would have.
func (t *tick) hungTurnPatience() time.Duration {
	p := t.loop.Progress
	return decide.SilentRetryPatience(p.StallAfter.Duration, p.MinWakeInterval.Duration, p.MaxNudges)
}

package supervise

import (
	"context"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

// A context reading may use at most contextTimeout of a check, and is not
// started with less than minContextTime left after what the check still
// reserves and the time a routine report needs (minReportTime), so it never
// costs a report its turn. It comes after the action, so it can never delay
// a send.
const (
	contextTimeout = 5 * time.Second
	minContextTime = time.Second
)

// contextReadDue says whether this check reads the target's context size:
// a check that observed the target and either sends to it (a wake, a
// re-prompt or a nudge that was not held back, with the target unchanged)
// or is the first to see its turn ended. previous is the state the last
// observed check recorded. A pure function of what the check already
// decided; the decision never depends on it.
func contextReadDue(d decide.Decision, obs decide.Observation, previous string, heldNudge, sameTarget, dryRun bool) bool {
	if dryRun || !sameTarget || d.Action == decide.AwaitUser || d.Action == decide.Stop {
		return false
	}
	switch d.Action {
	case decide.Wake, decide.Reprompt:
		return true
	case decide.Nudge:
		return !heldNudge
	}
	return obs.State == decide.Idle && previous != "" && previous != string(decide.Idle)
}

// readContext records the target's context size when the adapter can read
// it and the check has the time. Nothing about it can fail the check, warn,
// or change what the check decided or did: an unavailable reading leaves
// the last one in place, with its own time, and adds nothing to the history.
func (t *tick) readContext(a adapter.Adapter, target string, d decide.Decision, obs decide.Observation, previous string) {
	r, ok := a.(adapter.ContextReader)
	if !ok || !contextReadDue(d, obs, previous, t.heldNudge, t.st.Target() == target, t.opts.DryRun) {
		return
	}
	limit := min(contextTimeout, t.left()-minReportTime)
	if limit < minContextTime {
		return
	}
	ctx, cancel := context.WithTimeout(t.ctx, limit)
	defer cancel()
	got, err := r.ReadContext(ctx)
	if err != nil {
		return
	}
	t.st.NoteContext(target, state.ContextReading{UsedTokens: got.UsedTokens, WindowTokens: got.WindowTokens, Estimated: got.Estimated, At: t.now})
}

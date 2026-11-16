package supervise

import (
	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

// turnUsageReadDue says whether this check records the target's latest turn
// usage: a check that observed a turn that has ended (idle or in error),
// whose target did not change, that is not a dry run and not a stop. A pure
// function of what the check already decided; the decision never depends
// on it.
func turnUsageReadDue(d decide.Decision, obs decide.Observation, sameTarget, dryRun bool) bool {
	if dryRun || !sameTarget || d.Action == decide.Stop {
		return false
	}
	return obs.State == decide.Idle || obs.State == decide.Error
}

// readTurnUsage records the token counts the target's agent reported for its
// latest finished turn, when the adapter reports them. It reads the turn's
// saved output only: no provider is asked. A turn already recorded is not
// counted again. Nothing about it can fail the check, warn, or change what
// the check decided or did.
func (t *tick) readTurnUsage(a adapter.Adapter, target string, d decide.Decision, obs decide.Observation) {
	r, ok := a.(adapter.TurnUsageReader)
	if !ok || !turnUsageReadDue(d, obs, t.st.Target() == target, t.opts.DryRun) {
		return
	}
	u, ok := r.LatestTurnUsage()
	if !ok {
		return
	}
	t.st.NoteTurnUsage(target, state.TurnUsageReading{Turn: u.ID, At: t.now, Input: u.Input, CachedInput: u.CachedInput,
		CacheWriteInput: u.CacheWriteInput, Output: u.Output, ReasoningOutput: u.ReasoningOutput})
}

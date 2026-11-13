package supervise

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// retrySilentFor tracks how long the active target has been observed with an
// expired retry notice. Unverified observations keep the episode.
func (t *tick) retrySilentFor(obs decide.Observation, target string) time.Duration {
	var s *state.SilentRetry
	if old := t.st.SilentRetry; old != nil && old.Target == target {
		copy := *old
		s = &copy
	}
	switch {
	case obs.RetryUnverified || obs.State == decide.Unknown:
	case obs.State == decide.Active && obs.RetryExpired:
		if s == nil {
			s = &state.SilentRetry{Target: target, Since: t.now}
		}
	default:
		s = nil
	}
	if !t.opts.DryRun {
		t.st.SilentRetry = s
	}
	if s == nil || !(obs.State == decide.Active && obs.RetryExpired) {
		return 0
	}
	return max(time.Duration(0), t.now.Sub(s.Since))
}

// stallAfterRetry grants one ordinary stall interval when a previously observed
// retry ends while the same target remains active. Git progress, nudges already
// spent, and hard bounds are unchanged. Unknown evidence is not recovery.
func (t *tick) stallAfterRetry(obs decide.Observation, target string, unchanged time.Duration) time.Duration {
	var r *state.StallRetry
	if old := t.st.StallRetry; old != nil && old.Target == target {
		copy := *old
		r = &copy
	}
	switch {
	case obs.RetryUnverified || obs.State == decide.Unknown:
		// Keep the pending episode; Decide withholds work independently.
	case obs.Retrying:
		r = &state.StallRetry{Target: target, Waiting: true}
	case obs.RetryExpired:
		// The retry went silent; that is not a recovery and earns no grace.
		r = nil
	case obs.State != decide.Active:
		r = nil
	case r != nil && r.Waiting:
		r.Waiting = false
		r.ResumedAt = t.now
	}
	if r != nil && !r.Waiting && !r.ResumedAt.IsZero() {
		// Clamp the elapsed grace to zero if the wall clock moved backwards.
		unchanged = min(unchanged, max(time.Duration(0), t.now.Sub(r.ResumedAt)))
	}
	if !t.opts.DryRun {
		t.st.StallRetry = r
	}
	return unchanged
}

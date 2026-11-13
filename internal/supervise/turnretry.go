package supervise

import (
	"errors"
	"fmt"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// turnRetryRefused is recorded when the provider refused a retry or its
// result could not be read.
const turnRetryRefused = "refused"

// turnRetryUnsupported is recorded when the provider has no turn retry.
const turnRetryUnsupported = "unsupported"

// retryTurn carries out a re-prompt decision by asking the provider to re-run
// the failed turn by reference. No text is sent, so a session that keeps
// failing does not collect another copy of the task for every attempt.
//
// It is the same recovery attempt as a prompt would have been: it reserves
// one send, and it spends the re-prompt cap and starts the backoff whatever
// the provider answered, so a refused or stuck retry still ends in the
// ordinary hold for a person.
//
// done is false only when the provider reported that there is no failed turn,
// or that it has no turn retry at all.
// The caller then sends the prompt under the same reservation, and counts the
// attempt once there.
func (t *tick) retryTurn(r adapter.TurnRetrier, d decide.Decision) (done bool, err error) {
	if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
		return true, err
	}
	t.res.Recovery = string(decide.RecoverByTurnRetry)
	res, err := r.RetryFailedTurn(t.ctx)
	t.reserve = reserveReports // the provider has been asked
	outcome := string(res.Outcome)
	var unsupported adapter.ErrUnsupported
	switch {
	case errors.As(err, &unsupported):
		// This provider version cannot retry a turn: prompt, as before.
		outcome, err, res.Outcome = turnRetryUnsupported, nil, adapter.TurnRetryNothing
	case err != nil:
		outcome = turnRetryRefused
	}
	t.incidentTurnRetry = outcome
	t.st.LastTurnRetry = &state.TurnRetry{CheckAt: t.now, Target: t.res.Target, Outcome: outcome, Attempt: res.Attempt}
	if err == nil && res.Outcome == adapter.TurnRetryNothing {
		return false, nil
	}
	c := decide.Attempted(d.Action, decide.Counters{
		Nudges: t.st.Nudges, Reprompts: t.st.Reprompts, LastSendAt: t.st.LastWakeAt, LastRepromptAt: t.st.LastRepromptAt,
	}, t.now)
	t.st.Nudges, t.st.Reprompts, t.st.LastWakeAt, t.st.LastRepromptAt = c.Nudges, c.Reprompts, c.LastSendAt, c.LastRepromptAt
	switch {
	case err != nil:
		// Refused or unreadable. Nothing else is sent: an unknown result is
		// not evidence that the turn is gone.
		return true, err
	case res.Outcome == adapter.TurnRetryAlreadyQueued:
		t.warn("a retry of the failed turn is already queued on %s; nothing was sent", t.res.Target)
		return true, nil
	}
	what := "retried the failed turn on"
	if res.Outcome == adapter.TurnRetryQueued {
		what = "queued a retry of the failed turn on"
	}
	data := map[string]any{"recovery": string(decide.RecoverByTurnRetry), "delivery": outcome}
	if res.Attempt > 0 {
		data["attempt"] = res.Attempt
	}
	t.report(string(d.Action), fmt.Sprintf("%s: %s %s without a new prompt (%s)", t.loop.Name, what, t.res.Target, d.Reason), data)
	return true, nil
}

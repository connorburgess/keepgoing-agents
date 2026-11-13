package supervise

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Outcomes recorded beside the provider's own (retried, queued,
// nothing_to_retry, already_queued).
const (
	// turnRetryRefused: the provider refused or its result could not be read.
	turnRetryRefused = "refused"
	// turnRetryUnsupported: the provider has no turn retry.
	turnRetryUnsupported = "unsupported"
	// turnRetryNotInError: the provider reported no failed turn and the session
	// was no longer in error, or its status could not be re-read.
	turnRetryNotInError = "not_in_error"
	// turnRetryQueueUnconfirmed: the provider kept answering that a retry was
	// already queued while reporting nothing queued; see turnRetryMaxNoOps.
	turnRetryQueueUnconfirmed = "queue_unconfirmed"
)

// turnRetryMaxNoOps is how many consecutive checks may be answered "a retry
// is already queued" while the session reports nothing queued. Consecutive
// means with no other check between them: the count is cleared by every
// check that does not end in that answer. That pair
// contradicts itself: a retry that is waiting is a queued message, and a
// session in error with queued messages is not re-prompted at all. Beyond
// this the answer is treated as unknown and the ordinary error rules apply,
// so an inconsistent provider cannot be asked on every check for ever.
const turnRetryMaxNoOps = 3

// turnRetryMode is the send-graph mode of a turn retry: it has no prompt.
const turnRetryMode = "turn_retry"

// A recovery has one send reservation of 60s. The worst case is a retry, a
// status re-read and then the prompt: 12s + 10s + the adapter's own 30s, each
// with up to 2s for a killed command to release its output, 58s in all.
const (
	turnRetryTimeout   = 12 * time.Second
	turnRecheckTimeout = 10 * time.Second
)

// retryTurn carries out a re-prompt decision by asking the provider to re-run
// the failed turn by reference. No text is sent, so a session that keeps
// failing does not collect another copy of the task for every attempt.
//
// It is the same recovery attempt as a prompt would have been: it reserves
// one send, spends the re-prompt cap and starts the backoff. Two answers are
// not keepgoing's attempt and give both back: a retry the provider already
// has queued, and a session that is no longer in error.
//
// done is false only when the prompt is the right recovery after all: the
// provider reported no failed turn and the session is still in error, or the
// provider has no turn retry. The caller then sends the prompt under the same
// reservation and counts the attempt once there.
func (t *tick) retryTurn(a adapter.Adapter, r adapter.TurnRetrier, d decide.Decision) (done bool, err error) {
	if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
		return true, err
	}
	t.res.Recovery = string(decide.RecoverByTurnRetry)
	attempt := t.graphBegin(t.res.Target, string(d.Action), turnRetryMode, prompt.Message{})
	ctx, cancel := context.WithTimeout(t.ctx, turnRetryTimeout)
	res, err := r.RetryFailedTurn(ctx)
	cancel()
	graphOutcome := "adapter_returned"
	if err != nil {
		graphOutcome = "adapter_error"
	}
	t.graphFinish(attempt, graphOutcome)

	outcome := string(res.Outcome)
	var unsupported adapter.ErrUnsupported
	fallBack := false
	switch {
	case errors.As(err, &unsupported):
		// This provider version cannot retry a turn: prompt, as before.
		outcome, err, fallBack = turnRetryUnsupported, nil, true
	case err != nil:
		outcome = turnRetryRefused
	case res.Outcome == adapter.TurnRetryNothing:
		// "No failed turn" also means "not in error any more": something else
		// may have restarted the session since it was observed. A prompt in
		// auto mode would steer that live turn, so the status is read again.
		ctx, cancel := context.WithTimeout(t.ctx, turnRecheckTimeout)
		failed, checkErr := r.StillFailed(ctx)
		cancel()
		if checkErr == nil && failed {
			fallBack = true
		} else {
			outcome = turnRetryNotInError
		}
	}
	if outcome == string(adapter.TurnRetryAlreadyQueued) {
		// record clears the count on every check that does not set this.
		t.turnRetryAlreadyQueued = true
		t.st.TurnRetryNoOps++
		if t.st.TurnRetryNoOps > turnRetryMaxNoOps {
			// Unknown, not "queued". The prompt is the recovery, but only for a
			// session still in error with nothing queued, as for "no failed
			// turn": otherwise it would steer a live turn or sit beside a
			// retry that really is waiting.
			ctx, cancel := context.WithTimeout(t.ctx, turnRecheckTimeout)
			failed, checkErr := r.StillFailed(ctx)
			cancel()
			if checkErr == nil && failed {
				// This check ends in a prompt, not in "already queued", so the
				// count starts again from it.
				outcome, fallBack, t.turnRetryAlreadyQueued = turnRetryQueueUnconfirmed, true, false
			}
		}
	}
	t.incidentTurnRetry = outcome
	t.st.LastTurnRetry = &state.TurnRetry{CheckAt: t.now, Target: t.res.Target, Outcome: outcome, Attempt: res.Attempt}
	if fallBack {
		return false, nil
	}
	// From here the send reservation is finished with, whatever happens.
	defer func() { t.reserve = max(0, t.reserve-reserveSend) }()

	if outcome == turnRetryNotInError || outcome == string(adapter.TurnRetryAlreadyQueued) {
		// The provider did nothing for this request, and said so. It is not an
		// attempt of keepgoing's: no cap, no backoff, and the send is returned.
		if err := state.RefundSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
			return true, err
		}
		if outcome == turnRetryNotInError {
			t.warn("no failed turn to retry on %s and it is no longer in error; nothing was sent", t.res.Target)
		} else {
			t.warn("a retry of the failed turn is already queued on %s; nothing was sent", t.res.Target)
		}
		return true, nil
	}
	c := decide.Attempted(d.Action, decide.Counters{
		Nudges: t.st.Nudges, Reprompts: t.st.Reprompts, LastSendAt: t.st.LastWakeAt, LastRepromptAt: t.st.LastRepromptAt,
	}, t.now)
	t.st.Nudges, t.st.Reprompts, t.st.LastWakeAt, t.st.LastRepromptAt = c.Nudges, c.Reprompts, c.LastSendAt, c.LastRepromptAt
	if err != nil {
		// Refused or unreadable. Nothing else is sent: an unexplained refusal
		// is not evidence that the turn is gone.
		return true, err
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
	return true, t.childNoticeAfterRetry(a, d)
}

// childNoticeAfterRetry delivers the errored-children notice that this
// recovery would have carried had it been a prompt. A turn retry has no text
// to carry it on, so it is sent as its own short notice-only message, the
// same one the extra wake for errored children uses. It never contains the
// task, so it cannot stack task prompts. It is one more send against the
// allowance. Where the re-prompt would have carried no notice, because no
// child has newly entered error, this sends nothing.
func (t *tick) childNoticeAfterRetry(a adapter.Adapter, d decide.Decision) error {
	childErrors := decide.ChildErrorNote(t.decisionInput, d)
	if childErrors == 0 {
		return nil
	}
	if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
		// No allowance for it: the notice stays unannounced for a later wake.
		t.warn("errored-children notice not sent after the turn retry: %v", err)
		return nil
	}
	err := t.send(a, prompt.ChildErrorsOnlyRetried(childErrors, t.decisionInput.Obs.ActiveChildren, t.lanesRetried), adapter.ModeAuto, t.res.Target, string(d.Action))
	t.childNoticeSent(childErrors, err)
	if err != nil {
		t.warn("errored-children notice after the turn retry: %v", err)
	}
	return nil
}

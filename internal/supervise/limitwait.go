package supervise

import (
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// limitWaitEvent is the report event for a long limit wait.
const limitWaitEvent = "limit_wait"

// limitWaitResetSlack is how much later a reported reset must move before the
// same wait is reported again. Smaller movements are treated as the provider
// restating the same reset.
const limitWaitResetSlack = time.Hour

// reportLongLimitWait runs after the decision and its action, on the path
// where the target was observed. It never changes the decision, the action or
// a control: the loop keeps checking, and a recovery takes effect as before.
//
// When the check is waiting on a provider limit, no handoff can happen
// (failover is off for the error class, or no fallback is confirmed) and the
// reported reset is further away than errors.report_wait_after, it requests
// one report for that wait. The record in state.json makes it once: later
// checks and restarts see it and stay quiet. It is cleared when an observed
// check is no longer waiting on a limit, so a later wait is reported again,
// and replaced when the reset moves materially later.
func (t *tick) reportLongLimitWait(target string, obs decide.Observation) {
	if t.opts.DryRun {
		return
	}
	kind := t.incidentLimitWait
	if t.res.Action != string(decide.None) || kind == "" {
		// Observed and not waiting on a limit: the wait, if any, is over.
		t.st.LimitWaitReport = nil
		return
	}
	// A reset inside wait_if_reset_within is a short wait chosen over a handoff,
	// and without a reported reset the engine does not wait at all.
	if kind == decide.WaitResetSoon || obs.ResetsAt.IsZero() {
		return
	}
	// Loaded configuration always has a positive threshold (default 6h). A
	// zero value means it was never set, and nothing is reported.
	threshold := t.loop.Errors.ReportWaitAfter.Duration
	resetIn := obs.ResetsAt.Sub(t.now)
	if threshold <= 0 || resetIn <= threshold {
		return
	}
	subscribed := t.loop.Report.ReportsEvent(limitWaitEvent)
	if prev := t.st.LimitWaitReport; prev != nil && prev.Target == target && !obs.ResetsAt.After(prev.ResetsAt.Add(limitWaitResetSlack)) {
		// The same wait. One attempt was made, or nothing was subscribed then
		// and nothing is now.
		if prev.Outcome != state.LimitWaitNotSubscribed || !subscribed {
			return
		}
	}
	rec := &state.LimitWaitReport{Target: target, ResetsAt: obs.ResetsAt.UTC(), DetectedAt: t.now, Outcome: state.LimitWaitNotSubscribed}
	t.st.LimitWaitReport = rec
	t.incidentLimitWaitReport = rec.Outcome
	if !subscribed {
		return
	}
	reason := "no confirmed fallback"
	if kind == decide.WaitFailoverDisabled {
		reason = "failover disabled"
	}
	text := fmt.Sprintf("%s: %s is waiting on a provider limit that resets in about %s, and no handoff is possible (%s). keepgoing keeps checking and continues when the limit lifts; a person may want to retarget or review failover.",
		t.loop.Name, target, resetIn.Round(time.Minute), reason)
	rec.Outcome = state.LimitWaitRequested
	err := t.opts.Reporter.Report(t.ctx, report.Event{Type: limitWaitEvent, Loop: t.loop.Name, Text: text, At: t.now,
		Data: map[string]any{"target": target, "resetsAt": rec.ResetsAt, "reason": string(kind), "reportWaitAfter": t.loop.Errors.ReportWaitAfter.Duration.String()}})
	if err != nil {
		// One attempt per wait: a failed delivery is recorded, not retried.
		rec.Outcome = state.LimitWaitFailed
		t.warn("report %s: %v", limitWaitEvent, err)
	}
	t.incidentLimitWaitReport = rec.Outcome
}

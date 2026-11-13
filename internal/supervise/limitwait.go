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

// A wait is reported again only when its reset moves later than the reset
// last reported by a margin that doubles with each report (1h, then 2h), and
// never more than limitWaitMaxReports times. A provider whose reset keeps
// sliding forward therefore cannot produce an unbounded stream.
const (
	limitWaitResetSlack = time.Hour
	limitWaitMaxReports = 3
)

// limitWaitEnded reports positive evidence that the wait is over: the loop
// moved to another target, the session finished a turn or is working without
// a provider retry, or it is in error with no reset still ahead. A check that
// merely decided something else (outside the window, queued messages, an
// unknown or pending state, a provider retry) says nothing about the limit.
func limitWaitEnded(before, after string, obs decide.Observation, now time.Time) bool {
	switch {
	case after != before:
		return true
	case obs.State == decide.Idle:
		return true
	case obs.State == decide.Active:
		return !obs.Retrying && !obs.RetryUnverified
	case obs.State == decide.Error:
		return !obs.ResetsAt.After(now)
	}
	return false
}

// reportLongLimitWait runs after the decision and its action, on the path
// where the target was observed. It never changes the decision, the action or
// a control: the loop keeps checking, and a recovery takes effect as before.
//
// When the check is waiting on a provider limit, no handoff can happen
// (failover is off for the error class, or no fallback is confirmed) and the
// reported reset is further away than errors.report_wait_after, it requests
// one report for that wait. The wait is saved to state.json before the send,
// so a crash or a restart cannot repeat it: a report is sent at most once,
// and an interrupted one is lost, like any other single-attempt delivery.
func (t *tick) reportLongLimitWait(target string, obs decide.Observation) {
	if t.opts.DryRun {
		return
	}
	kind := t.incidentLimitWait
	if t.res.Action != string(decide.None) || kind == "" {
		if limitWaitEnded(target, t.res.Target, obs, t.now) {
			t.st.LimitWaitReport = nil
		}
		return
	}
	// A short wait chosen over a confirmed fallback is the operator's own
	// wait_if_reset_within setting, not a wait with no way out.
	if kind == decide.WaitResetSoon && t.handoffAvailable {
		return
	}
	// Loaded configuration always has a positive threshold (default 6h). A
	// zero value means it was never set, and nothing is reported.
	threshold := t.loop.Errors.ReportWaitAfter.Duration
	resetIn := obs.ResetsAt.Sub(t.now)
	if threshold <= 0 || obs.ResetsAt.IsZero() || resetIn <= threshold {
		return
	}
	subscribed := t.loop.Report.ReportsEvent(limitWaitEvent)
	reports := 0
	if prev := t.st.LimitWaitReport; prev != nil && prev.Target == target {
		// The same wait.
		reports = prev.Reports
		if prev.Outcome == state.LimitWaitNotSubscribed {
			if !subscribed {
				return
			}
		} else {
			margin := limitWaitResetSlack << max(0, prev.Reports-1)
			if prev.Reports >= limitWaitMaxReports || !obs.ResetsAt.After(prev.ResetsAt.Add(margin)) {
				return
			}
		}
	}
	rec := &state.LimitWaitReport{Target: target, ResetsAt: obs.ResetsAt.UTC(), DetectedAt: t.now, Outcome: state.LimitWaitNotSubscribed, Reports: reports}
	if !subscribed {
		t.st.LimitWaitReport = rec
		t.incidentLimitWaitReport = rec.Outcome
		return
	}
	rec.Outcome, rec.Reports = state.LimitWaitRequested, reports+1
	// A report that cannot be sent in this check for lack of time must not use
	// up its single attempt: nothing is recorded, so the next check sends it.
	if !t.canReport(limitWaitEvent) {
		t.warn("report %s put off to the next check: the network is down or no time is left in this one", limitWaitEvent)
		return
	}
	// Intent first, as holds and completions are saved before their reports.
	// If it cannot be saved nothing is sent, so an unwritable state directory
	// cannot turn into a report on every check.
	saved, err := state.Load(t.loop.StateDir)
	if err == nil {
		saved.LimitWaitReport = rec
		err = state.Save(t.loop.StateDir, saved)
	}
	if err != nil {
		t.warn("report %s: not sent because the wait could not be recorded", limitWaitEvent)
		return
	}
	t.st.LimitWaitReport = rec
	reason := "no confirmed fallback"
	cause := decide.WaitNoFallback
	if kind == decide.WaitFailoverDisabled {
		reason, cause = "failover disabled", kind
	}
	text := fmt.Sprintf("%s: %s is waiting on a provider limit that resets in about %s, and no handoff is possible (%s). keepgoing keeps checking and continues when the limit lifts; a person may want to retarget or review failover.",
		t.loop.Name, target, resetIn.Round(time.Minute), reason)
	if rec.Reports > 1 {
		text += " The reported reset has moved later since the last report for this wait."
	}
	ctx, cancel, ok := t.reportContext(limitWaitEvent, text)
	if !ok {
		return // cannot happen: canReport was checked before the attempt was reserved
	}
	defer cancel()
	err = t.opts.Reporter.Report(ctx, report.Event{Type: limitWaitEvent, Loop: t.loop.Name, Text: text, At: t.now,
		Data: map[string]any{"target": target, "resetsAt": rec.ResetsAt, "reason": string(cause), "reportWaitAfter": threshold.String(), "report": rec.Reports}})
	if err != nil {
		// One attempt: a failed delivery is recorded, not retried.
		rec.Outcome = state.LimitWaitFailed
		t.warn("report %s: %v", limitWaitEvent, err)
	}
	t.incidentLimitWaitReport = rec.Outcome
}

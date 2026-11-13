package main

import (
	"fmt"
	"io"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// limitWaitStatus shows the long limit wait recorded by the last observed
// check, if any. It is historical: a held or stopped supervisor does not
// refresh it, and the record is cleared only by a check that observes the
// target no longer waiting on a limit.
type limitWaitStatus struct {
	State           string     `json:"state"` // none, reported, report_failed, not_subscribed
	Target          string     `json:"target,omitempty"`
	ResetsAt        *time.Time `json:"resetsAt,omitempty"`
	ResetPassed     bool       `json:"resetPassed,omitempty"`
	DetectedAt      *time.Time `json:"detectedAt,omitempty"`
	ReportWaitAfter string     `json:"reportWaitAfter"`
	Subscribed      bool       `json:"subscribed"`
	Receipt         string     `json:"receipt,omitempty"`
	// Reports counts report attempts in this wait; at most three are made.
	Reports int `json:"reports,omitempty"`
}

func describeLimitWait(r *state.LimitWaitReport, loop config.Loop, now time.Time) limitWaitStatus {
	out := limitWaitStatus{State: "none", ReportWaitAfter: loop.Errors.ReportWaitAfter.Duration.String(), Subscribed: loop.Report.ReportsEvent("limit_wait")}
	if r == nil || r.Target == "" || r.ResetsAt.IsZero() {
		return out
	}
	switch r.Outcome {
	case state.LimitWaitRequested:
		out.State, out.Receipt = "reported", "unverified"
	case state.LimitWaitFailed:
		out.State = "report_failed"
	default:
		out.State = "not_subscribed"
	}
	out.Target, out.ResetsAt, out.DetectedAt, out.ResetPassed, out.Reports = r.Target, &r.ResetsAt, &r.DetectedAt, !now.Before(r.ResetsAt), r.Reports
	return out
}

func printLimitWait(w io.Writer, s limitWaitStatus) {
	if s.State == "none" {
		return
	}
	fmt.Fprintf(w, "long limit wait: %s was waiting on a provider limit with no handoff possible at the check of %s; reported reset %s", s.Target, when(*s.DetectedAt), s.ResetsAt.Format(time.RFC3339))
	if s.ResetPassed {
		fmt.Fprint(w, " (now passed; the next check decides)")
	}
	fmt.Fprintln(w)
	switch s.State {
	case "reported":
		fmt.Fprintf(w, "  a limit_wait report was requested for this wait (attempt %d of at most 3, each only after the reset moves later); receipt unverified, see notifications. The loop is not held and keeps checking.\n", max(1, s.Reports))
	case "report_failed":
		fmt.Fprintln(w, "  the limit_wait report for this wait FAILED and is not retried; see notifications. The loop is not held and keeps checking.")
	default:
		fmt.Fprintln(w, "  nothing was sent: report.events does not include limit_wait. The loop is not held and keeps checking.")
	}
}

// turnRetryStatus shows the last recovery made by retrying a failed turn by
// reference. It is saved with a completed check and is historical.
type turnRetryStatus struct {
	Availability string           `json:"availability"`
	Last         *state.TurnRetry `json:"last,omitempty"`
	Delivery     string           `json:"delivery"`
}

func describeTurnRetry(last *state.TurnRetry) turnRetryStatus {
	r := turnRetryStatus{Availability: "unavailable", Delivery: "unverified"}
	if last == nil || last.CheckAt.IsZero() || last.Target == "" {
		return r
	}
	switch last.Outcome {
	case "retried", "queued", "nothing_to_retry", "not_in_error", "already_queued", "refused", "unsupported":
	default:
		return r
	}
	r.Availability, r.Last = "recorded", last
	return r
}

func printTurnRetry(w io.Writer, r turnRetryStatus) {
	if r.Last == nil {
		return
	}
	p := r.Last
	fmt.Fprintf(w, "last recorded turn retry: %s on %s; check %s", p.Outcome, p.Target, when(p.CheckAt))
	if p.Attempt > 0 {
		fmt.Fprintf(w, "; provider attempt %d", p.Attempt)
	}
	fmt.Fprintln(w)
	switch p.Outcome {
	case "retried", "queued":
		fmt.Fprintln(w, "  the failed turn was re-run by reference; no prompt was sent. Whether the turn then succeeded is unverified")
	case "nothing_to_retry":
		fmt.Fprintln(w, "  the provider reported no failed turn, so the prompt was sent instead")
	case "not_in_error":
		fmt.Fprintln(w, "  the provider reported no failed turn and the session was no longer in error (or its status could not be read); nothing was sent and no attempt was counted")
	case "unsupported":
		fmt.Fprintln(w, "  this provider version cannot retry a turn, so the prompt was sent instead")
	case "already_queued":
		fmt.Fprintln(w, "  a retry of that turn was already waiting; nothing was sent and no attempt was counted")
	default:
		fmt.Fprintln(w, "  the provider refused the retry or its result could not be read; nothing was sent")
	}
}

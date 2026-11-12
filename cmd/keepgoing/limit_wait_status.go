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
	out.Target, out.ResetsAt, out.DetectedAt, out.ResetPassed = r.Target, &r.ResetsAt, &r.DetectedAt, !now.Before(r.ResetsAt)
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
		fmt.Fprintln(w, "  one limit_wait report was requested for this wait; receipt unverified, see notifications. The loop is not held and keeps checking.")
	case "report_failed":
		fmt.Fprintln(w, "  the one limit_wait report for this wait FAILED and is not retried; see notifications. The loop is not held and keeps checking.")
	default:
		fmt.Fprintln(w, "  nothing was sent: report.events does not include limit_wait. The loop is not held and keeps checking.")
	}
}

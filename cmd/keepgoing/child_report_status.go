package main

import (
	"fmt"
	"io"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// childReportStatus shows the owner report recorded for children in error, if
// any. It is historical: a held or stopped supervisor does not refresh it,
// and the record is cleared only by a check that sees no child in error.
type childReportStatus struct {
	State      string     `json:"state"` // none, reported, report_failed, not_subscribed, capped
	DetectedAt *time.Time `json:"detectedAt,omitempty"`
	// Counts seen by the detecting check; lower bounds when ListingComplete is false.
	ErroredChildren int  `json:"erroredChildren,omitempty"`
	RunningChildren int  `json:"runningChildren,omitempty"`
	ListingComplete bool `json:"listingComplete"`
	Subscribed      bool `json:"subscribed"`
	// ReportConfigured is false when the loop has no report section at all.
	ReportConfigured bool `json:"reportConfigured"`
	// Reports counts report attempts in this episode; at most three are made.
	Reports int    `json:"reports,omitempty"`
	Receipt string `json:"receipt,omitempty"`
}

func describeChildReport(r *state.ChildErrorReport, loop config.Loop) childReportStatus {
	out := childReportStatus{State: "none", ListingComplete: true, Subscribed: loop.Report.ReportsEvent("child_error"), ReportConfigured: loop.Report != nil}
	if r == nil || r.DetectedAt.IsZero() {
		return out
	}
	switch r.Outcome {
	case state.ChildReportRequested:
		out.State, out.Receipt = "reported", "unverified"
	case state.ChildReportFailed:
		out.State = "report_failed"
	case state.ChildReportCapped:
		out.State = "capped"
	default:
		out.State = "not_subscribed"
	}
	out.DetectedAt, out.ErroredChildren, out.RunningChildren, out.ListingComplete, out.Reports = &r.DetectedAt, r.Errored, r.Running, r.Complete, r.Reports
	return out
}

func printChildReport(w io.Writer, s childReportStatus) {
	if s.State == "none" {
		return
	}
	bound := ""
	if !s.ListingComplete {
		bound = " (child listing incomplete: lower bounds)"
	}
	fmt.Fprintf(w, "child errors: %d direct child thread(s) in error and %d still running at the check of %s%s\n", s.ErroredChildren, s.RunningChildren, when(*s.DetectedAt), bound)
	switch s.State {
	case "reported":
		fmt.Fprintf(w, "  a child_error report was requested (report %d of at most 3 in this episode); receipt unverified, see notifications. The loop is not held.\n", max(1, s.Reports))
	case "report_failed":
		fmt.Fprintln(w, "  the child_error report FAILED and is not retried; see notifications. The loop is not held.")
	case "capped":
		fmt.Fprintf(w, "  more children failed after %d reports; no further child_error report is sent until every child has recovered. The loop is not held.\n", s.Reports)
	default:
		why := "report.events does not include child_error"
		if !s.ReportConfigured {
			why = "this loop has no report section"
		}
		if s.Reports > 0 {
			// Unsubscribed part-way through: earlier reports of this episode went out.
			fmt.Fprintf(w, "  %d child_error report(s) were requested earlier in this episode; the latest failure was not sent: %s. The loop is not held.\n", s.Reports, why)
		} else {
			fmt.Fprintf(w, "  nothing was sent: %s. The loop is not held.\n", why)
		}
	}
}

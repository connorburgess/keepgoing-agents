package main

import (
	"fmt"
	"io"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/report"
)

func printDeliveryStatus(w io.Writer, s report.DeliveryStatus) {
	fmt.Fprintf(w, "notifications: %s; receipt unverified\n", s.State)
	if s.State == "unavailable" {
		fmt.Fprintln(w, "notification warning: delivery history unavailable; check private state storage")
		return
	}
	if s.Latest != nil {
		fmt.Fprintf(w, "latest notification: %s; %s at %s; %d request attempts begun\n", s.Latest.Event, s.Latest.Outcome, s.Latest.UpdatedAt.Format(time.RFC3339), len(s.Latest.Attempts))
	}
	for _, d := range s.Warnings {
		fmt.Fprintf(w, "notification warning (historical #%d): %s; %s at %s; %d request attempts begun", d.Sequence, d.Event, d.Outcome, d.UpdatedAt.Format(time.RFC3339), len(d.Attempts))
		if len(d.Attempts) > 0 && d.Attempts[len(d.Attempts)-1].HTTPStatus != 0 {
			fmt.Fprintf(w, "; HTTP %d", d.Attempts[len(d.Attempts)-1].HTTPStatus)
		}
		fmt.Fprintln(w, "; delivery failed or unconfirmed")
	}
	if len(s.Warnings) > 0 {
		fmt.Fprintln(w, "inspect notification setup and receiver before any manual resend; held checks do not retry delivery")
	}
	if s.Dropped > 0 {
		fmt.Fprintf(w, "notification retention: %d older records dropped; absence of warnings is not proof of delivery\n", s.Dropped)
	}
}

func doctorDeliveries(r *doctorReport, s report.DeliveryStatus) {
	switch {
	case s.State == "unavailable":
		r.add("notifications", "warning", "Notification delivery history is unavailable.", "Check private state storage; no delivery success can be inferred.")
	case s.State == "not_recorded":
		r.add("notifications", "unavailable", "No notification delivery evidence is recorded.", "Reporting may be disabled, unsubscribed, not yet invoked, or from an older build; this is not proof of delivery.")
	case len(s.Warnings) > 0:
		r.add("notifications", "warning", fmt.Sprintf("Retained history contains %d failed or unconfirmed notification deliveries; %d older records were dropped. Receipt is unverified.", len(s.Warnings), s.Dropped), "Inspect status and the receiver before any manual resend. Held checks do not retry delivery; historical warnings do not establish current endpoint health.")
	default:
		r.add("notifications", "ok", fmt.Sprintf("Retained notifications were HTTP accepted; receipt is unverified. %d older records were dropped.", s.Dropped), "HTTP acceptance is not proof that a person or destination application received a notification.")
	}
}

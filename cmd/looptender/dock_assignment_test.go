package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/control"
)

func TestDockAssignmentProjectionUnknownAndCounts(t *testing.T) {
	for _, state := range []string{"not_recorded", "unavailable", "stale_binding", "available"} {
		s := control.AssignmentSummary{Availability: state}
		if state == "available" {
			s.Coverage = "partial"
			s.ObservedAt = &dockNow
			s.RecordedAt = &dockNow
			s.Counts = &control.AssignmentCounts{Assignments: 3, Unknown: 2, Unacknowledged: 1, Unfinished: 3}
		}
		view := dockAssignmentsOf(&statusOut{Assignments: s})
		raw, _ := json.Marshal(view)
		if state != "available" && strings.Contains(string(raw), `"counts"`) {
			t.Fatal("unknown became zero", string(raw))
		}
		if !strings.Contains(string(raw), `"availability":"`+state+`"`) {
			t.Fatal(string(raw))
		}
	}
	partialZero := dockAssignmentsOf(&statusOut{Assignments: control.AssignmentSummary{Availability: "available", Coverage: "partial", Counts: &control.AssignmentCounts{}}})
	if !strings.Contains(partialZero.Text, "additional work is unknown") {
		t.Fatal("partial zero implied global all-clear", partialZero)
	}
	if view := dockAssignmentsOf(nil); view.Availability != "unavailable" || view.Counts != nil {
		t.Fatal(view)
	}
}
func TestDockAssignmentBrowserFixture(t *testing.T) {
	path := os.Getenv("KEEPGOING_ASSIGNMENT_BROWSER_REPORT")
	if path == "" {
		t.Skip("optional fictional fixture output")
	}
	d, _ := dockFixtureReader(t)
	report := d.read(context.Background(), false)
	at := dockNow.Add(-24 * 365 * 60 * 60 * 1000000000)
	for i := range report.Loops {
		l := &report.Loops[i]
		s := control.AssignmentSummary{Availability: "not_recorded"}
		switch l.Name {
		case "docs-site-refresh":
			s = control.AssignmentSummary{Availability: "available", Coverage: "partial", ObservedAt: &at, RecordedAt: &dockNow, Counts: &control.AssignmentCounts{Assignments: 4, Unknown: 2, Unacknowledged: 1, Unfinished: 3, PersonBlocked: 1, DependencyWaiting: 1}, Coordination: &control.AssignmentWakeSummary{State: "attempted_unreconciled", AttemptedAt: &dockNow, NextAction: "looptender assignment show --private; then assignment reconcile-wake --file reconciliation.json --id <record-id> --revision <current-revision>; delivery is not established"}}
		case "billing-api-migration":
			s.Availability = "stale_binding"
		case "mobile-onboarding":
			s.Availability = "unavailable"
		case "search-indexer":
			s = control.AssignmentSummary{Availability: "available", Coverage: "observed_direct_children", ObservedAt: &at, RecordedAt: &dockNow, Counts: &control.AssignmentCounts{Assignments: 2}}
		}
		l.Assignments = dockAssignmentsOf(&statusOut{Assignments: s})
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDockAssignmentWakeRecoveryCopy(t *testing.T) {
	at := dockNow
	view := dockAssignmentsOf(&statusOut{Assignments: control.AssignmentSummary{Availability: "available", Counts: &control.AssignmentCounts{Unknown: 1, Unfinished: 2}, Coordination: &control.AssignmentWakeSummary{State: "attempted_unreconciled", AttemptedAt: &at, NextAction: "looptender assignment reconcile-wake"}}})
	raw, _ := json.Marshal(view)
	if !strings.Contains(view.Text, "delivery unverified") || !strings.Contains(view.Text, "reconcile-wake") || !strings.Contains(view.Text, "1 assignment needs reconciliation") || strings.Contains(string(raw), "grant") {
		t.Fatal(view)
	}
}

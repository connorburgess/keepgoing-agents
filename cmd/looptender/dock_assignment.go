package main

import (
	"github.com/looptender/looptender/internal/control"
	"time"
)

type dockAssignments struct {
	control.AssignmentSummary
	Text string `json:"text"`
}

func dockAssignmentsOf(st *statusOut) dockAssignments {
	s := control.AssignmentSummary{Availability: "unavailable"}
	if st != nil && st.Assignments.Availability != "" {
		s = st.Assignments
	}
	text := "Assignment evidence unavailable. Outcomes are unknown; inspect assignment show privately."
	switch s.Availability {
	case "not_recorded":
		text = "No assignment review is saved. Existing lanes need reconciliation; idle does not mean complete. Next: looptender assignment inventory --json, then assignment review --file plan.json."
	case "stale_binding":
		text = "Assignment evidence belongs to an earlier binding. Outcomes are unknown; review the current goal and lanes."
	case "available":
		if s.Counts == nil {
			s = control.AssignmentSummary{Availability: "unavailable"}
		} else {
			text = dockPlural(s.Counts.Unknown, "assignment needs reconciliation", "assignments need reconciliation")
			if s.Coverage != "observed_direct_children" {
				text += ". Coverage is partial; additional work is unknown."
			}
		}
	}
	if s.Coordination != nil {
		state := "Readiness needs review"
		switch s.Coordination.State {
		case "declared_ready":
			state = "Readiness declared; waiting for an eligible check"
		case "attempted_unreconciled":
			state = "Wake attempted; reconciliation required; delivery unverified"
		case "reviewed_attempt":
			state = "Wake attempt reviewed; new readiness requires an explicit declaration"
		case "ready_required":
			state = "No independent readiness declared"
		}
		text += ". " + state
		if at := s.Coordination.AttemptedAt; at != nil {
			text += " (" + at.Format(time.RFC3339) + ")"
		}
		text += ". Next: " + s.Coordination.NextAction
	}
	// Only validated local summary fields are present; private records never enter
	// dockLoop, even when the identifier display is enabled.
	return dockAssignments{AssignmentSummary: s, Text: text}
}

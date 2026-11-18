package main

import (
	"github.com/looptender/looptender/internal/control"
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
		text = "No assignment review is saved. Existing lanes need reconciliation; idle does not mean complete."
	case "stale_binding":
		text = "Assignment evidence belongs to an earlier binding. Outcomes are unknown; review the current goal and lanes."
	case "available":
		if s.Counts == nil {
			s = control.AssignmentSummary{Availability: "unavailable"}
		} else {
			text = dockPlural(s.Counts.Unknown, "assignment needs reconciliation", "assignments need reconciliation")
		}
	}
	// Only validated local summary fields are present; private records never enter
	// dockLoop, even when the identifier display is enabled.
	return dockAssignments{AssignmentSummary: s, Text: text}
}

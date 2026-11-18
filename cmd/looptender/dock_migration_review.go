package main

import "time"

// A minimized view of the bound, validated status summary. Do not project the
// private review ID, digest, assignment text, identities or paths, even when
// the panel's identifier display is on. This is not a control or a decision.
type dockMigrationReview struct {
	Availability string               `json:"availability"`
	Text         string               `json:"text"`
	ObservedAt   *time.Time           `json:"observedAt,omitempty"`
	Counts       *dockMigrationCounts `json:"counts,omitempty"`
}

type dockMigrationCounts struct {
	Assignments       int `json:"assignments"`
	Unfinished        int `json:"unfinished"`
	Dormant           int `json:"dormant"`
	Unresolved        int `json:"unresolved"`
	DirectLanes       int `json:"directLanes"`
	AbsentAssignments int `json:"absentAssignments"`
}

func dockMigrationReviewOf(st *statusOut) dockMigrationReview {
	unknown := dockMigrationReview{Availability: "unavailable", Text: "Migration review unavailable. Unfinished work is unknown; inspect the private review with looptender transfer reconcile show."}
	if st == nil {
		return unknown
	}
	if st.MigrationReviewError != "" {
		if st.MigrationReviewError == "binding_changed" {
			unknown.Availability = "binding_changed"
			unknown.Text = "Migration review belongs to a different loop binding. Unfinished work is unknown; inspect the private review with looptender transfer reconcile show."
		}
		return unknown
	}
	s := st.MigrationReview
	if s == nil {
		return dockMigrationReview{Availability: "not_recorded", Text: "No migration review is saved for this loop. This does not establish that migration work is complete."}
	}
	at := s.ObservedAt
	return dockMigrationReview{Availability: "available", Text: dockPlural(s.Unfinished, "unfinished assignment declared", "unfinished assignments declared"), ObservedAt: &at,
		Counts: &dockMigrationCounts{Assignments: s.Assignments, Unfinished: s.Unfinished, Dormant: s.Dormant, Unresolved: s.Unresolved, DirectLanes: s.DirectLanes, AbsentAssignments: s.AbsentAssignments}}
}

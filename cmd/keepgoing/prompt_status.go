package main

import (
	"fmt"
	"io"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

type promptSendStatus struct {
	Availability          string            `json:"availability"`
	Last                  *state.PromptSend `json:"last,omitempty"`
	CurrentGoalComparison string            `json:"currentGoalComparison"`
	Delivery              string            `json:"delivery"`
}

func describePromptSend(last *state.PromptSend, goal *control.GoalSummary) promptSendStatus {
	r := promptSendStatus{Availability: "unavailable", CurrentGoalComparison: "unavailable", Delivery: "unverified"}
	if last == nil || !last.Valid() {
		return r
	}
	r.Availability, r.Last = "recorded", last
	switch {
	case last.GoalID == "":
		r.CurrentGoalComparison = "not_included"
	case goal == nil:
	case goal.ID != last.GoalID:
		r.CurrentGoalComparison = "different_goal"
	case goal.Revision != last.GoalRevision:
		r.CurrentGoalComparison = "different_revision"
	default:
		r.CurrentGoalComparison = "same_revision"
	}
	return r
}

func printPromptSend(w io.Writer, r promptSendStatus) {
	if r.Last == nil {
		fmt.Fprintln(w, "last automatic prompt: unavailable; this does not prove nothing was sent")
		return
	}
	p := r.Last
	fmt.Fprintf(w, "last recorded automatic prompt: %s to %s (%s, %s); check %s\n", p.Action, p.Target, p.Mode, p.Outcome, when(p.CheckAt))
	if p.GoalID == "" {
		fmt.Fprintln(w, "  goal context: not included")
	} else {
		fmt.Fprintf(w, "  goal context: %s revision %d; current record: %s\n", p.GoalID, p.GoalRevision, r.CurrentGoalComparison)
	}
	fmt.Fprintln(w, "  saved after adapter return; delivery and execution unverified; queued messages may retain older revisions")
}

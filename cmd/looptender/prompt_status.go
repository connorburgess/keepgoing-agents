package main

import (
	"fmt"
	"io"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

type promptSendStatus struct {
	Availability          string            `json:"availability"`
	Last                  *state.PromptSend `json:"last,omitempty"`
	CurrentGoalComparison string            `json:"currentGoalComparison"`
	Delivery              string            `json:"delivery"`
	// GoalCopy is the latest full goal copy that returned, which lets
	// prompt_goal_on_change send the short line. Diagnostic only.
	GoalCopy *state.GoalCopy `json:"goalCopy,omitempty"`
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
	if p.Bytes > 0 {
		goal := "no goal"
		switch p.GoalForm {
		case "full":
			goal = "goal in full"
		case "short":
			goal = "goal named as unchanged"
		}
		fmt.Fprintf(w, "  size: %d bytes, %s (bytes measured, tokens not)\n", p.Bytes, goal)
	}
	if g := r.GoalCopy; g != nil {
		fmt.Fprintf(w, "  last full goal copy: %s revision %d to %s; check %s; %d short send(s) since\n", g.GoalID, g.GoalRevision, g.Target, when(g.At), g.ShortSends)
	}
	fmt.Fprintln(w, "  saved after adapter return; delivery and execution unverified; queued messages may retain older revisions")
}

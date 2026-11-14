package main

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A loop on a step usage-aware timing chose is described from the move on
// record, not as a departure whose reason is not recorded, and with timing's
// own return, not "No return has been tried yet".
func TestDockDescribesALoopOnATimingStep(t *testing.T) {
	at, resets := dockNow.Add(-90*time.Minute), dockNow.Add(30*time.Minute)
	chain := &dockChainInput{Candidates: []config.Candidate{{ID: "example-large"}}, TimingOn: true, TimingSteps: []string{"primary", "example-large"}}
	for _, c := range []struct {
		name      string
		action    string
		left      bool
		why, text string
	}{
		{"stepped up", "up", false,
			"Usage-aware timing stepped up for the 7-day window, which resets Mon 4 Mar, 12:30",
			"Usage-aware timing stepped up from here on Mon 4 Mar, to use the 7-day allowance before it resets. Timing returns here when that window resets, Mon 4 Mar, 12:30, or earlier if the readings show it running out."},
		{"stepped down", "down", false,
			"Usage-aware timing stepped down for the 7-day window, which resets Mon 4 Mar, 12:30",
			"Usage-aware timing stepped down from here on Mon 4 Mar: the 7-day window was projected to run out before its reset. Timing returns here when that window resets, Mon 4 Mar, 12:30, or earlier if the readings show it running out."},
		{"no longer a step", "up", true,
			"Usage-aware timing stepped up for the 7-day window, which resets Mon 4 Mar, 12:30; it is no longer a timing step, so the next turn boundary returns it to its first choice",
			"Usage-aware timing stepped up from here on Mon 4 Mar, to use the 7-day allowance before it resets. That route is no longer a timing step: the next turn boundary returns here."},
	} {
		ch := *chain
		if c.left {
			ch.TimingSteps = []string{"primary"}
		}
		st := &statusOut{TargetConfigured: true, Target: "example-large", State: state.File{ActiveTarget: "example-large", TargetSetBy: state.SetByTiming,
			Timing: &state.TimingRecord{LastMove: &state.TimingMove{At: at, From: state.Primary, To: "example-large", Action: c.action, Window: "seven_day", ResetsAt: resets}}}}
		a := dockAwayOf(st, &ch, nil, nil, dockNow)
		if a == nil || len(a.Departures) != 1 || a.How != dockLeftTiming || a.Choice != 2 {
			t.Fatalf("%s: %+v", c.name, a)
		}
		if got := say(a.Why, dockNow); got != c.why {
			t.Errorf("%s why:\n got  %q\n want %q", c.name, got, c.why)
		}
		if got := say(a.Departures[0].Text, dockNow); got != c.text {
			t.Errorf("%s departure:\n got  %q\n want %q", c.name, got, c.text)
		}
	}
	// A loop a handoff moved is still described from its history.
	st := &statusOut{TargetConfigured: true, Target: "example-large", State: state.File{ActiveTarget: "example-large", TargetSetBy: state.SetByHandoff,
		Timing: &state.TimingRecord{LastMove: &state.TimingMove{At: at, From: state.Primary, To: "example-large", Action: "up"}}}}
	if a := dockAwayOf(st, chain, nil, nil, dockNow); a == nil || a.How == dockLeftTiming {
		t.Fatalf("a handoff described as timing: %+v", a)
	}
}

// The history's timing causes: a timing move and a repair are timeline kinds
// of their own, a timing move is the last move, and the panel words them and
// counts a timing move as a departure from the route it left.
func TestTimingCausesInTimelineAndPanel(t *testing.T) {
	a, b := incident.TargetKey(state.Primary), incident.TargetKey("example-large")
	up := incident.Record{Kind: "tick", Action: "wake", Cause: incident.TimingStepUp, Outcome: "succeeded", TargetKey: a, TargetKeyAfter: b, LimitWaitReport: "none", TurnRetry: "none"}
	repair := incident.Record{Kind: "tick", Action: "none", Cause: incident.TimingRepair, Outcome: "succeeded", TargetKey: a, TargetKeyAfter: a, LimitWaitReport: "none", TurnRetry: "none"}
	if k := timelineKinds(up); !containsString(k, "timing_move") || containsString(k, "handoff") {
		t.Errorf("step up kinds %v", k)
	}
	if k := timelineKinds(repair); !containsString(k, "timing_repair") || containsString(k, "timing_move") {
		t.Errorf("repair kinds %v", k)
	}
	if !containsString(timelineKindNames, "timing_move") || !containsString(timelineKindNames, "timing_repair") {
		t.Error("the kinds are not filterable")
	}
	for cause, want := range map[string]string{
		incident.TimingStepUp:   "usage-aware timing stepped up",
		incident.TimingStepDown: "usage-aware timing stepped down",
		incident.TimingReturn:   "usage-aware timing returned to the first route",
		incident.TimingRepair:   "the thread was set to the route on record",
	} {
		if got := dockReason(cause); got != want {
			t.Errorf("%s: %q", cause, got)
		}
	}
	tl := &timelineReport{Events: []timelineEntry{{At: dockNow.Add(-time.Hour), Incident: &up}, {At: dockNow, Incident: &repair}}}
	moves := dockMoves(tl)
	if len(moves) != 1 || moves[0].how != dockLeftTiming || moves[0].from != a || moves[0].to != b {
		t.Fatalf("moves %+v", moves)
	}
}

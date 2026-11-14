package main

import (
	"strings"
	"testing"
	"time"
)

// An overdue loop is a panel need, which the menu's badge counts, unless
// something else about the loop already needs the person.
func TestPanelRaisesAnOverdueLoopAsANeed(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	last := now.Add(-3 * time.Hour)
	in := dockInput{Client: clientStatusReport{Outcome: "available", Snapshot: &clientSnapshot{Loop: "example-loop",
		Controls: &clientControls{Enabled: true},
		Runtime:  &clientRuntime{State: "unknown", Freshness: "stale", CheckedAt: &last, Overdue: true, OverdueMessage: "no check has run for 3h0m; checks normally run every 10m, so the loop's scheduler may have stopped"},
	}}, Status: &statusOut{Loop: "example-loop", ControlState: "enabled"}}
	_, need := projectDockLoop(in, now, 30*time.Minute, false)
	if need == nil || need.Kind != "overdue" || !strings.Contains(need.Reason, "the loop's scheduler may have stopped") {
		t.Fatalf("%+v", need)
	}
	in.Client.Snapshot.Runtime.Overdue = false
	if _, need = projectDockLoop(in, now, 30*time.Minute, false); need != nil && need.Kind == "overdue" {
		t.Fatalf("%+v", need)
	}
}

package main

import (
	"strings"
	"testing"
	"time"
)

// A loop whose last check found a lane failed was drawn as a healthy green
// "Supervising", in neither "Needs you" nor "Worth knowing", with the fact
// only in a grey line (design pass 2, new finding 1). A failed lane does not
// hold the loop (a child_error is reported, not held), so it is not a need;
// it is worth knowing, and the row, the picker and the page say it in words.
func TestDockFailedLaneIsWorthKnowing(t *testing.T) {
	on := &clientControls{Enabled: true}
	failed := dockEvidence{controls: on}.input()
	failed.Entry.ID, failed.Entry.Loop = "loop_0123456789abcdef0123456789abcde1", "example-coordinator"
	failed.Client.Snapshot.Loop = "example-coordinator"
	failed.RunsKnown, failed.Runs = true, runsLoop{Blocker: runsBlocker{Availability: blockerListed,
		Children: []runsChild{{Ref: "child:example1", Status: "error"}, {Ref: "child:example2", Status: "active"}}}}
	working := dockEvidence{controls: on, blocker: blockerListed}.input()

	r := buildDockReport([]dockInput{failed, working}, dockNow, 15*time.Second, 30*time.Minute, false, true, 0, true)
	l := dockLoopNamed(t, r, "example-coordinator")
	if l.State != "supervising" || l.LanesFailed != "1 lane failed" {
		t.Fatalf("state %q, lanesFailed %q", l.State, l.LanesFailed)
	}
	if !strings.HasSuffix(l.PickerState, " · 1 lane failed") {
		t.Errorf("picker row: %q", l.PickerState)
	}
	if !strings.Contains(l.Detail, "1 lane failed, as of its last check; the loop is not held for a failed lane") {
		t.Errorf("page detail: %q", l.Detail)
	}
	if len(r.NeedsYou) != 0 || !r.AllClear {
		t.Errorf("a failed lane is not a need: %+v", r.NeedsYou)
	}
	if len(r.Worth) != 1 || r.Worth[0].Loop != "example-coordinator" || !strings.Contains(say(r.Worth[0].Text, dockNow), "1 lane failed") {
		t.Errorf("worth knowing: %+v", r.Worth)
	}
	// A loop whose lanes are all working stays quiet and unmarked.
	if w := dockLoopNamed(t, r, "example-loop"); w.LanesFailed != "" || strings.Contains(w.PickerState, "failed") {
		t.Errorf("%+v", w)
	}

	// Only a current listing counts: lanes kept from before a hold are not
	// said as failed now, and a held loop is under "Needs you" already.
	before := failed
	before.Runs.Blocker.Availability = blockerBeforeHold
	held := dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}}.input()
	held.RunsKnown, held.Runs = true, failed.Runs
	r = buildDockReport([]dockInput{before, held}, dockNow, 15*time.Second, 30*time.Minute, false, true, 0, true)
	if l := dockLoopNamed(t, r, "example-coordinator"); l.LanesFailed != "" {
		t.Errorf("before a hold: %q", l.LanesFailed)
	}
	for _, w := range r.Worth {
		if strings.Contains(say(w.Text, dockNow), "lane failed") {
			t.Errorf("worth knowing: %+v", w)
		}
	}

	// The page draws it in words, with a dot that is not the green of a
	// loop with nothing to say.
	js := dockUIText(t, "dock.js")
	for _, want := range []string{
		`if (l.lanesFailed) status.appendChild(tag("needs", l.lanesFailed));`,
		`if (wl && wl.lanesFailed && tone === "good") tone = "warn";`,
		`pills.appendChild(tag("needs", l.lanesFailed));`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("dock.js lacks %q", want)
		}
	}
}

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The timeline shows a return to the primary as a move, whatever the check's
// outcome (it is recorded only once saved), and finds it by the fallback the
// loop was on.
func TestTimelineShowsAReturnToThePrimary(t *testing.T) {
	dir := t.TempDir()
	on := &incident.Controls{Enabled: true}
	now := time.Now().UTC()
	primary := incident.TargetKey(state.Primary)
	check := func(i int, outcome string) incident.Record {
		return incident.Record{Kind: "tick", Adapter: "bb", TargetKey: primary, TargetKeyAfter: primary, CheckStartedAt: now.Add(time.Duration(i-10) * time.Minute),
			Action: "none", Outcome: outcome, Cause: "idle", ControlBefore: on, ControlAfter: on, Observation: incident.Observation{Availability: "observed", State: "idle"},
			Probes: incident.Probes{Availability: "captured"}, Decisions: incident.Decisions{Availability: "captured"}}
	}
	appendTimelineRecord(t, dir, check(0, "succeeded")) // 1
	saved := check(1, "succeeded")
	saved.Returned = &incident.Return{FromKey: incident.TargetKey("fictional-large"), Reason: incident.ReturnDisabled}
	appendTimelineRecord(t, dir, saved) // 2
	failed := check(2, "failed")
	failed.Returned = &incident.Return{FromKey: incident.TargetKey("fictional-large"), Reason: incident.ReturnRemoved}
	appendTimelineRecord(t, dir, failed)                // 3
	appendTimelineRecord(t, dir, check(3, "succeeded")) // 4
	// A dry run sends nothing but saves its state, and so its return.
	dry := check(4, "dry_run")
	dry.DryRun, dry.Returned = true, &incident.Return{FromKey: incident.TargetKey("fictional-small"), Reason: incident.ReturnNoCreatedThread}
	appendTimelineRecord(t, dir, dry) // 5

	all := timelineJSON(t, dir)
	if all.Summary.LastTargetMove == nil || all.Summary.LastTargetMove.Sequence != 5 {
		t.Fatalf("%+v", all.Summary.LastTargetMove)
	}
	for _, kind := range []string{"return", "moves"} {
		if got := timelineSequences(timelineJSON(t, dir, "--kind", kind)); len(got) != 3 || got[0] != 2 || got[1] != 3 || got[2] != 5 {
			t.Fatalf("--kind %s: %v", kind, got)
		}
	}
	if got := timelineSequences(timelineJSON(t, dir, "--target", "fictional-large")); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("--target: %v", got)
	}
	short := "fallback " + incident.TargetKey("fictional-large")[:8]
	code, out, _ := timelineCLI(t, dir)
	for _, want := range []string{
		"RETURNED TO THE PRIMARY: the loop was on " + short + ", which is disabled, so this check ran on the primary",
		"outcome failed; cause idle; controls enabled -> enabled; target primary; RETURNED TO THE PRIMARY: the loop was on " + short + ", which is no longer configured, so this check ran on the primary",
		"which has no created thread on record, so this check ran on the primary; DRY RUN (no executed action)",
		"last recorded target move (handoff, switch-back, timing, return to the primary or retarget): incidents.json #5",
	} {
		if code != exitOK || !strings.Contains(out, want) {
			t.Fatalf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "may not have been saved") {
		t.Fatalf("a recorded return is doubted:\n%s", out)
	}
}

// The panel's recent history says the loop was returned, before anything else
// the check did, and words a manual check by its results.
func TestPanelWordsAReturnAndAManualCheck(t *testing.T) {
	primary := incident.TargetKey(state.Primary)
	names := dockRouteNames{primary: "Example primary"}
	r := incident.Record{Kind: "tick", TargetKey: primary, TargetKeyAfter: primary, Action: "none", Outcome: "succeeded", Cause: "idle",
		Returned: &incident.Return{FromKey: incident.TargetKey("fictional-large"), Reason: incident.ReturnRemoved}}
	var ev dockEvent
	dockWordIncident(&ev, r, names)
	if ev.Tone != "key" || ev.Detail != "Returned to Example primary, because the route it was on is no longer configured." {
		t.Fatalf("%+v", ev)
	}
	// Then a handoff from the primary in the same check.
	r.Action, r.TargetKeyAfter = "handoff", incident.TargetKey("fictional-small")
	names[r.TargetKeyAfter] = "Example small"
	ev = dockEvent{}
	dockWordIncident(&ev, r, names)
	if ev.Tone != "key" || ev.Detail != "Returned to Example primary, because the route it was on is no longer configured. Then to Example small." {
		t.Fatalf("%+v", ev)
	}
	// Then a wake on the primary.
	r.Action, r.TargetKeyAfter = "wake", primary
	ev = dockEvent{}
	dockWordIncident(&ev, r, names)
	if ev.Detail != "Returned to Example primary, because the route it was on is no longer configured. Delivery is not confirmed." {
		t.Fatalf("%+v", ev)
	}
	// A failed check that saved its return still moved the loop.
	r.Action, r.TargetKeyAfter, r.Outcome = "none", primary, "failed"
	ev = dockEvent{}
	dockWordIncident(&ev, r, names)
	if !strings.HasPrefix(ev.Detail, "Returned to Example primary") {
		t.Fatalf("%+v", ev)
	}
	p := incident.Record{Kind: "probe", Probes: incident.Probes{Availability: "captured", Results: []incident.ProbeResult{{OK: true}, {OK: false}, {OK: true}}}}
	ev = dockEvent{}
	dockWordIncident(&ev, p, names)
	if ev.Phrase != "Fallbacks checked by hand" || ev.Detail != "2 passed, 1 failed." || ev.Tone != "problem" {
		t.Fatalf("%+v", ev)
	}
	p.Probes.Results = p.Probes.Results[:1]
	ev = dockEvent{}
	dockWordIncident(&ev, p, names)
	if ev.Detail != "1 passed, 0 failed." || ev.Tone == "problem" {
		t.Fatalf("%+v", ev)
	}
}

// The panel folds a run of identical idle checks into one line, but never a
// return to the primary: the idle check after it stays a line of its own.
func TestPanelDoesNotFoldAReturnIntoTheNextCheck(t *testing.T) {
	dir := t.TempDir()
	on := &incident.Controls{Enabled: true}
	now := time.Now().UTC()
	primary := incident.TargetKey(state.Primary)
	check := func(i int) incident.Record {
		return incident.Record{Kind: "tick", Adapter: "bb", TargetKey: primary, TargetKeyAfter: primary, CheckStartedAt: now.Add(time.Duration(i-10) * time.Minute),
			Action: "none", Outcome: "succeeded", Cause: "idle", ControlBefore: on, ControlAfter: on, Observation: incident.Observation{Availability: "observed", State: "idle"},
			Probes: incident.Probes{Availability: "captured"}, Decisions: incident.Decisions{Availability: "captured"}}
	}
	ret := check(0)
	ret.Returned = &incident.Return{FromKey: incident.TargetKey("fictional-large"), Reason: incident.ReturnRemoved}
	appendTimelineRecord(t, dir, ret)
	appendTimelineRecord(t, dir, check(1))
	tl := readTimeline(dir, now, time.Hour, 40, timelineFilter{})
	got := dockRecentEvents(&tl, dockRouteNames{primary: "Example primary"}, now)
	if len(got.Events) != 2 || got.Events[0].Folded != 0 || got.Events[1].Folded != 0 || !strings.HasPrefix(got.Events[1].Detail, "Returned to Example primary") {
		t.Fatalf("%+v", got.Events)
	}
	// Two identical idle checks are still one line.
	appendTimelineRecord(t, dir, check(2))
	tl = readTimeline(dir, now, time.Hour, 40, timelineFilter{})
	if got := dockRecentEvents(&tl, dockRouteNames{primary: "Example primary"}, now); len(got.Events) != 2 || got.Events[0].Folded != 1 {
		t.Fatalf("%+v", got.Events)
	}
}

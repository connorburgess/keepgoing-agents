package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A manual `fallbacks check` leaves one history record of its probes, with
// hashed aliases and typed results only. A dry run probes nothing and leaves
// none.
func TestFallbacksCheckRecordsItsProbesInTheHistory(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.dir, "state")
	write(t, filepath.Join(f.dir, "bin", "codex"), "#!/bin/sh\nexit 1\n", 0o755)
	if code, _, _ := f.run("fallbacks", "check", "--dry-run"); code != exitOK {
		t.Fatalf("dry run: %d", code)
	}
	if j, err := incident.Load(dir); err != nil || len(j.Records) != 0 {
		t.Fatalf("a dry run recorded: %+v %v", j.Records, err)
	}
	if code, _, _ := f.run("fallbacks", "check"); code != exitFailed {
		t.Fatalf("check: %d", code)
	}
	j, err := incident.Load(dir)
	if err != nil || len(j.Records) != 1 {
		t.Fatalf("%+v %v", j.Records, err)
	}
	r := j.Records[0]
	if r.Kind != "probe" || r.Tier != incident.TierCondition || r.Outcome != "probes_saved" || len(r.Probes.Results) != 2 {
		t.Fatalf("%+v", r)
	}
	want := map[string]bool{incident.TargetKey("claude-other-account"): true, incident.TargetKey("codex-main"): false}
	for _, p := range r.Probes.Results {
		ok, known := want[p.TargetKey]
		if !known || p.OK != ok || p.Tier != "manual" {
			t.Fatalf("%+v", p)
		}
	}
	// An exact check of one candidate is recorded with its tier.
	if code, _, _ := f.run("fallbacks", "check", "--id", "claude-other-account", "--exact"); code != exitFailed {
		t.Fatalf("exact check: %d", code)
	}
	j, _ = incident.Load(dir)
	if r := j.Records[len(j.Records)-1]; r.Kind != "probe" || len(r.Probes.Results) != 1 || r.Probes.Results[0].Tier != "exact" || !r.Probes.Results[0].OK || r.Tier != incident.TierActivity {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
	for _, private := range []string{"claude-other-account", "codex-main", "thr_"} {
		if strings.Contains(string(b), private) {
			t.Fatalf("%s leaked into the history", private)
		}
	}
	code, out, errs := timelineCLI(t, dir, "--kind", "probe")
	if code != exitOK || errs != "" || !strings.Contains(out, "manual fallbacks check (recorded build ") || !strings.Contains(out, "manual passed") ||
		!strings.Contains(out, "manual failed (") || !strings.Contains(out, "no session was observed and nothing was sent") {
		t.Fatalf("%d %s\n%s", code, errs, out)
	}
	// A target filter finds a manual check by the candidate it probed.
	if got := timelineJSON(t, dir, "--target", "codex-main"); len(got.Events) != 1 || got.Events[0].Incident.Kind != "probe" {
		t.Fatalf("%+v", got.Events)
	}
}

// The timeline shows a saved return to the primary as a move, finds it by
// the fallback the loop was on, and does not claim one a failed check may
// not have saved.
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
		if got := timelineSequences(timelineJSON(t, dir, "--kind", kind)); len(got) != 2 || got[0] != 2 || got[1] != 5 {
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
		"RETURNED TO THE PRIMARY: the loop was on " + short + ", which is no longer configured, so this check ran on the primary (the check failed, so the return may not have been saved)",
		"which has no created thread on record, so this check ran on the primary; DRY RUN (no executed action)",
		"last recorded target move (handoff, switch-back, return to the primary or retarget): incidents.json #5",
	} {
		if code != exitOK || !strings.Contains(out, want) {
			t.Fatalf("lacks %q:\n%s", want, out)
		}
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
	// A failed check may not have saved it: nothing is claimed.
	r.Action, r.TargetKeyAfter, r.Outcome = "none", primary, "failed"
	ev = dockEvent{}
	dockWordIncident(&ev, r, names)
	if strings.Contains(ev.Detail, "Returned") {
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

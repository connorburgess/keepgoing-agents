package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// eventLogFixture retains a fictional journey: the primary waits days for a
// limit with failover disabled, another owner takes the loop, a person
// retargets to a fallback, and a later check switches back.
func eventLogFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now().UTC()
	enabled := &incident.Controls{Enabled: true}
	primary, fallback := incident.TargetKey(state.Primary), incident.TargetKey("fictional-fallback")
	captured := incident.Probes{Availability: "captured"}
	wait := incident.Record{Kind: "tick", Build: "v0.1.5", Adapter: "bb", TargetKey: primary, TargetKeyAfter: primary, Probes: captured, CheckStartedAt: now.Add(-80 * time.Hour),
		Action: "none", Outcome: "succeeded", Cause: "limit_failover_disabled", ControlBefore: enabled, ControlAfter: enabled,
		Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit", Source: "bb_rate_limits"},
		Decisions: incident.Decisions{Availability: "captured", Evaluations: []incident.Evaluation{{Stage: "normal", Action: "none", Cause: "limit_failover_disabled",
			Input: incident.DecisionInput{State: "error", ErrorClass: "rate_limit", ResetIn: 72 * time.Hour}}}}}
	appendTimelineRecord(t, dir, wait) // 1
	wait.CheckStartedAt = now.Add(-40 * time.Hour)
	wait.Decisions.Evaluations[0].Input.ResetIn = 32 * time.Hour
	appendTimelineRecord(t, dir, wait) // 2
	appendTimelineRecord(t, dir, incident.Record{Kind: "control", Build: "v0.1.5", CheckStartedAt: now.Add(-10 * time.Hour), ControlBefore: enabled, ControlAfter: enabled,
		ControlChange: &incident.ControlChange{Operation: "daemon_takeover", SaveObservedAt: now.Add(-10 * time.Hour), OwnerPresentBefore: true, OwnerPresentAfter: true, OwnershipChanged: true}}) // 3
	appendTimelineRecord(t, dir, incident.Record{Kind: "retarget", Build: "v0.1.5", CheckStartedAt: now.Add(-9 * time.Hour), TargetKey: primary, TargetKeyAfter: fallback}) // 4
	wake := incident.Record{Kind: "tick", Build: "v0.1.5", Adapter: "bb", TargetKey: fallback, TargetKeyAfter: fallback, CheckStartedAt: now.Add(-8 * time.Hour),
		Action: "wake", Outcome: "succeeded", Cause: "idle", ControlBefore: enabled, ControlAfter: enabled, Observation: incident.Observation{Availability: "observed", State: "idle"}, Decisions: incident.Decisions{Availability: "captured"}}
	wake.Probes.Add("fictional-other-candidate", "scheduled", false, "auth")
	appendTimelineRecord(t, dir, wake) // 5
	back := wake
	back.CheckStartedAt, back.Action, back.TargetKeyAfter, back.Probes = now.Add(-time.Hour), "switch_back", primary, incident.Probes{}
	back.Probes.Add(state.Primary, "exact", true, "")
	appendTimelineRecord(t, dir, back) // 6
	return dir, fallback
}

func timelineJSON(t *testing.T, dir string, args ...string) timelineReport {
	t.Helper()
	code, out, errs := timelineCLI(t, dir, append([]string{"--json"}, args...)...)
	var r timelineReport
	if code != exitOK || errs != "" || json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("timeline %v: exit %d\n%s\n%s", args, code, out, errs)
	}
	return r
}

func timelineSequences(r timelineReport) []uint64 {
	seqs := []uint64{}
	for _, e := range r.Events {
		seqs = append(seqs, e.Sequence)
	}
	return seqs
}

func TestTimelineFiltersByKindTargetAndTime(t *testing.T) {
	dir, fallback := eventLogFixture(t)
	before := timelineFiles(t, dir)
	all := timelineJSON(t, dir)
	if all.SchemaVersion != 8 || all.Filter.ReachesPrunedHistory || len(all.Events) != 6 || all.Filter.Matched != 6 || all.Filter.Excluded != 0 || len(all.Filter.Kinds) != 0 || all.Filter.TargetKey != "" {
		t.Fatalf("%+v", all.Filter)
	}
	wantKinds := [][]string{{"check", "limit_wait"}, {"check", "limit_wait"}, {"control", "ownership"}, {"retarget"}, {"check", "wake", "probe"}, {"check", "switch_back", "probe"}}
	for i, e := range all.Events {
		if !reflect.DeepEqual(e.Kinds, wantKinds[i]) {
			t.Fatalf("event %d kinds %v, want %v", i+1, e.Kinds, wantKinds[i])
		}
	}
	// Filters never narrow the summaries.
	for _, tc := range []struct {
		args []string
		want []uint64
	}{
		{[]string{"--kind", "moves"}, []uint64{3, 4, 6}},
		{[]string{"--kind", "retarget, switch_back"}, []uint64{4, 6}},
		{[]string{"--kind", "limit_wait", "--since", "100h", "--until", "50h"}, []uint64{1}},
		{[]string{"--since", time.Now().UTC().Add(-9*time.Hour - time.Minute).Format(time.RFC3339)}, []uint64{4, 5, 6}},
		{[]string{"--target", "fictional-fallback"}, []uint64{4, 5, 6}},
		{[]string{"--target", fallback}, []uint64{4, 5, 6}},
		{[]string{"--target", "primary", "--kind", "probe"}, []uint64{6}},
		{[]string{"--target", "fictional-other-candidate"}, []uint64{5}},
		{[]string{"--target", "fictional-unrecorded-target"}, []uint64{}},
		{[]string{"--kind", "notification"}, []uint64{}},
		{[]string{"--kind", "check", "--limit", "1"}, []uint64{6}},
	} {
		got := timelineJSON(t, dir, tc.args...)
		if !reflect.DeepEqual(timelineSequences(got), tc.want) {
			t.Fatalf("%v: %v, want %v", tc.args, timelineSequences(got), tc.want)
		}
		if got.Filter.Matched+got.Filter.Excluded != 6 || got.Filter.Matched != len(got.Events)+got.Omitted {
			t.Fatalf("%v: %+v omitted %d", tc.args, got.Filter, got.Omitted)
		}
		s := got.Summary
		if s.RetainedChecks != 4 || s.LimitWaitChecks != 2 || s.LastLimitWait.Sequence != 2 || s.LastTargetMove.Sequence != 6 || s.LastSendAction.Sequence != 6 {
			t.Fatalf("%v narrowed the summary: %+v", tc.args, s)
		}
	}
	moves := timelineJSON(t, dir, "--kind", "moves")
	if !reflect.DeepEqual(moves.Filter.Kinds, timelineMoveKinds) || moves.Filter.Excluded != 3 {
		t.Fatalf("%+v", moves.Filter)
	}
	// The typed alias is hashed before it is reported or printed.
	code, out, errs := timelineCLI(t, dir, "--json", "--target", "fictional-fallback")
	if code != exitOK || strings.Contains(out+errs, "fictional-fallback") || !strings.Contains(out, `"targetKey": "`+fallback+`"`) {
		t.Fatalf("alias echoed or key missing: %d %s", code, errs)
	}
	code, out, errs = timelineCLI(t, dir, "--target", "fictional-fallback", "--kind", "moves", "--since", "24h")
	short := "fallback " + fallback[:8]
	for _, want := range []string{
		"filter: kinds handoff,switch_back,retarget,ownership; target " + short + "; since ", "matched 2 of 6 retained events",
		"manual retarget saved: target primary -> " + short, "retarget_save_observed_at", "action switch_back", "target " + short + " -> primary", "probes: primary exact passed",
		"last recorded target move (handoff, switch-back or retarget): incidents.json #6", "checks waiting for a provider limit: 2", "target_moves: recorded",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text view lacks %q:\n%s", want, out)
		}
	}
	if code != exitOK || errs != "" || strings.Contains(out, "fictional-fallback") || strings.Contains(out, "daemon_takeover") {
		t.Fatalf("%d %s\n%s", code, errs, out)
	}
	_, out, _ = timelineCLI(t, dir)
	for _, want := range []string{
		"control daemon_takeover saved: enabled -> enabled", "loop owner changed (identity withheld)",
		"waiting for a provider limit: recorded reset in 72h0m0s, failover eligible false, fallback available false; no prompt is sent while waiting",
		"probes: fallback " + incident.TargetKey("fictional-other-candidate")[:8] + " scheduled failed (auth)", "check,limit_wait]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text view lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nfilter:") || strings.Contains(out, "The latest recorded check was waiting") {
		t.Fatalf("unfiltered view reports a filter or a stale wait:\n%s", out)
	}
	if !reflect.DeepEqual(before, timelineFiles(t, dir)) {
		t.Fatal("timeline changed runtime files")
	}
}

func TestTimelineReportsAnOngoingLimitWaitAndLegacyTargets(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	enabled := &incident.Controls{Enabled: true}
	r := incident.Record{Kind: "tick", Adapter: "bb", TargetKey: incident.TargetKey(state.Primary), CheckStartedAt: now.Add(-time.Minute), Action: "none", Outcome: "succeeded", Cause: "limit_no_fallback", ControlBefore: enabled, ControlAfter: enabled,
		Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"}}
	// A record without a post-check target stands in for pre-schema-5 history.
	appendTimelineRecord(t, dir, r)
	got := timelineJSON(t, dir)
	if got.Summary.LimitWaitChecks != 1 || got.Summary.LastTargetMove != nil || !strings.Contains(strings.Join(got.NextSteps, "\n"), "waiting for a provider limit to reset") {
		t.Fatalf("%+v %v", got.Summary, got.NextSteps)
	}
	if e := got.Events[0]; strings.Contains(e.Description, "; target ") || strings.Contains(e.Description, "probes:") || strings.Contains(e.Description, "recorded reset") {
		t.Fatalf("invented target, probe or reset evidence: %s", e.Description)
	}
	for _, l := range got.Layers {
		if l.Layer == "target_moves" && l.Availability != "not_recorded" {
			t.Fatalf("%+v", l)
		}
	}
	// A dry run that proposed a handoff is neither a move nor a send.
	r.DryRun, r.Outcome, r.Action, r.Cause, r.TargetKeyAfter = true, "dry_run", "handoff", "other", r.TargetKey
	appendTimelineRecord(t, dir, r)
	got = timelineJSON(t, dir, "--kind", "dry_run")
	if len(got.Events) != 1 || !reflect.DeepEqual(got.Events[0].Kinds, []string{"check", "dry_run"}) || got.Summary.LastTargetMove != nil {
		t.Fatalf("%+v", got.Events)
	}
	if moves := timelineJSON(t, dir, "--kind", "moves"); len(moves.Events) != 0 {
		t.Fatalf("%+v", moves.Events)
	}
	// A dry run that would have waited is the latest check, but it is not a
	// wait: the count, the summary and the next step all agree.
	r.Action, r.Cause = "none", "limit_no_fallback"
	appendTimelineRecord(t, dir, r)
	got = timelineJSON(t, dir)
	if got.Summary.LatestCheck.Sequence != 3 || got.Summary.LimitWaitChecks != 1 || got.Summary.LastLimitWait.Sequence != 1 || strings.Contains(strings.Join(got.NextSteps, "\n"), "waiting for a provider limit to reset") {
		t.Fatalf("%+v %v", got.Summary, got.NextSteps)
	}
}

func TestTimelineDoesNotCountAFailedHandoffAsAMove(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	enabled := &incident.Controls{Enabled: true}
	primary, fallback := incident.TargetKey(state.Primary), incident.TargetKey("fictional-fallback")
	r := incident.Record{Kind: "tick", Adapter: "bb", TargetKey: primary, TargetKeyAfter: fallback, Probes: incident.Probes{Availability: "captured"}, CheckStartedAt: now.Add(-5 * time.Minute),
		Action: "handoff", Outcome: "succeeded", Cause: "other", ControlBefore: enabled, ControlAfter: enabled, Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"}}
	appendTimelineRecord(t, dir, r) // 1: a real move
	// 2: the handoff errored and the target stayed where it was.
	r.CheckStartedAt, r.Outcome, r.TargetKeyAfter = now.Add(-4*time.Minute), "failed", primary
	appendTimelineRecord(t, dir, r)
	// 3: the target changed even though a later step of the check failed.
	r.CheckStartedAt, r.TargetKeyAfter = now.Add(-3*time.Minute), fallback
	appendTimelineRecord(t, dir, r)
	// 4 and 5: pre-schema-5 records have no second target; only a succeeded outcome counts.
	r.CheckStartedAt, r.TargetKeyAfter, r.Probes, r.Action = now.Add(-2*time.Minute), "", incident.Probes{}, "switch_back"
	appendTimelineRecord(t, dir, r)
	r.CheckStartedAt, r.Outcome = now.Add(-time.Minute), "succeeded"
	appendTimelineRecord(t, dir, r)
	// 6: a later failed handoff must not replace the last real move.
	r.CheckStartedAt, r.Outcome, r.TargetKeyAfter, r.Action = now, "failed", primary, "handoff"
	appendTimelineRecord(t, dir, r)
	got := timelineJSON(t, dir, "--kind", "moves")
	if !reflect.DeepEqual(timelineSequences(got), []uint64{1, 3, 5}) || got.Summary.LastTargetMove.Sequence != 5 {
		t.Fatalf("%v %+v", timelineSequences(got), got.Summary.LastTargetMove)
	}
	all := timelineJSON(t, dir)
	for _, i := range []int{1, 3, 5} {
		if !reflect.DeepEqual(all.Events[i].Kinds, []string{"check"}) {
			t.Fatalf("event %d: %v", i+1, all.Events[i].Kinds)
		}
	}
	if d := all.Events[1].Description; !strings.Contains(d, "action handoff; outcome failed") || !strings.Contains(d, "the target did not change, so this is not a recorded move") {
		t.Fatal(d)
	}
	if strings.Contains(all.Events[0].Description, "not a recorded move") || strings.Contains(all.Events[3].Description, "not a recorded move") {
		t.Fatal("a move or a legacy record was described as unchanged")
	}
}

func TestTimelineFilterArgumentsAreRefusedWithoutEcho(t *testing.T) {
	dir, _ := eventLogFixture(t)
	for _, args := range [][]string{
		{"--kind", "PRIVATE_FILTER_MARKER"}, {"--kind", "check,"}, {"--kind", ","}, {"--since", "PRIVATE_FILTER_MARKER"}, {"--until", "-1h"}, {"--since", "0s"},
		{"--since", "1h", "--until", "2h"}, {"--since", "2031-01-01T00:00:00Z", "--until", "2030-01-01T00:00:00Z"},
	} {
		code, out, errs := timelineCLI(t, dir, args...)
		if code != exitUsage || out != "" || strings.Contains(errs, "PRIVATE_FILTER_MARKER") || !strings.Contains(errs, "--kind takes") {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
	}
	code, out, _ := timelineCLI(t, dir, "--help")
	if code != exitOK || !strings.Contains(out, "moves selects") || !strings.Contains(out, "-since") {
		t.Fatalf("%d %s", code, out)
	}
}

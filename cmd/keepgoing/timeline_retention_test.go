package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// retentionFixture is a fictional loop with more history than fits: a
// retarget, a 300-check limit wait, a pause with 50 held checks, then 200
// wakes interleaved with routine checks that cannot collapse.
func retentionFixture(t *testing.T) (string, time.Time) {
	t.Helper()
	dir := t.TempDir()
	start := time.Now().UTC().Add(-200 * time.Hour).Truncate(time.Second)
	on, paused := &incident.Controls{Enabled: true}, &incident.Controls{Enabled: true, Paused: true}
	primary := incident.TargetKey(state.Primary)
	n := 0
	tick := func(change func(*incident.Record)) {
		r := incident.Record{Kind: "tick", Build: "v0.1.5", Adapter: "bb", TargetKey: primary, TargetKeyAfter: primary, CheckStartedAt: start.Add(time.Duration(n) * 10 * time.Minute),
			Action: "none", Outcome: "succeeded", Cause: "active", ControlBefore: on, ControlAfter: on, Observation: incident.Observation{Availability: "observed", State: "active"},
			Decisions: incident.Decisions{Availability: "captured"}, Probes: incident.Probes{Availability: "captured"}}
		n++
		change(&r)
		appendTimelineRecord(t, dir, r)
	}
	appendTimelineRecord(t, dir, incident.Record{Kind: "retarget", Build: "v0.1.5", CheckStartedAt: start.Add(-time.Minute), TargetKey: incident.TargetKey("fictional-fallback"), TargetKeyAfter: primary}) // 1
	for i := 0; i < 300; i++ {                                                                                                                                                                              // 2-301
		tick(func(r *incident.Record) {
			r.Cause, r.Observation = "limit_failover_disabled", incident.Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"}
			r.Decisions.Evaluations = []incident.Evaluation{{Stage: "normal", Action: "none", Cause: "limit_failover_disabled", Input: incident.DecisionInput{ResetIn: time.Duration(300-i) * 10 * time.Minute}}}
		})
	}
	at := start.Add(time.Duration(n) * 10 * time.Minute)
	appendTimelineRecord(t, dir, incident.Record{Kind: "control", Build: "v0.1.5", CheckStartedAt: at, ControlBefore: on, ControlAfter: paused, ControlChange: &incident.ControlChange{Operation: "pause", SaveObservedAt: at}}) // 302
	for i := 0; i < 50; i++ {                                                                                                                                                                                                    // 303-352
		tick(func(r *incident.Record) {
			r.ControlBefore, r.ControlAfter, r.Cause, r.Observation = paused, paused, "paused", incident.Observation{Availability: "not_observed"}
		})
	}
	at = start.Add(time.Duration(n) * 10 * time.Minute)
	appendTimelineRecord(t, dir, incident.Record{Kind: "control", Build: "v0.1.5", CheckStartedAt: at, ControlBefore: paused, ControlAfter: on, ControlChange: &incident.ControlChange{Operation: "resume", SaveObservedAt: at}}) // 353
	for i := 0; i < 400; i++ {                                                                                                                                                                                                    // 354-753
		tick(func(r *incident.Record) {
			switch i % 4 {
			case 0:
				r.Action, r.Cause, r.Observation.State = "wake", "idle", "idle"
			case 2:
				r.Observation.Queued = 1
			}
		})
	}
	return dir, start
}

func TestTimelineShowsCollapsedAndPrunedHistory(t *testing.T) {
	dir, start := retentionFixture(t)
	before := timelineFiles(t, dir)
	got := timelineJSON(t, dir, "--limit", "320")
	ret := got.Retention
	if ret.MaxRecords != 256 || ret.MaxBytes != 512*1024 || len(ret.Tiers) != 4 || ret.UntieredPruned != 0 {
		t.Fatalf("%+v", ret)
	}
	// Key: the retarget, the pause and the resume. Condition: both ends of the
	// limit wait. Activity: all 100 wakes, borrowing spare room. Routine: both
	// ends of the held run plus 300 checks that cannot collapse, pruned down
	// to what is left of the 256.
	key, condition, activity, routine := ret.Tiers[0], ret.Tiers[1], ret.Tiers[2], ret.Tiers[3]
	if key != (timelineTier{Tier: "key", Reserved: 64, Retained: 3}) || condition != (timelineTier{Tier: "condition", Reserved: 48, Retained: 2}) || activity != (timelineTier{Tier: "activity", Reserved: 80, Retained: 100}) {
		t.Fatalf("%+v %+v %+v", key, condition, activity)
	}
	if routine.Reserved != 64 || routine.Retained != 151 || routine.Pruned != 151 || routine.PrunedThrough == nil {
		t.Fatalf("%+v", routine)
	}
	if ret.CollapsedChecks != 298 || got.Sources[0].Dropped != 151 || got.Sources[0].Retained != 256 {
		t.Fatalf("%+v %+v", ret, got.Sources[0])
	}
	// The held run's 48 collapsed checks went with its pruned routine records.
	if ret.CollapsedPruned != 48 {
		t.Fatalf("%+v", ret)
	}
	// The whole wait is still counted and explained from its two ends.
	s := got.Summary
	if s.LimitWaitChecks != 300 || s.LastLimitWait.Sequence != 301 || s.LastTargetMove.Sequence != 1 || s.LastHoldEntry.Sequence != 302 {
		t.Fatalf("%+v", s)
	}
	wait := timelineJSON(t, dir, "--kind", "limit_wait")
	if !reflect.DeepEqual(timelineSequences(wait), []uint64{2, 301}) || !reflect.DeepEqual(wait.Events[1].Kinds, []string{"check", "limit_wait", "collapsed"}) {
		t.Fatalf("%v %v", timelineSequences(wait), wait.Events)
	}
	c := wait.Events[1].Incident.Collapsed
	if c == nil || c.Count != 298 || !c.FirstAt.Equal(start.Add(10*time.Minute)) || !c.LastAt.Equal(start.Add(298*10*time.Minute)) || wait.Events[0].Incident.Collapsed != nil {
		t.Fatalf("%+v", c)
	}
	for _, want := range []string{"COLLAPSED: 298 identical earlier checks from " + start.Add(10*time.Minute).Format(time.RFC3339) + " to " + start.Add(2980*time.Minute).Format(time.RFC3339) + " are summarized here", "their decision inputs are not retained", "recorded reset in 10m0s"} {
		if !strings.Contains(wait.Events[1].Description, want) {
			t.Fatalf("description lacks %q: %s", want, wait.Events[1].Description)
		}
	}
	if !strings.Contains(wait.Events[0].Description, "recorded reset in 50h0m0s") || strings.Contains(wait.Events[0].Description, "COLLAPSED") {
		t.Fatal(wait.Events[0].Description)
	}
	if only := timelineJSON(t, dir, "--kind", "collapsed"); !reflect.DeepEqual(timelineSequences(only), []uint64{301}) {
		t.Fatalf("%v", timelineSequences(only))
	}
	// A range that falls inside a collapsed span still shows its summary:
	// the checks in that range ran, even though their records are folded.
	mid := start.Add(150 * 10 * time.Minute)
	inside := timelineJSON(t, dir, "--since", mid.Add(-time.Hour).Format(time.RFC3339), "--until", mid.Format(time.RFC3339))
	if !reflect.DeepEqual(timelineSequences(inside), []uint64{301}) {
		t.Fatalf("%v", timelineSequences(inside))
	}
	// A range before the summary's span, or after its record, does not match it.
	if early := timelineJSON(t, dir, "--until", start.Add(5*time.Minute).Format(time.RFC3339)); !reflect.DeepEqual(timelineSequences(early), []uint64{1, 2}) {
		t.Fatalf("%v", timelineSequences(early))
	}
	// Pruned history is flagged for any range that reaches it, and only then.
	horizon := *routine.PrunedThrough
	for _, tc := range []struct {
		args    []string
		reaches bool
	}{
		{nil, true},
		{[]string{"--kind", "moves"}, true},
		{[]string{"--since", horizon.Add(-time.Hour).Format(time.RFC3339)}, true},
		{[]string{"--since", horizon.Format(time.RFC3339)}, true},
		{[]string{"--since", horizon.Add(time.Second).Format(time.RFC3339)}, false},
		{[]string{"--since", "1h"}, false},
	} {
		if v := timelineJSON(t, dir, tc.args...); v.Filter.ReachesPrunedHistory != tc.reaches {
			t.Fatalf("%v: reachesPrunedHistory %t", tc.args, v.Filter.ReachesPrunedHistory)
		}
	}
	code, out, errs := timelineCLI(t, dir, "--kind", "limit_wait,hold")
	for _, want := range []string{
		"retention incidents.json (at most 256 records and 512 KiB): key 3 (reserved 64), pruned 0; condition 2 (reserved 48), pruned 0; activity 100 (reserved 80), pruned 0; routine 151 (reserved 64), pruned 151 through " + horizon.Format(time.RFC3339) + "; 298 identical checks collapsed into retained summaries",
		"(plus 298 collapsed into them)", "48 more were collapsed into records since pruned", "checks waiting for a provider limit: 300", "check,limit_wait,collapsed]", "COLLAPSED: 298 identical earlier checks",
		"NOTE: the selected range reaches history that retention pruned (see retention above); no event there does not mean nothing happened",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text view lacks %q:\n%s", want, out)
		}
	}
	if code != exitOK || errs != "" {
		t.Fatalf("%d %s", code, errs)
	}
	if _, out, _ = timelineCLI(t, dir, "--since", "1h"); strings.Contains(out, "NOTE: the selected range") {
		t.Fatal("recent range was flagged as reaching pruned history")
	}
	if !reflect.DeepEqual(before, timelineFiles(t, dir)) {
		t.Fatal("timeline changed runtime files")
	}
}

func TestTimelineCountsCollapsedHeldChecksAndLegacyDrops(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	held := &incident.Controls{Enabled: true, AwaitingUser: true}
	for i := 0; i < 10; i++ {
		appendTimelineRecord(t, dir, incident.Record{Kind: "tick", Adapter: "bb", CheckStartedAt: now.Add(time.Duration(i-20) * time.Minute), Action: "none", Outcome: "succeeded", Cause: "awaiting_user", ControlBefore: held, ControlAfter: held, Observation: incident.Observation{Availability: "not_observed"}})
	}
	got := timelineJSON(t, dir)
	if got.Summary.RetainedChecks != 2 || got.Summary.HeldChecks != 10 || got.Retention.CollapsedChecks != 8 || got.Filter.ReachesPrunedHistory || len(got.Events) != 2 {
		t.Fatalf("%+v %+v", got.Summary, got.Retention)
	}
	// An empty or unreadable journal reports its retention honestly.
	empty := timelineJSON(t, t.TempDir())
	if len(empty.Retention.Tiers) != 4 || empty.Retention.CollapsedChecks != 0 || empty.Filter.ReachesPrunedHistory {
		t.Fatalf("%+v", empty.Retention)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, incident.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := timelineCLI(t, broken)
	if code != exitFailed || !strings.Contains(out, "retention incidents.json: unavailable") {
		t.Fatalf("%d %s", code, out)
	}
	// Records dropped by an older build have no tier or time: any range may reach them.
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, incident.FileName), []byte(`{"schemaVersion":5,"nextSequence":4,"dropped":3,"records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := timelineJSON(t, legacy, "--since", "1h")
	if old.Retention.UntieredPruned != 3 || !old.Filter.ReachesPrunedHistory || old.Sources[0].SchemaVersion != 5 {
		t.Fatalf("%+v %+v", old.Retention, old.Filter)
	}
	if _, out, _ = timelineCLI(t, legacy); !strings.Contains(out, "3 records were dropped by an older build with no tier or time recorded") {
		t.Fatal(out)
	}
}

func TestTimelineMatchesACollapsedSpanAcrossAClockRollback(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	on := &incident.Controls{Enabled: true}
	// The host clock moved backwards between checks: the latest record is not the latest time.
	for _, minute := range []int{0, 50, 10, 40, 20, 30} {
		appendTimelineRecord(t, dir, incident.Record{Kind: "tick", Adapter: "bb", CheckStartedAt: base.Add(time.Duration(minute) * time.Minute), Action: "none", Outcome: "succeeded", Cause: "active", ControlBefore: on, ControlAfter: on, Observation: incident.Observation{Availability: "observed", State: "active"}})
	}
	all := timelineJSON(t, dir)
	var summary *incident.Record
	for _, e := range all.Events {
		if e.Incident.Collapsed != nil {
			summary = e.Incident
		}
	}
	if len(all.Events) != 2 || summary == nil || !summary.CheckStartedAt.Equal(base.Add(30*time.Minute)) || !summary.Collapsed.FirstAt.Equal(base.Add(10*time.Minute)) || !summary.Collapsed.LastAt.Equal(base.Add(50*time.Minute)) {
		t.Fatalf("%+v", summary)
	}
	at := func(minute int) string { return base.Add(time.Duration(minute) * time.Minute).Format(time.RFC3339) }
	for _, tc := range []struct {
		since, until int
		want         []uint64
	}{
		{45, 55, []uint64{6}}, // after the record's own time, inside its folded span
		{5, 15, []uint64{6}},  // before the record's own time, inside its folded span
		{51, 60, []uint64{}},
		{1, 9, []uint64{}},
		{0, 0, []uint64{1}},
	} {
		got := timelineJSON(t, dir, "--since", at(tc.since), "--until", at(tc.until))
		if !reflect.DeepEqual(timelineSequences(got), tc.want) {
			t.Fatalf("%d-%d: %v, want %v", tc.since, tc.until, timelineSequences(got), tc.want)
		}
	}
}

// Weeks of failed switch-back probes after a handoff leave the handoff as the
// last recorded target move.
func TestTimelineKeepsTheLastMoveUnderProbeNoise(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().UTC().Add(-400 * time.Hour).Truncate(time.Second)
	on := &incident.Controls{Enabled: true}
	primary, fallback := incident.TargetKey(state.Primary), incident.TargetKey("fictional-fallback")
	appendTimelineRecord(t, dir, incident.Record{Kind: "tick", Adapter: "bb", TargetKey: primary, TargetKeyAfter: fallback, CheckStartedAt: start, Action: "handoff", Outcome: "succeeded", Cause: "other", ControlBefore: on, ControlAfter: on,
		Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"}, Probes: incident.Probes{Availability: "captured"}})
	for i := 1; i <= 400; i++ {
		r := incident.Record{Kind: "tick", Adapter: "bb", TargetKey: fallback, TargetKeyAfter: fallback, CheckStartedAt: start.Add(time.Duration(i) * 30 * time.Minute), Action: "wake", Outcome: "succeeded", Cause: "idle", ControlBefore: on, ControlAfter: on,
			Observation: incident.Observation{Availability: "observed", State: "idle"}}
		r.Probes.Add(state.Primary, "exact", false, "rate_limit")
		appendTimelineRecord(t, dir, r)
	}
	got := timelineJSON(t, dir, "--kind", "moves")
	if got.Summary.LastTargetMove == nil || got.Summary.LastTargetMove.Sequence != 1 || !reflect.DeepEqual(timelineSequences(got), []uint64{1}) {
		t.Fatalf("%+v %v", got.Summary.LastTargetMove, timelineSequences(got))
	}
	condition := got.Retention.Tiers[1]
	if condition.Tier != "condition" || condition.Pruned == 0 || got.Retention.Tiers[0].Pruned != 0 || !got.Filter.ReachesPrunedHistory {
		t.Fatalf("%+v", got.Retention)
	}
}

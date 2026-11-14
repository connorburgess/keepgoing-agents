package incident

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func returnTick(n int, outcome string) Record {
	r := retentionTick(n)
	r.Outcome = outcome
	r.DryRun = outcome == "dry_run"
	r.Returned = &Return{FromKey: TargetKey("fictional-fallback"), Reason: ReturnRemoved}
	return r
}

// A saved return to the primary is a move: it is kept in the key tier, and a
// run of identical checks around it never folds it away.
func TestAReturnToThePrimaryIsAKeyEventAndIsNeverFolded(t *testing.T) {
	for _, outcome := range []string{"succeeded", "dry_run"} {
		if tier := clean(returnTick(0, outcome)).Tier; tier != TierKey {
			t.Fatalf("%s: tier %s", outcome, tier)
		}
	}
	// A failed check may not have saved the return.
	if tier := clean(returnTick(0, "failed")).Tier; tier == TierKey {
		t.Fatal("a failed check's return is a key event")
	}
	m := newMemJournal()
	m.add(t, retentionTick(0)) // 1
	m.add(t, retentionTick(1)) // 2
	m.add(t, returnTick(2, "succeeded"))
	m.add(t, retentionTick(3)) // 4
	m.add(t, retentionTick(4)) // 5
	j := m.flush(t)
	if !hasSequence(j, 3) {
		t.Fatalf("the return was folded: %+v", j.Records)
	}
	for _, r := range j.Records {
		if r.Sequence == 3 && (r.Returned == nil || r.Collapsed != nil || r.Tier != TierKey) {
			t.Fatalf("%+v", r)
		}
		if r.Sequence != 3 && r.Returned != nil {
			t.Fatalf("a return was attached to another check: %+v", r)
		}
	}
	// Two identical returns are two moves, not one folded run.
	if repeatable(returnTick(5, "dry_run")) {
		t.Fatal("a return is repeatable")
	}
}

// Nothing but the hashed alias and a fixed reason crosses the storage
// boundary, and only a check can carry a return.
func TestReturnIsCleanedToItsVocabulary(t *testing.T) {
	r := returnTick(0, "succeeded")
	r.Returned = &Return{FromKey: "fictional-fallback", Reason: "PRIVATE reason text"}
	got := clean(r).Returned
	if got == nil || got.FromKey != "unavailable" || got.Reason != "unavailable" {
		t.Fatalf("%+v", got)
	}
	for _, reason := range []string{ReturnRemoved, ReturnDisabled, ReturnNoCreatedThread} {
		r.Returned = &Return{FromKey: TargetKey("fictional-fallback"), Reason: reason}
		if got := clean(r).Returned; got.Reason != reason || got.FromKey != TargetKey("fictional-fallback") {
			t.Fatalf("%+v", got)
		}
	}
	for _, kind := range []string{"control", "retarget", "probe", "unavailable"} {
		r := returnTick(0, "succeeded")
		r.Kind = kind
		if got := clean(r).Returned; got != nil {
			t.Fatalf("%s kept a return: %+v", kind, got)
		}
	}
	// Cleaning copies: the caller's record is not changed.
	r.Returned = &Return{FromKey: "x", Reason: "y"}
	clean(r)
	if r.Returned.FromKey != "x" || r.Returned.Reason != "y" {
		t.Fatalf("%+v", r.Returned)
	}
}

func manualProbe(results ...ProbeResult) Record {
	return Record{Kind: "probe", CheckStartedAt: retentionStart, Build: "v0.1.5", Outcome: "probes_saved",
		Probes: Probes{Availability: "captured", Results: results}}
}

// A manual `fallbacks check` is recorded as its own kind with only its probe
// results. A failure is a condition; a check that passed is activity.
func TestManualProbeRecords(t *testing.T) {
	pass := ProbeResult{TargetKey: TargetKey("fictional-a"), Tier: "manual", OK: true, ErrorClass: "none"}
	fail := ProbeResult{TargetKey: TargetKey("fictional-b"), Tier: "exact", ErrorClass: "rate_limit"}
	dir := t.TempDir()
	r := manualProbe(pass, fail)
	// Fields a probe record cannot have are dropped at the boundary.
	r.Action, r.Cause, r.Adapter, r.TargetKey, r.Observation = "handoff", "limit_wait", "bb", TargetKey("primary"), Observation{Availability: "observed", State: "error"}
	r.LimitWaitReport, r.Lanes, r.Returned = "requested", LaneWork{Retried: 1}, &Return{Reason: ReturnRemoved}
	mustAppend(t, dir, r)
	mustAppend(t, dir, manualProbe(pass))
	j := loadAccounted(t, dir)
	if len(j.Records) != 2 {
		t.Fatalf("%+v", j.Records)
	}
	got := j.Records[0]
	if got.Kind != "probe" || got.Tier != TierCondition || got.Action != "none" || got.Cause != "other" || got.Outcome != "probes_saved" ||
		got.Adapter != "unavailable" || got.TargetKey != "unavailable" || got.Observation.Availability != "not_observed" ||
		got.LimitWaitReport != "none" || got.Lanes != (LaneWork{}) || got.Returned != nil || got.Collapsed != nil {
		t.Fatalf("%+v", got)
	}
	if len(got.Probes.Results) != 2 || got.Probes.Results[0] != pass || got.Probes.Results[1] != fail {
		t.Fatalf("%+v", got.Probes)
	}
	if j.Records[1].Tier != TierActivity {
		t.Fatalf("a passing manual check: %+v", j.Records[1])
	}
	// A record of no probes is not a manual check.
	if err := Append(dir, manualProbe()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty probe record appended: %v", err)
	}
	empty := manualProbe(pass)
	empty.Probes.Availability = "unavailable"
	if err := Append(dir, empty); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uncaptured probe record appended: %v", err)
	}
}

// Schema 11 adds the two; a schema 10 journal has neither, and one that
// claims either is not trusted for the new one.
func TestSchemaElevenAddsReturnsAndManualProbes(t *testing.T) {
	dir := t.TempDir()
	mustAppend(t, dir, returnTick(0, "succeeded"))
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil || !strings.Contains(string(b), `"schemaVersion":11`) {
		t.Fatalf("%s %v", b, err)
	}
	// Written as schema 10, a return is dropped on load, as the older writer
	// never recorded one.
	old := strings.Replace(string(b), `"schemaVersion":11`, `"schemaVersion":10`, 1)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	j := loadAccounted(t, dir)
	if len(j.Records) != 1 || j.Records[0].Returned != nil {
		t.Fatalf("%+v", j.Records)
	}
	// A schema 10 journal holding a manual probe record is unavailable.
	dir = t.TempDir()
	mustAppend(t, dir, manualProbe(ProbeResult{TargetKey: TargetKey("fictional-a"), Tier: "manual", OK: true}))
	b, _ = os.ReadFile(filepath.Join(dir, FileName))
	old = strings.Replace(string(b), `"schemaVersion":11`, `"schemaVersion":10`, 1)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("schema 10 probe record loaded: %v", err)
	}
}

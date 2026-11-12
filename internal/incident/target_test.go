package incident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestRetargetRecordKeepsOnlyHashedTargets(t *testing.T) {
	dir := t.TempDir()
	r := sample()
	r.Kind = "retarget"
	r.TargetKeyAfter = TargetKey("fictional-fallback")
	r.Action, r.Cause, r.Outcome = "handoff", "PRIVATE_RETARGET_NOTE", "succeeded"
	r.Nudges, r.CountersAvailable = 9, true
	r.Probes.Add("fictional-fallback", "exact", true, "")
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
	j, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := j.Records[0]
	if got.Kind != "retarget" || got.TargetKey != TargetKey("primary") || got.TargetKeyAfter != TargetKey("fictional-fallback") || got.Action != "none" || got.Outcome != "target_saved" || got.Cause != "other" ||
		got.Adapter != "unavailable" || got.Observation.Availability != "not_observed" || got.Probes.Availability != "unavailable" || got.Decisions.Availability != "unavailable" || got.Nudges != 0 || got.ControlBefore != nil {
		t.Fatalf("%+v", got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(b), "PRIVATE_RETARGET_NOTE") || strings.Contains(string(b), "fictional-fallback") {
		t.Fatal("note or alias leaked")
	}
	// A retarget without both hashed targets is not a recorded move.
	for _, bad := range []func(*Record){func(r *Record) { r.TargetKeyAfter = "" }, func(r *Record) { r.TargetKey = "fictional-fallback" }, func(r *Record) { r.TargetKeyAfter = r.TargetKey }} {
		r := sample()
		r.Kind, r.TargetKeyAfter = "retarget", TargetKey("fictional-fallback")
		bad(&r)
		if Append(dir, r) != ErrUnavailable {
			t.Fatal("accepted incomplete retarget")
		}
	}
	if j, err = Load(dir); err != nil || len(j.Records) != 1 {
		t.Fatal(j, err)
	}
}

func TestProbeResultsAreBoundedTypedAndHashed(t *testing.T) {
	var p Probes
	for i := 0; i < MaxProbes+2; i++ {
		p.Add("fictional-candidate", "exact", i%2 == 0, "PRIVATE_PROBE_CLASS")
	}
	p.Add("fictional-candidate", "PRIVATE_TIER", false, "rate_limit")
	if p.Availability != "captured" || len(p.Results) != MaxProbes || p.Dropped != 3 {
		t.Fatalf("%+v", p)
	}
	last, passed := p.Results[MaxProbes-1], p.Results[MaxProbes-3]
	if last.Tier != "unavailable" || last.ErrorClass != "rate_limit" || last.OK || last.TargetKey != TargetKey("fictional-candidate") {
		t.Fatalf("%+v", last)
	}
	if !passed.OK || passed.ErrorClass != "none" || p.Results[MaxProbes-2].ErrorClass != "unavailable" {
		t.Fatalf("%+v", p.Results)
	}
	r := sample()
	r.Probes, r.TargetKeyAfter = p, "fictional-candidate"
	dir := t.TempDir()
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	for _, private := range []string{"PRIVATE_PROBE_CLASS", "PRIVATE_TIER", "fictional-candidate"} {
		if strings.Contains(string(b), private) {
			t.Fatalf("%s leaked", private)
		}
	}
	j, err := Load(dir)
	if err != nil || j.Records[0].TargetKeyAfter != "unavailable" || len(j.Records[0].Probes.Results) != MaxProbes {
		t.Fatal(j, err)
	}
	// A tick that ran no probe is captured-empty, distinct from legacy history.
	if got := clean(Record{Probes: Probes{Availability: "captured"}}).Probes; got.Availability != "captured" || len(got.Results) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSchemaFourUpgradeLeavesTargetMovesUnavailable(t *testing.T) {
	dir := t.TempDir()
	r := sample()
	r.Sequence = 1
	r.Decisions = Decisions{Availability: "captured"}
	b, _ := json.Marshal(Journal{SchemaVersion: 4, NextSequence: 2, Records: []Record{r}})
	// A schema-4 writer never produced the fields added by schema 5.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	rec := raw["records"].([]any)[0].(map[string]any)
	delete(rec, "targetKeyAfter")
	delete(rec, "probes")
	b, _ = json.Marshal(raw)
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := Load(dir)
	if err != nil || j.SchemaVersion != 4 || j.Records[0].TargetKeyAfter != "unavailable" || j.Records[0].Probes.Availability != "unavailable" || j.Records[0].Decisions.Availability != "captured" {
		t.Fatal(j, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(b) {
		t.Fatal("read mutated journal")
	}
	next := sample()
	next.Kind, next.TargetKeyAfter = "retarget", TargetKey("fictional-fallback")
	if err := Append(dir, next); err != nil {
		t.Fatal(err)
	}
	j, err = Load(dir)
	if err != nil || j.SchemaVersion != 6 || len(j.Records) != 2 || j.Records[0].TargetKeyAfter != "unavailable" || j.Records[0].Probes.Availability != "unavailable" || j.Records[1].Kind != "retarget" {
		t.Fatal(j, err)
	}
	// A legacy file cannot claim a retarget it had no way to record.
	next.Sequence = 1
	b, _ = json.Marshal(Journal{SchemaVersion: 4, NextSequence: 2, Records: []Record{next}})
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if j, err = Load(dir); err != nil || j.Records[0].Kind != "unavailable" || j.Records[0].TargetKeyAfter != "unavailable" {
		t.Fatal(j, err)
	}
	// A future schema is refused, not downgraded.
	b, _ = json.Marshal(Journal{SchemaVersion: 7, NextSequence: 1, Records: []Record{}})
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(dir); err != ErrUnavailable {
		t.Fatal(err)
	}
}

// The limit-wait causes come from the engine's typed rule, so neither a
// reworded reason nor text appended by the supervisor turns them into "other".
func TestLimitWaitCausesFollowTheDecisionEngine(t *testing.T) {
	limited := decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	in := decide.Input{Obs: limited, Control: decide.Control{Enabled: true}, InWindow: true, MaxReprompts: 2}
	for _, tc := range []struct {
		failover decide.Failover
		want     string
	}{
		{decide.Failover{Eligible: true, Available: true, ResetIn: 10 * time.Minute, WaitIfResetWithin: 30 * time.Minute}, "limit_wait"},
		{decide.Failover{Eligible: true, ResetIn: 72 * time.Hour, WaitIfResetWithin: 30 * time.Minute}, "limit_no_fallback"},
		{decide.Failover{ResetIn: 72 * time.Hour}, "limit_failover_disabled"},
	} {
		in.Failover = tc.failover
		d := decide.Decide(in)
		if d.Action != decide.None || DecisionCause(d) != tc.want || cleanCause(tc.want) != tc.want {
			t.Fatalf("%+v -> %q, want %s", tc.failover, d.Reason, tc.want)
		}
		appended := d
		appended.Reason += " (no fallback could take over; see probes and warnings)"
		if DecisionCause(appended) != tc.want {
			t.Fatalf("appended reason lost %s", tc.want)
		}
		var h Decisions
		h.Add("normal", in, d)
		if h.Evaluations[0].Cause != tc.want || h.Evaluations[0].Input.ResetIn != tc.failover.ResetIn {
			t.Fatalf("%+v", h.Evaluations[0])
		}
	}
	// Reason text alone never labels a wait, and an unknown rule is not stored.
	if Cause("limit resets in 4h0m0s; waiting") != "other" || DecisionCause(decide.Decision{LimitWait: "PRIVATE_RULE"}) != "unavailable" {
		t.Fatal("limit wait labelled without a recognised typed rule")
	}
	if d := decide.Decide(decide.Input{Obs: decide.Observation{State: decide.Idle}, Control: decide.Control{Enabled: true}, InWindow: true}); d.LimitWait != "" {
		t.Fatalf("%+v", d)
	}
}

func TestResolveTargetKey(t *testing.T) {
	key := TargetKey("fictional-fallback")
	if ResolveTargetKey("fictional-fallback") != key || ResolveTargetKey(key) != key || ResolveTargetKey("") != "unavailable" {
		t.Fatal("target selection is not stable")
	}
}

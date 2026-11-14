package incident

import "testing"

// Timing causes, stages and reason codes are fixed vocabularies: kept as
// recorded, anything like them but not them cleaned away, and a repair is
// never folded into a run of routine checks.
func TestTimingVocabulary(t *testing.T) {
	for _, c := range []string{TimingStepUp, TimingStepDown, TimingReturn, TimingRepair} {
		if got := cleanCause(c); got != c {
			t.Errorf("cause %s cleaned to %s", c, got)
		}
		if got := cleanAction(c); got != c {
			t.Errorf("action %s cleaned to %s", c, got)
		}
	}
	if got := cleanCause("timing_sideways"); got != "unavailable" {
		t.Errorf("an unknown timing cause kept: %s", got)
	}
	var d Decisions
	d.AddTiming("step_up", TimingStepUp, "near_reset_unused")
	d.AddTiming("guess", TimingStepUp, "because I said so")
	if e := d.Evaluations[0]; e.Stage != "timing_step_up" || e.Action != TimingStepUp || e.Cause != "near_reset_unused" {
		t.Errorf("%+v", e)
	}
	if e := d.Evaluations[1]; e.Stage != "unavailable" || e.Cause != "unavailable" {
		t.Errorf("free text kept: %+v", e)
	}
	repair := Record{Kind: "tick", Action: "none", Cause: TimingRepair, LimitWaitReport: "none", TargetKey: "a", TargetKeyAfter: "a"}
	if repeatable(repair) || tierOf(repair) != TierCondition {
		t.Errorf("repair repeatable %v, tier %s", repeatable(repair), tierOf(repair))
	}
}

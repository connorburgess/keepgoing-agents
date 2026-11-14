package incident

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// A hold for a restricted turn, the one re-prompt a person asks for by
// resuming, the continue and the wait after a refused turn, and the question a
// refusal streak raises each have a cause of their own in the journal, not
// "other", and each survives the journal's allowlist.
func TestDeclinedCauses(t *testing.T) {
	for reason, want := range map[string]string{
		decide.RestrictedReason:          "restricted",
		decide.RefusedContinueReason:     "refused_continue",
		decide.RefusalWaitReason:         "refused_wait",
		decide.RefusalAskReason:          "refusal_ask",
		decide.DeclineAcknowledgedReason: "decline_acknowledged",
	} {
		if got := Cause(reason); got != want || cleanCause(got) != want {
			t.Errorf("%q: cause %q (kept as %q), want %q", reason, got, cleanCause(got), want)
		}
	}
	in := decide.Input{Obs: decide.Observation{State: decide.Error, ErrorClass: decide.ErrRefused}, Control: decide.Control{Enabled: true}, InWindow: true, MaxReprompts: 3, SinceRefusal: time.Hour}
	d := decide.Decide(in)
	var h Decisions
	h.Add("normal", in, d)
	if e := h.Evaluations[0]; e.Cause != "refused_continue" || e.Input.ErrorClass != "refused" || e.Action != "reprompt" {
		t.Fatalf("%+v", e)
	}
}

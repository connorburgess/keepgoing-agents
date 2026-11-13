package incident

import (
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// A hold for a restricted or a refused turn, and the one re-prompt a person
// asks for by resuming, each have a cause of their own in the journal, not
// "other", and each survives the journal's allowlist.
func TestDeclinedCauses(t *testing.T) {
	for reason, want := range map[string]string{
		decide.RestrictedReason:          "restricted",
		decide.RefusedReason:             "refused",
		decide.DeclineAcknowledgedReason: "decline_acknowledged",
	} {
		if got := Cause(reason); got != want || cleanCause(got) != want {
			t.Errorf("%q: cause %q (kept as %q), want %q", reason, got, cleanCause(got), want)
		}
	}
	in := decide.Input{Obs: decide.Observation{State: decide.Error, ErrorClass: decide.ErrRefused}, Control: decide.Control{Enabled: true}, InWindow: true, MaxReprompts: 3}
	d := decide.Decide(in)
	var h Decisions
	h.Add("normal", in, d)
	if e := h.Evaluations[0]; e.Cause != "refused" || e.Input.ErrorClass != "refused" || e.Action != "await_user" {
		t.Fatalf("%+v", e)
	}
}

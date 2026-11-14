package incident

import (
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// A held-back nudge, the wait on a hung turn and the stall rule's hold after
// held-back nudges each have a cause of their own in the timeline.
func TestHungTurnCauses(t *testing.T) {
	for reason, want := range map[string]string{
		decide.HungTurnNudgeReason: "nudge_held",
		decide.HungTurnReason:      "hung_wait",
		"no progress after 3 nudges (3" + decide.HeldNudgesClause + "); " + decide.HungTurnHoldReason: "stall_limit_held",
		"no progress after 3 nudges (1" + decide.HeldNudgesClause + ")":                               "stall_limit_held",
		"no progress after 3 nudges": "stall_limit",
	} {
		if got := Cause(reason); got != want || cleanCause(got) != want {
			t.Errorf("%q: %s", reason, got)
		}
	}
}

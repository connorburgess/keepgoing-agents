package supervise

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A check that waits on a provider limit with a structured reset records
// that reset, and only that: no margin is guessed. A check that does not
// wait on a limit clears it.
func TestALimitWaitRecordsItsReset(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Errors.WaitIfResetWithin = config.Duration{Duration: time.Hour}
	reset := t0.Add(20 * time.Minute)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, ResetsAt: reset})
	r := runTick(t, w, t0)
	st, _ := state.Load(w.dir)
	if r.Action != "none" || !st.LimitResetsAt.Equal(reset) {
		t.Fatalf("%s (%s): recorded %v, want %v", r.Action, r.Reason, st.LimitResetsAt, reset)
	}
	p.obs = decide.Observation{State: decide.Idle}
	runTick(t, w, reset.Add(time.Minute))
	if st, _ = state.Load(w.dir); !st.LimitResetsAt.IsZero() {
		t.Fatalf("kept after the wait: %v", st.LimitResetsAt)
	}
}

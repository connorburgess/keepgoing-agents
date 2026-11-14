package supervise

import (
	"errors"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Only a stop-only check may run from the last configuration a daemon read:
// any other is refused before it observes or sends anything.
func TestOnlyAStopMayUseTheLastConfiguration(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	o := w.opts(t0)
	o.LastConfigReadAt = t0.Add(-1)
	if _, err := Tick(t.Context(), w.cfg, o); !errors.Is(err, ErrLastConfigNotStop) {
		t.Fatalf("%v", err)
	}
	if p.observed != 0 || len(p.sent) != 0 {
		t.Fatalf("observed %d, sent %v", p.observed, p.sent)
	}
	if st, _ := state.Load(w.dir); st.LastCheck != nil {
		t.Fatalf("a refused check was recorded: %+v", st.LastCheck)
	}
	// A stop-only check with no stop saved does nothing and records that it
	// came from the last configuration.
	o.StopOnly = true
	if _, err := Tick(t.Context(), w.cfg, o); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load(w.dir)
	if p.observed != 0 || len(p.sent) != 0 || st.LastCheck == nil || st.LastCheck.ConfigUnavailable == nil || !st.LastCheck.ConfigUnavailable.LastReadAt.Equal(t0.Add(-1).UTC()) {
		t.Fatalf("observed %d sent %v last %+v", p.observed, p.sent, st.LastCheck)
	}
}

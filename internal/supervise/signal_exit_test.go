package supervise

import (
	"context"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/decide"
)

// A factory may return the same adapter on later checks. Its cumulative
// count must never attribute earlier exits to the current observation.
type signalExitAdapter struct {
	adapter.Adapter
	exits int
	next  int
}

func (a *signalExitAdapter) SignalExits() int { return a.exits }
func (a *signalExitAdapter) Observe(context.Context) (decide.Observation, error) {
	a.exits += a.next
	return decide.Observation{}, nil
}

func TestSignalExitWarningsAreScopedToEachObservation(t *testing.T) {
	a := &signalExitAdapter{exits: 7}
	for check := range 2 {
		tick := &tick{ctx: context.Background(), res: &Result{}, opts: Options{DryRun: true}}
		for observation, delta := range []int{0, 2, 0, 1} {
			a.next = delta
			before := len(tick.res.Warnings)
			if _, err := tick.observe(a); err != nil {
				t.Fatal(err)
			}
			added := tick.res.Warnings[before:]
			if delta == 0 {
				if len(added) != 0 {
					t.Fatalf("check %d observation %d repeats earlier exits: %v", check, observation, added)
				}
				continue
			}
			if len(added) != 1 {
				t.Fatalf("warnings: %v", added)
			}
			want := "2 provider command(s) during this observation"
			if delta == 1 {
				want = "1 provider command(s) during this observation"
			}
			if !strings.Contains(added[0], want) || !strings.Contains(added[0], "signal source and cause are unknown") {
				t.Fatalf("incorrect count or cause claim: %s", added[0])
			}
			for _, claim := range []string{"killed by the host", "out of memory", "overloaded", "not looking at a provider problem"} {
				if strings.Contains(added[0], claim) {
					t.Fatalf("unproven cause: %s", added[0])
				}
			}
		}
	}
}

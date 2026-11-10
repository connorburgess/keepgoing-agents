package supervise

import (
	"context"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestActiveChildAcrossStallIntervalsDoesNotSpendBudgets(t *testing.T) {
	for _, parent := range []decide.State{decide.Idle, decide.Active, decide.Error} {
		t.Run(string(parent), func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.Progress.MaxNudges = 2
			w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
			p := w.primary(decide.Observation{State: parent, ActiveChildren: 1})
			run := func(at time.Time) Result {
				opts := w.opts(at)
				opts.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
				res, err := Tick(context.Background(), w.cfg, opts)
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			for i := 0; i < 6; i++ {
				res := run(t0.Add(time.Duration(i) * time.Hour))
				st, err := state.Load(w.dir)
				if err != nil || res.Action != "none" || res.Reason != "child work running" || st.Nudges != 0 || st.Reprompts != 0 || !st.LastWakeAt.IsZero() || len(p.sent) != 0 {
					t.Fatalf("interval %d: %+v %+v %v", i, res, st, err)
				}
				ctl, err := control.Load(w.dir)
				if err != nil || ctl.AwaitingUser {
					t.Fatal("child wait escalated the parent")
				}
			}
			p.obs = decide.Observation{State: decide.Idle}
			if res := run(t0.Add(5*time.Hour + time.Second)); res.Action != "wake" || len(p.sent) != 1 {
				t.Fatalf("idle parent not woken next check: %+v", res)
			}
		})
	}
}

package supervise

import (
	"context"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

func TestSupersededLoopNeverObservesSendsOrProbesButStops(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(decide.Observation{State: decide.Idle})
	if _, err := control.Update(w.dir, func(f *control.File) { f.SupersededBy = "thr_example_successor" }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	primary := w.adapters["primary"]
	if primary.observed != 0 || len(primary.sent) != 0 || len(w.probes) != 0 {
		t.Fatal("superseded loop accessed providers")
	}
	primary.obs = decide.Observation{State: decide.Active}
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	if _, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if primary.stopped != 1 {
		t.Fatal("superseded loop did not carry out stop")
	}
}

func TestSupersessionRefusesRouteRepairAndTimingActivation(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	if _, err := control.Update(w.dir, func(f *control.File) { f.SupersededBy = "thr_example_successor" }); err != nil {
		t.Fatal(err)
	}
	check := &tick{loop: w.cfg.Loop, opts: w.opts(t0)}
	if check.routeChangeAllowed() {
		t.Fatal("superseded loop permits route repair")
	}
	note := &TimingNote{}
	if got := check.applyStep(p, state.TimingMove{}, note); got != p || note.Outcome != "held" {
		t.Fatal("superseded loop permits timing activation")
	}
	if p.activated != 0 {
		t.Fatal("provider was activated")
	}
}

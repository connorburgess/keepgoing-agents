package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
)

// loop.context_window.reset_at is previewed by status only. A check
// decides, sends and saves exactly the same with it set or unset, on a
// reading far above any threshold.
func TestContextWindowThresholdChangesNoCheck(t *testing.T) {
	steps := []decide.Observation{
		{State: decide.Idle},
		{State: decide.Active},
		{State: decide.Idle},
		{State: decide.Error, ErrorClass: decide.ErrUnknown},
		{State: decide.Error, ErrorClass: decide.ErrRateLimit},
		{State: decide.Idle},
	}
	run := func(resetAt *float64) string {
		w := newWorld(t, true, false)
		w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
		if resetAt != nil {
			w.cfg.Loop.ContextWindow = &config.ContextWindow{ResetAt: resetAt}
		}
		p := w.primary(decide.Observation{})
		a := &contextFake{fakeAdapter: p, reading: adapter.ContextReading{UsedTokens: 199_000, WindowTokens: 200_000, Estimated: true}}
		var results []Result
		for i, obs := range steps {
			p.obs = obs
			o := w.opts(t0.Add(time.Duration(i) * 40 * time.Minute))
			o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return a, nil }
			o.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
			res, err := Tick(context.Background(), w.cfg, o)
			if err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
			res.At = time.Time{}
			results = append(results, res)
		}
		st := loadState(t, w)
		st.CheckFinishedAt = time.Time{} // wall time
		if st.Context == nil {
			t.Fatal("no reading was recorded, so the comparison proves nothing")
		}
		b, _ := json.Marshal(results)
		s, _ := json.Marshal(st)
		return string(b) + "\n" + string(s) + "\n" + jsonString(p.sent) + "\n" + jsonString(a.calls)
	}
	unset := run(nil)
	for _, v := range []float64{0.5, 0.8, 0.95} {
		if got := run(&v); got != unset {
			t.Errorf("reset_at %v: the checks differ\nunset: %s\n  set: %s", v, unset, got)
		}
	}
}

// The check that first sees a turn end notes it on the context record,
// before reading: a turn-end reading is as new as the turn end, and one that
// is unavailable leaves the turn end after the latest reading.
func TestTurnEndIsNotedOnTheContextRecord(t *testing.T) {
	w, f := contextWorld(t, decide.Observation{State: decide.Idle})
	if _, err := Tick(context.Background(), w.cfg, w.contextOpts(t0, f)); err != nil { // wake, reading at t0
		t.Fatal(err)
	}
	f.obs = decide.Observation{State: decide.Active}
	if _, err := Tick(context.Background(), w.cfg, w.contextOpts(t0.Add(10*time.Minute), f)); err != nil {
		t.Fatal(err)
	}
	if c := loadState(t, w).Context; !c.TurnEndSeenAt.IsZero() {
		t.Fatalf("a turn end was noted while active: %+v", c)
	}
	end := t0.Add(20 * time.Minute)
	f.obs = decide.Observation{State: decide.Idle}
	o := w.contextOpts(end, f)
	if _, err := Tick(context.Background(), w.cfg, o); err != nil {
		t.Fatal(err)
	}
	if c := loadState(t, w).Context; !c.TurnEndSeenAt.Equal(end) || !c.Latest.At.Equal(end) || !c.TurnEndsTracked {
		t.Fatalf("turn-end reading: %+v", c)
	}

	f.obs = decide.Observation{State: decide.Active}
	if _, err := Tick(context.Background(), w.cfg, w.contextOpts(end.Add(10*time.Minute), f)); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("bb context reading unavailable")
	later := end.Add(20 * time.Minute)
	f.obs = decide.Observation{State: decide.Idle}
	if _, err := Tick(context.Background(), w.cfg, w.contextOpts(later, f)); err != nil {
		t.Fatal(err)
	}
	if c := loadState(t, w).Context; !c.TurnEndSeenAt.Equal(later) || !c.Latest.At.Before(later) {
		t.Fatalf("unavailable turn-end reading: %+v", c)
	}
}

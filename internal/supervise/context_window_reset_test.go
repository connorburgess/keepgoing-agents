package supervise

import (
	"context"
	"encoding/json"
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

package supervise

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

type fakeAdapter struct {
	obs     decide.Observation
	obsErr  error
	sent    []string
	modes   []adapter.Mode
	stopped int
	noStop  bool
}

func (f *fakeAdapter) Name() string { return "fake" }
func (f *fakeAdapter) Observe(context.Context) (decide.Observation, error) {
	return f.obs, f.obsErr
}
func (f *fakeAdapter) Send(_ context.Context, p string, m adapter.Mode) error {
	f.sent = append(f.sent, p)
	f.modes = append(f.modes, m)
	return nil
}
func (f *fakeAdapter) Stop(context.Context) error {
	if f.noStop {
		return adapter.ErrUnsupported{Op: "stop"}
	}
	f.stopped++
	return nil
}

func setup(t *testing.T, enabled bool) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Version: 1, Loop: config.Loop{
		Name: "example-loop", StateDir: dir, Adapter: "process", Prompt: "keep going",
		Process:  &config.Process{StatusCommand: []string{"x"}, ResumeCommand: []string{"y"}},
		Progress: config.Progress{StallAfter: config.Duration{Duration: 30 * time.Minute}, MinWakeInterval: config.Duration{Duration: time.Hour}},
		Errors:   config.ErrorRules{MaxReprompts: 2, Backoff: config.Duration{Duration: 10 * time.Minute}},
	}}
	if enabled {
		if _, err := control.Update(dir, func(f *control.File) { f.Enabled = true }); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, dir
}

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestMissingControlFileNeverWakes(t *testing.T) {
	cfg, _ := setup(t, false)
	a := &fakeAdapter{obs: decide.Observation{State: decide.Idle}}
	res, err := Tick(context.Background(), cfg, a, Options{Now: t0})
	if err != nil || res.Action != "none" || len(a.sent) != 0 {
		t.Errorf("%+v %v sent=%v", res, err, a.sent)
	}
}

func TestIdleWakesAndRecords(t *testing.T) {
	cfg, dir := setup(t, true)
	a := &fakeAdapter{obs: decide.Observation{State: decide.Idle}}
	res, err := Tick(context.Background(), cfg, a, Options{Now: t0})
	if err != nil || res.Action != "wake" || len(a.sent) != 1 || a.modes[0] != adapter.ModeAuto {
		t.Fatalf("%+v %v", res, err)
	}
	st, _ := state.Load(dir)
	if !st.LastWakeAt.Equal(t0) {
		t.Errorf("lastWakeAt = %v", st.LastWakeAt)
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	cfg, _ := setup(t, true)
	a := &fakeAdapter{obs: decide.Observation{State: decide.Idle}}
	res, _ := Tick(context.Background(), cfg, a, Options{Now: t0, DryRun: true})
	if res.Action != "wake" || len(a.sent) != 0 {
		t.Errorf("%+v sent=%v", res, a.sent)
	}
}

func TestStallNudgesOnceFingerprintStopsChanging(t *testing.T) {
	cfg, _ := setup(t, true)
	cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	fp := "a"
	opts := func(now time.Time) Options {
		return Options{Now: now, Fingerprint: func([]string) (string, error) { return fp, nil }}
	}
	a := &fakeAdapter{obs: decide.Observation{State: decide.Active}}
	if res, _ := Tick(context.Background(), cfg, a, opts(t0)); res.Action != "none" {
		t.Fatalf("first tick: %+v", res)
	}
	fp = "b"
	if res, _ := Tick(context.Background(), cfg, a, opts(t0.Add(20*time.Minute))); res.Action != "none" {
		t.Fatalf("progress tick: %+v", res)
	}
	// 31 minutes with fingerprint "b" unchanged: stalled.
	res, _ := Tick(context.Background(), cfg, a, opts(t0.Add(51*time.Minute)))
	if res.Action != "nudge" || a.modes[len(a.modes)-1] != adapter.ModeQueue {
		t.Fatalf("stalled tick: %+v modes=%v", res, a.modes)
	}
	// Nudged recently: no second nudge inside min_wake_interval.
	res, _ = Tick(context.Background(), cfg, a, opts(t0.Add(90*time.Minute)))
	if res.Action != "none" {
		t.Fatalf("recent nudge: %+v", res)
	}
}

func TestErrorRepromptsThenAwaitsUser(t *testing.T) {
	cfg, dir := setup(t, true)
	a := &fakeAdapter{obs: decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}}
	res, _ := Tick(context.Background(), cfg, a, Options{Now: t0})
	if res.Action != "reprompt" || res.Reprompts != 1 {
		t.Fatalf("first: %+v", res)
	}
	res, _ = Tick(context.Background(), cfg, a, Options{Now: t0.Add(2 * time.Minute)})
	if res.Action != "none" {
		t.Fatalf("backoff: %+v", res)
	}
	res, _ = Tick(context.Background(), cfg, a, Options{Now: t0.Add(12 * time.Minute)})
	if res.Action != "reprompt" || res.Reprompts != 2 {
		t.Fatalf("second: %+v", res)
	}
	res, _ = Tick(context.Background(), cfg, a, Options{Now: t0.Add(30 * time.Minute)})
	if res.Action != "await_user" {
		t.Fatalf("exhausted: %+v", res)
	}
	ctl, _ := control.Load(dir)
	if !ctl.AwaitingUser {
		t.Error("control should be awaiting user")
	}
	res, _ = Tick(context.Background(), cfg, a, Options{Now: t0.Add(31 * time.Minute)})
	if res.Action != "none" || res.Reason != "awaiting user" || len(a.sent) != 2 {
		t.Fatalf("after await: %+v sent=%d", res, len(a.sent))
	}
}

func TestCleanRunResetsErrorBudget(t *testing.T) {
	cfg, dir := setup(t, true)
	a := &fakeAdapter{obs: decide.Observation{State: decide.Error}}
	Tick(context.Background(), cfg, a, Options{Now: t0})
	a.obs = decide.Observation{State: decide.Active}
	Tick(context.Background(), cfg, a, Options{Now: t0.Add(time.Minute)})
	st, _ := state.Load(dir)
	if st.Reprompts != 0 {
		t.Errorf("reprompts = %d", st.Reprompts)
	}
}

func TestStopIsImmediateAndSentOnce(t *testing.T) {
	cfg, _ := setup(t, true)
	control.Update(cfg.Loop.StateDir, func(f *control.File) { f.Stop = true })
	a := &fakeAdapter{obs: decide.Observation{State: decide.Active}}
	res, _ := Tick(context.Background(), cfg, a, Options{Now: t0})
	if res.Action != "stop" || a.stopped != 1 || len(a.sent) != 0 {
		t.Fatalf("%+v stopped=%d", res, a.stopped)
	}
	res, _ = Tick(context.Background(), cfg, a, Options{Now: t0.Add(time.Minute)})
	if res.Action != "none" || a.stopped != 1 {
		t.Fatalf("second: %+v stopped=%d", res, a.stopped)
	}
	// Adapter without stop support: warn, still never send.
	cfg2, _ := setup(t, true)
	control.Update(cfg2.Loop.StateDir, func(f *control.File) { f.Stop = true })
	b := &fakeAdapter{obs: decide.Observation{State: decide.Active}, noStop: true}
	res, err := Tick(context.Background(), cfg2, b, Options{Now: t0})
	if err != nil || res.Warning == "" || len(b.sent) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestObserveFailureTakesNoAction(t *testing.T) {
	cfg, _ := setup(t, true)
	a := &fakeAdapter{obsErr: errors.New("cli exploded")}
	res, err := Tick(context.Background(), cfg, a, Options{Now: t0})
	if err == nil || res.Action != "none" || len(a.sent) != 0 {
		t.Errorf("%+v %v", res, err)
	}
}

package supervise

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

type fakeAdapter struct {
	name        string
	obs         decide.Observation
	obsErr      error
	sent        []string
	modes       []adapter.Mode
	stopped     int
	noStop      bool
	activated   int
	activateErr error
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
func (f *fakeAdapter) Activate(context.Context) error {
	if f.activateErr != nil {
		return f.activateErr
	}
	f.activated++
	return nil
}

type world struct {
	cfg      *config.Config
	dir      string
	adapters map[string]*fakeAdapter
	probeOK  map[string]bool // exact and scheduled outcome per candidate
	probes   []string
	reports  []report.Event
}

func (w *world) Report(_ context.Context, e report.Event) error {
	w.reports = append(w.reports, e)
	return nil
}

func (w *world) opts(now time.Time) Options {
	return Options{
		Now: now,
		NewAdapter: func(a config.Agent, stateDir, target string) (adapter.Adapter, error) {
			f, ok := w.adapters[target]
			if !ok {
				f = &fakeAdapter{name: target, obs: decide.Observation{State: decide.Idle}}
				w.adapters[target] = f
			}
			return f, nil
		},
		Probe: func(_ context.Context, c config.Candidate, tier probe.Tier, _ time.Duration) probe.Result {
			w.probes = append(w.probes, c.ID+":"+string(tier))
			if w.probeOK[c.ID] {
				return probe.Result{OK: true}
			}
			return probe.Result{ErrorClass: "rate_limit"}
		},
		Reporter: w,
	}
}

func (w *world) reportTypes() string {
	var s []string
	for _, e := range w.reports {
		s = append(s, e.Type)
	}
	return strings.Join(s, ",")
}

func newWorld(t *testing.T, enabled bool, withFallbacks bool) *world {
	t.Helper()
	dir := t.TempDir()
	loop := config.Loop{Name: "example-loop", StateDir: dir, Prompt: "keep going"}
	loop.Agent = config.Agent{Adapter: "bb", Provider: "claude-code", Group: "claude-account-a", BB: &config.BB{Thread: "thr_primary"}}
	loop.Progress = config.Progress{StallAfter: config.Duration{Duration: 30 * time.Minute}, MinWakeInterval: config.Duration{Duration: time.Hour}}
	loop.Errors = config.ErrorRules{MaxReprompts: 2, Backoff: config.Duration{Duration: 10 * time.Minute}, FailoverOn: []string{"rate_limit"}, WaitIfResetWithin: config.Duration{Duration: 30 * time.Minute}}
	cfg := &config.Config{Version: 1, Loop: loop}
	if withFallbacks {
		mk := func(id, provider, group string) config.Candidate {
			c := config.Candidate{ID: id, Probe: config.Probe{Kind: "command", Command: []string{"x"}}}
			c.Agent = config.Agent{Adapter: "bb", Provider: provider, Group: group, BB: &config.BB{Thread: "thr_" + id}}
			return c
		}
		cfg.FallbackSet = &config.Fallbacks{
			Redundancy: config.Redundancy{MinHealthy: 2, Span: "provider"},
			Probe:      config.ProbePolicy{Every: config.Duration{Duration: 4 * time.Hour}, TTL: config.Duration{Duration: 6 * time.Hour}, DailyCap: 6, Timeout: config.Duration{Duration: time.Second}},
			Candidates: []config.Candidate{
				mk("same-account", "claude-code", "claude-account-a"), // shares the exhausted account
				mk("claude-other", "claude-code", "claude-account-b"),
				mk("codex-main", "codex", "codex"),
			},
		}
	}
	if enabled {
		if _, err := control.Update(dir, func(f *control.File) { f.Enabled = true }); err != nil {
			t.Fatal(err)
		}
	}
	return &world{cfg: cfg, dir: dir, adapters: map[string]*fakeAdapter{}, probeOK: map[string]bool{}}
}

func (w *world) primary(obs decide.Observation) *fakeAdapter {
	f := &fakeAdapter{obs: obs}
	w.adapters[state.Primary] = f
	return f
}

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
var limited = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}

func runTick(t *testing.T, w *world, now time.Time) Result {
	t.Helper()
	res, err := Tick(context.Background(), w.cfg, w.opts(now))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return res
}

func TestMissingControlFileNeverWakes(t *testing.T) {
	w := newWorld(t, false, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	if res := runTick(t, w, t0); res.Action != "none" || len(p.sent) != 0 {
		t.Errorf("%+v", res)
	}
}

func TestIdleWakesAndDryRunDoesNot(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	o := w.opts(t0)
	o.DryRun = true
	res, _ := Tick(context.Background(), w.cfg, o)
	if res.Action != "wake" || len(p.sent) != 0 {
		t.Fatalf("dry run: %+v", res)
	}
	if res := runTick(t, w, t0); res.Action != "wake" || len(p.sent) != 1 || p.modes[0] != adapter.ModeAuto {
		t.Fatalf("%+v", res)
	}
}

func TestStallNudges(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	fp := "a"
	p := w.primary(decide.Observation{State: decide.Active})
	run := func(now time.Time) Result {
		o := w.opts(now)
		o.Fingerprint = func([]string) (string, error) { return fp, nil }
		res, _ := Tick(context.Background(), w.cfg, o)
		return res
	}
	run(t0)
	fp = "b"
	run(t0.Add(20 * time.Minute))
	if res := run(t0.Add(51 * time.Minute)); res.Action != "nudge" || p.modes[0] != adapter.ModeQueue {
		t.Fatalf("%+v", res)
	}
	if res := run(t0.Add(90 * time.Minute)); res.Action != "none" {
		t.Fatalf("recent nudge: %+v", res)
	}
}

func TestStalledSessionEscalatesAfterMaxNudges(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	w.cfg.Loop.Progress.MaxNudges = 2
	fp := "a"
	p := w.primary(decide.Observation{State: decide.Active})
	run := func(at time.Duration) Result {
		o := w.opts(t0.Add(at))
		o.Fingerprint = func([]string) (string, error) { return fp, nil }
		res, _ := Tick(context.Background(), w.cfg, o)
		return res
	}
	run(0)
	want := []struct {
		at     time.Duration
		action string
	}{{31 * time.Minute, "nudge"}, {95 * time.Minute, "nudge"}, {160 * time.Minute, "await_user"}}
	for _, s := range want {
		if res := run(s.at); res.Action != s.action {
			t.Fatalf("at %v: %+v", s.at, res)
		}
	}
	if len(p.sent) != 2 || !strings.HasSuffix(w.reportTypes(), "await_user") {
		t.Errorf("sent=%d reports=%s", len(p.sent), w.reportTypes())
	}
	// Progress resets the count: after resume, nudging starts again.
	control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
	fp = "b"
	run(170 * time.Minute)
	if res := run(205 * time.Minute); res.Action != "nudge" {
		t.Fatalf("after progress: %+v", res)
	}
}

func TestErrorRepromptsThenAwaitsUser(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	steps := []struct {
		at   time.Duration
		want string
	}{{0, "reprompt"}, {2 * time.Minute, "none"}, {12 * time.Minute, "reprompt"}, {30 * time.Minute, "await_user"}, {31 * time.Minute, "none"}}
	for _, s := range steps {
		if res := runTick(t, w, t0.Add(s.at)); res.Action != s.want {
			t.Fatalf("at %v: %+v", s.at, res)
		}
	}
	if len(p.sent) != 2 || w.reportTypes() != "reprompt,reprompt,await_user" {
		t.Errorf("sent=%d reports=%s", len(p.sent), w.reportTypes())
	}
}

func TestStopIsImmediateAndSentOnce(t *testing.T) {
	w := newWorld(t, true, false)
	control.Update(w.dir, func(f *control.File) { f.Stop = true })
	p := w.primary(decide.Observation{State: decide.Active})
	runTick(t, w, t0)
	runTick(t, w, t0.Add(time.Minute))
	if p.stopped != 1 || len(p.sent) != 0 {
		t.Fatalf("stopped=%d sent=%d", p.stopped, len(p.sent))
	}
	w2 := newWorld(t, true, false)
	control.Update(w2.dir, func(f *control.File) { f.Stop = true })
	w2.primary(decide.Observation{State: decide.Active}).noStop = true
	if res := runTick(t, w2, t0); len(res.Warnings) == 0 {
		t.Errorf("expected a warning: %+v", res)
	}
}

func TestObserveFailureTakesNoAction(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{})
	p.obsErr = errors.New("cli exploded")
	res, err := Tick(context.Background(), w.cfg, w.opts(t0))
	if err == nil || res.Action != "none" || len(p.sent) != 0 {
		t.Errorf("%+v %v", res, err)
	}
}

func TestYieldSkipsWithoutObserving(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Yield = &config.Yield{BusyCommand: []string{"x"}}
	p := w.primary(decide.Observation{State: decide.Idle})
	o := w.opts(t0)
	o.Busy = func(context.Context, config.Yield) (bool, error) { return true, nil }
	res, _ := Tick(context.Background(), w.cfg, o)
	if !res.Yielded || len(p.sent) != 0 {
		t.Errorf("%+v", res)
	}
}

func TestScheduledProbesRunWhenDueOnly(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(decide.Observation{State: decide.Active})
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	res := runTick(t, w, t0)
	// same-account shares the primary's quota group, so it is never probed on schedule.
	if len(w.probes) != 2 || res.Redundancy == nil || !res.Redundancy.Meets {
		t.Fatalf("probes=%v res=%+v", w.probes, res.Redundancy)
	}
	runTick(t, w, t0.Add(time.Hour))
	if len(w.probes) != 2 {
		t.Errorf("probed again too soon: %v", w.probes)
	}
	runTick(t, w, t0.Add(5*time.Hour))
	if len(w.probes) != 4 {
		t.Errorf("not probed when due: %v", w.probes)
	}
	// Paused: no probes at all.
	control.Update(w.dir, func(f *control.File) { f.Paused = true })
	runTick(t, w, t0.Add(10*time.Hour))
	if len(w.probes) != 4 {
		t.Errorf("probed while paused: %v", w.probes)
	}
}

func TestHandoffToFirstCandidatePassingExactProbe(t *testing.T) {
	w := newWorld(t, true, true)
	p := w.primary(limited)
	// Both healthy on schedule; claude-other then fails its exact probe.
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"same-account", "claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"codex-main": true}
	res := runTick(t, w, t0)
	if res.Action != "handoff" || res.Handoff == nil || res.Handoff.To != "codex-main" || res.Target != "codex-main" {
		t.Fatalf("%+v %+v", res, res.Handoff)
	}
	if strings.Join(res.Handoff.Tried, ",") != "claude-other,codex-main" {
		t.Errorf("tried = %v (same-account must be skipped)", res.Handoff.Tried)
	}
	c := w.adapters["codex-main"]
	if len(c.sent) != 1 || !strings.Contains(c.sent[0], "taking over") || c.activated != 1 || len(p.sent) != 0 {
		t.Fatalf("codex sent=%v activated=%d primary sent=%d", c.sent, c.activated, len(p.sent))
	}
	if !strings.Contains(w.reportTypes(), "handoff") {
		t.Errorf("reports = %s", w.reportTypes())
	}
	st, _ := state.Load(w.dir)
	if st.ActiveTarget != "codex-main" || st.Handoffs != 1 {
		t.Errorf("state = %+v", st)
	}
	// The next tick supervises the new target.
	c.obs = decide.Observation{State: decide.Active}
	if res := runTick(t, w, t0.Add(time.Minute)); res.Target != "codex-main" || res.State != "active" {
		t.Errorf("next tick: %+v", res)
	}
}

func TestNoHandoffBelowMinimumHealthy(t *testing.T) {
	w := newWorld(t, true, true)
	p := w.primary(limited)
	w.probeOK = map[string]bool{"claude-other": true} // one provider only
	res := runTick(t, w, t0)
	if res.Action != "reprompt" || res.Handoff != nil || len(p.sent) != 1 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "handoff refused") || !strings.Contains(w.reportTypes(), "degraded") {
		t.Errorf("warnings=%v reports=%s", res.Warnings, w.reportTypes())
	}
	// Degraded is reported once, not every tick.
	runTick(t, w, t0.Add(time.Minute))
	if strings.Count(w.reportTypes(), "degraded") != 1 {
		t.Errorf("reports = %s", w.reportTypes())
	}
}

func TestAllExactProbesFailFallsBackToRepromptRules(t *testing.T) {
	w := newWorld(t, true, true)
	p := w.primary(limited)
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	res := runTick(t, w, t0) // probeOK empty: exact probes fail
	if res.Action != "reprompt" || !strings.Contains(res.Reason, "no fallback could take over") || len(p.sent) != 1 {
		t.Fatalf("%+v", res)
	}
	st, _ := state.Load(w.dir)
	if st.Target() != state.Primary {
		t.Errorf("target moved: %+v", st)
	}
}

func TestWaitsWhenLimitResetsSoon(t *testing.T) {
	w := newWorld(t, true, true)
	obs := limited
	obs.ResetsAt = t0.Add(10 * time.Minute)
	p := w.primary(obs)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	res := runTick(t, w, t0)
	if res.Action != "none" || !strings.Contains(res.Reason, "waiting") || len(p.sent) != 0 || res.ResetsAt == nil {
		t.Fatalf("%+v", res)
	}
}

func TestUnknownErrorNeverHandsOff(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	if res := runTick(t, w, t0); res.Action != "reprompt" || res.Handoff != nil {
		t.Fatalf("%+v", res)
	}
}

func TestRemovedTargetReturnsToPrimary(t *testing.T) {
	w := newWorld(t, true, true)
	state.Save(w.dir, state.File{ActiveTarget: "deleted-candidate"})
	w.primary(decide.Observation{State: decide.Active})
	res := runTick(t, w, t0)
	if res.Target != state.Primary || len(res.Warnings) == 0 {
		t.Errorf("%+v", res)
	}
}

type failingReporter struct{}

func (failingReporter) Report(context.Context, report.Event) error {
	return errors.New("endpoint down")
}

func TestStopWinsOverPauseDisableAndBrokenState(t *testing.T) {
	cases := map[string]func(w *world){
		"paused":        func(w *world) { control.Update(w.dir, func(f *control.File) { f.Paused = true }) },
		"disabled":      func(w *world) { control.Update(w.dir, func(f *control.File) { f.Enabled = false }) },
		"completed":     func(w *world) { control.Update(w.dir, func(f *control.File) { f.Completed = true }) },
		"awaiting user": func(w *world) { control.Update(w.dir, func(f *control.File) { f.AwaitingUser = true }) },
		"broken fingerprint": func(w *world) {
			w.cfg.Loop.Progress.Repositories = []string{"/fictional/missing"}
		},
		"busy yield": func(w *world) { w.cfg.Loop.Yield = &config.Yield{BusyCommand: []string{"x"}} },
	}
	for name, setup := range cases {
		w := newWorld(t, true, true)
		p := w.primary(decide.Observation{State: decide.Active})
		setup(w)
		control.Update(w.dir, func(f *control.File) { f.Stop = true })
		o := w.opts(t0)
		o.Fingerprint = func([]string) (string, error) { return "", errors.New("no such repository") }
		o.Busy = func(context.Context, config.Yield) (bool, error) { return true, nil }
		res, err := Tick(context.Background(), w.cfg, o)
		if err != nil || res.Action != "stop" || p.stopped != 1 || len(w.probes) != 0 {
			t.Errorf("%s: %+v err=%v stopped=%d probes=%v", name, res, err, p.stopped, w.probes)
		}
	}
}

func TestHandoffSkipsCandidateThatCannotActivate(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(limited)
	w.adapters["claude-other"] = &fakeAdapter{obs: decide.Observation{State: decide.Idle}, activateErr: errors.New("model not available")}
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	res := runTick(t, w, t0)
	if res.Target != "codex-main" || !strings.Contains(strings.Join(res.Warnings, ";"), "activate") {
		t.Fatalf("%+v", res)
	}
}

func TestBriefErrorSwitchesNothing(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(limited)
	w.cfg.Loop.HandoffFile = w.dir + "/HANDOFF.md"
	os.WriteFile(w.cfg.Loop.HandoffFile, []byte("   \n"), 0o600) // empty brief is an error
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	_, err := Tick(context.Background(), w.cfg, w.opts(t0))
	if err == nil {
		t.Fatal("expected the brief error")
	}
	for id, a := range w.adapters {
		if id != state.Primary && (a.activated > 0 || len(a.sent) > 0) {
			t.Errorf("%s was touched: activated=%d sent=%d", id, a.activated, len(a.sent))
		}
	}
	if st, _ := state.Load(w.dir); st.Target() != state.Primary {
		t.Errorf("target moved to %s", st.Target())
	}
}

func TestReportFailureNeverFailsTheTick(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrAuth})
	o := w.opts(t0)
	o.Reporter = failingReporter{}
	res, err := Tick(context.Background(), w.cfg, o)
	if err != nil || res.Action != "await_user" || len(res.Warnings) == 0 || len(p.sent) != 0 {
		t.Errorf("%+v %v", res, err)
	}
}

func TestDegradedThenRecovered(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(decide.Observation{State: decide.Active})
	w.probeOK = map[string]bool{"claude-other": true}
	runTick(t, w, t0)
	w.probeOK["codex-main"] = true
	runTick(t, w, t0.Add(5*time.Hour))
	if got := w.reportTypes(); !strings.HasSuffix(got, "degraded,recovered") || strings.Count(got, "degraded") != 1 {
		t.Errorf("reports = %s", got)
	}
}

func TestNoScheduledProbesOutsideWindow(t *testing.T) {
	w := newWorld(t, true, true)
	w.primary(decide.Observation{State: decide.Active})
	w.cfg.Loop.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Tue": {"09:00", "17:00"}}}
	runTick(t, w, t0) // a Monday
	if len(w.probes) != 0 {
		t.Errorf("probed outside the window: %v", w.probes)
	}
}

func TestPendingInteractionIsLeftAloneAndReportedOnce(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle, AwaitingInput: true})
	for i := 0; i < 3; i++ {
		if res := runTick(t, w, t0.Add(time.Duration(i)*time.Hour)); res.Action != "none" {
			t.Fatalf("tick %d: %+v", i, res)
		}
	}
	if len(p.sent) != 0 || w.reportTypes() != "await_user" {
		t.Errorf("sent=%v reports=%s", p.sent, w.reportTypes())
	}
	// Answered: the next idle tick wakes the session again, and a later
	// pending interaction is reported again.
	p.obs = decide.Observation{State: decide.Idle}
	if res := runTick(t, w, t0.Add(4*time.Hour)); res.Action != "wake" {
		t.Fatalf("after answer: %+v", res)
	}
	p.obs.AwaitingInput = true
	runTick(t, w, t0.Add(5*time.Hour))
	if w.reportTypes() != "await_user,wake,await_user" {
		t.Errorf("reports = %s", w.reportTypes())
	}
}

func TestGoneTargetAsksForAPerson(t *testing.T) {
	w := newWorld(t, true, false)
	w.primary(decide.Observation{State: decide.Unknown, Gone: true})
	if res := runTick(t, w, t0); res.Action != "await_user" || !strings.Contains(w.reportTypes(), "await_user") {
		t.Errorf("%+v reports=%s", res, w.reportTypes())
	}
}

func switchBackWorld(t *testing.T) (*world, *fakeAdapter, *fakeAdapter) {
	w := newWorld(t, true, true)
	w.cfg.Loop.SwitchBack = config.SwitchBack{Enabled: true, MinAfter: config.Duration{Duration: time.Hour}, RetryEvery: config.Duration{Duration: 30 * time.Minute}}
	w.cfg.Loop.Probe = config.Probe{Kind: "command", Command: []string{"x"}}
	obs := limited
	obs.ResetsAt = t0.Add(2 * time.Hour)
	p := w.primary(obs)
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	if res := runTick(t, w, t0); res.Target != "claude-other" {
		t.Fatalf("setup handoff: %+v", res)
	}
	fb := w.adapters["claude-other"]
	fb.obs = decide.Observation{State: decide.Idle}
	return w, p, fb
}

func TestSwitchBackAfterThePrimaryResets(t *testing.T) {
	w, p, fb := switchBackWorld(t)
	// Before the reset: the fallback keeps working.
	if res := runTick(t, w, t0.Add(time.Hour)); res.Action != "wake" || res.Target != "claude-other" {
		t.Fatalf("before reset: %+v", res)
	}
	// After the reset, between turns: confirm the primary and move back.
	w.probeOK["primary"] = true
	p.obs = decide.Observation{State: decide.Idle}
	res := runTick(t, w, t0.Add(2*time.Hour+time.Minute))
	if res.Action != "switch_back" || res.Target != state.Primary {
		t.Fatalf("switch back: %+v", res)
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "limit has reset") {
		t.Errorf("primary brief = %v", p.sent)
	}
	if !strings.Contains(w.reportTypes(), "switch_back") {
		t.Errorf("reports = %s", w.reportTypes())
	}
	st, _ := state.Load(w.dir)
	if st.Target() != state.Primary || !st.PrimaryResetsAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	_ = fb
}

func TestSwitchBackWaitsWhilePrimaryFailsAndNeverInterruptsATurn(t *testing.T) {
	w, p, fb := switchBackWorld(t)
	after := t0.Add(2*time.Hour + time.Minute)
	// The primary fails its probe: wake the fallback instead.
	res := runTick(t, w, after)
	if res.Action != "wake" || res.Target != "claude-other" || !strings.Contains(res.Reason, "primary failed its probe") {
		t.Fatalf("failed probe: %+v", res)
	}
	// Not retried before retry_every.
	runTick(t, w, after.Add(10*time.Minute))
	if n := strings.Count(strings.Join(w.probes, ","), "primary:exact"); n != 1 {
		t.Errorf("primary probed %d times before retry_every", n)
	}
	// Mid-turn on the fallback: no switch even though the primary is ready.
	w.probeOK["primary"] = true
	fb.obs = decide.Observation{State: decide.Active}
	if res := runTick(t, w, after.Add(40*time.Minute)); res.Action != "none" || res.Target != "claude-other" {
		t.Fatalf("mid-turn: %+v", res)
	}
	// The fallback's turn ends: switch back.
	fb.obs = decide.Observation{State: decide.Idle}
	if res := runTick(t, w, after.Add(45*time.Minute)); res.Action != "switch_back" {
		t.Fatalf("after turn: %+v", res)
	}
	if len(p.sent) != 1 {
		t.Errorf("primary sent = %v", p.sent)
	}
}

func TestSwitchBackOffByDefault(t *testing.T) {
	w, _, _ := switchBackWorld(t)
	w.cfg.Loop.SwitchBack.Enabled = false
	w.probeOK["primary"] = true
	if res := runTick(t, w, t0.Add(5*time.Hour)); res.Action != "wake" || res.Target != "claude-other" {
		t.Fatalf("%+v", res)
	}
}

func TestCompletedSkipsProviderProbesYieldAndFingerprint(t *testing.T) {
	w := newWorld(t, true, true)
	if _, err := control.Update(w.dir, func(f *control.File) { f.Completed = true }); err != nil {
		t.Fatal(err)
	}
	w.cfg.Loop.Yield = &config.Yield{BusyCommand: []string{"example"}}
	w.cfg.Loop.Progress.Repositories = []string{"/fictional/missing"}
	o := w.opts(t0)
	o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) {
		t.Fatal("completed task queried the provider")
		return nil, nil
	}
	o.Busy = func(context.Context, config.Yield) (bool, error) {
		t.Fatal("completed task ran the busy check")
		return false, nil
	}
	o.Fingerprint = func([]string) (string, error) { t.Fatal("completed task read the fingerprint"); return "", nil }
	// Completion must not depend on readable fallback health.
	if err := os.WriteFile(w.dir+"/health.json", []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		o.Now = t0.Add(time.Duration(i) * time.Hour)
		res, err := Tick(context.Background(), w.cfg, o)
		if err != nil || res.Action != "none" || res.Reason != "completed" || res.Control != "completed" || len(w.probes) != 0 || len(w.reports) != 0 {
			t.Fatalf("completed tick: %+v %v", res, err)
		}
	}
}

package supervise

import (
	"context"
	"fmt"
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

// outage is a world with no usable network: every probe runs until its
// timeout and every webhook report until the report timeout. Time is a fake
// wall clock that those calls advance, so a check's total duration is exact.
type outage struct {
	*world
	clock       time.Time
	probeUp     map[string]bool
	probeCalls  []string
	observeTime time.Duration
}

const reportTime = 20 * time.Second

func newOutage(t *testing.T, candidates int) *outage {
	o := &outage{world: newWorld(t, true, true), clock: t0, probeUp: map[string]bool{}}
	o.cfg.Loop.Report = &config.Report{}
	o.cfg.FallbackSet.Candidates = nil
	for i := 0; i < candidates; i++ {
		id := fmt.Sprintf("fallback-%d", i+1)
		c := config.Candidate{ID: id, Probe: config.Probe{Kind: "command", Command: []string{"x"}}}
		c.Agent = config.Agent{Adapter: "bb", Provider: fmt.Sprintf("provider-%d", i+1), Group: fmt.Sprintf("account-%d", i+1), BB: &config.BB{Thread: "thr_" + id}}
		o.cfg.FallbackSet.Candidates = append(o.cfg.FallbackSet.Candidates, c)
	}
	o.cfg.FallbackSet.Probe.Timeout = config.Duration{Duration: 90 * time.Second}
	return o
}

func (o *outage) Report(ctx context.Context, e report.Event) error {
	limit := reportTime
	if d, ok := ctx.Deadline(); ok {
		// The context's deadline is on the real clock; only its length matters.
		limit = min(limit, time.Until(d).Round(time.Second))
	}
	o.clock = o.clock.Add(limit)
	o.reports = append(o.reports, e)
	return fmt.Errorf("fictional network unreachable")
}

type slowAdapter struct {
	adapter.Adapter
	o *outage
}

func (s slowAdapter) Observe(ctx context.Context) (decide.Observation, error) {
	s.o.clock = s.o.clock.Add(s.o.observeTime)
	return s.Adapter.Observe(ctx)
}

func (o *outage) tick(t *testing.T, at time.Time) (Result, time.Duration, error) {
	t.Helper()
	opts := o.world.opts(at)
	inner := opts.NewAdapter
	opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
		ad, err := inner(a, dir, target)
		return slowAdapter{ad, o}, err
	}
	opts.Reporter = o
	opts.Clock = func() time.Time { return o.clock }
	opts.Probe = func(_ context.Context, c config.Candidate, tier probe.Tier, timeout time.Duration) probe.Result {
		o.probeCalls = append(o.probeCalls, fmt.Sprintf("%s:%s:%s", c.ID, tier, timeout))
		if o.probeUp[c.ID] {
			o.clock = o.clock.Add(2 * time.Second)
			return probe.Result{OK: true}
		}
		o.clock = o.clock.Add(timeout)
		return probe.Result{ErrorClass: string(decide.ErrUnknown), Detail: "timeout"}
	}
	start := o.clock
	r, err := Tick(context.Background(), o.cfg, opts)
	return r, o.clock.Sub(start), err
}

func skipped(r Result) (notRun, cut, ran int) {
	for _, p := range r.Probes {
		switch p.Skipped {
		case "tick_timeout":
			notRun++
		case "cut_short":
			cut++
		default:
			ran++
		}
	}
	return
}

// With the network down, four due fallbacks each ran a 90-second probe and a
// 20-second failure report before the session was even considered: over
// seven minutes for one check, which an external scheduler with a five-minute
// timeout killed before any tick line was written. The budget keeps the check
// inside its limit, still wakes the idle session, and records the result.
func TestTickBudgetBoundsProbesDuringAnOutage(t *testing.T) {
	o := newOutage(t, 4)
	p := o.primary(decide.Observation{State: decide.Idle})
	r, took, err := o.tick(t, t0)
	if err != nil || !r.Recorded || r.Outcome != "succeeded" {
		t.Fatalf("%+v %v", r, err)
	}
	if took > 3*time.Minute {
		t.Fatalf("check took %s, over the default budget", took)
	}
	notRun, cut, ran := skipped(r)
	if ran != 1 || notRun != 3 || cut != 0 || len(o.probeCalls) != 1 {
		t.Fatalf("probes ran=%d notRun=%d cut=%d calls=%v", ran, notRun, cut, o.probeCalls)
	}
	if r.Action != "wake" || len(p.sent) != 1 {
		t.Fatalf("the idle session was not woken: %+v", r)
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "loop.tick_timeout") {
		t.Fatalf("warnings: %v", r.Warnings)
	}
	// Skipped probes are not failures: nothing recorded, still due.
	reg, _ := health.Load(o.dir)
	for i, c := range o.cfg.FallbackSet.Candidates {
		due, _ := reg.Due(c, o.cfg.FallbackSet.Probe, t0.Add(time.Minute))
		if recorded := contains(reg.IDs(), c.ID); recorded != (i == 0) || due != (i != 0) {
			t.Fatalf("%s: recorded=%t due=%t", c.ID, recorded, due)
		}
	}
	st, _ := state.Load(o.dir)
	if st.LastCheck == nil || st.LastCheck.Action != "wake" || !st.CheckedAt.Equal(t0) {
		t.Fatalf("check not recorded: %+v", st.LastCheck)
	}
	// The next check probes the ones left out, not the one already probed.
	o.probeCalls = nil
	o.clock = o.clock.Add(10 * time.Minute)
	if _, took, _ = o.tick(t, t0.Add(10*time.Minute)); took > 3*time.Minute || len(o.probeCalls) != 1 || !strings.HasPrefix(o.probeCalls[0], "fallback-2:") {
		t.Fatalf("second check took %s, probes %v", took, o.probeCalls)
	}
}

// A probe given less than its own timeout that then times out says nothing
// about the candidate: it is noted as cut short and not recorded.
func TestProbeCutShortByTheBudgetIsNotAFailure(t *testing.T) {
	o := newOutage(t, 2)
	o.cfg.Loop.TickTimeout = config.Duration{Duration: time.Minute}
	o.primary(decide.Observation{State: decide.Idle})
	r, took, err := o.tick(t, t0)
	notRun, cut, ran := skipped(r)
	if err != nil || took > time.Minute || cut != 1 || notRun != 1 || ran != 0 || r.Action != "wake" {
		t.Fatalf("%+v took=%s %v", r, took, err)
	}
	if o.probeCalls[0] != "fallback-1:scheduled:30s" {
		t.Fatal(o.probeCalls)
	}
	reg, _ := health.Load(o.dir)
	if len(reg.IDs()) != 0 || strings.Contains(o.reportTypes(), "probe_failed") {
		t.Fatalf("a cut-short probe was recorded or reported: %v %s", reg.IDs(), o.reportTypes())
	}
	// With room, the same probe runs to its own timeout and is a real failure.
	o.cfg.Loop.TickTimeout = config.Duration{Duration: 10 * time.Minute}
	o.clock = o.clock.Add(time.Hour)
	r, _, _ = o.tick(t, t0.Add(time.Hour))
	if _, cut, ran := skipped(r); cut != 0 || ran != 2 || !strings.Contains(o.reportTypes(), "probe_failed") {
		t.Fatalf("%+v", r.Probes)
	}
}

// A handoff needs an exact probe. When the check has no time left for one,
// nothing is spent: no re-prompt, no hold, no move. The next check hands off.
func TestHandoffWaitsForTheNextCheckWhenTheBudgetIsSpent(t *testing.T) {
	o := newOutage(t, 2)
	for _, c := range o.cfg.FallbackSet.Candidates {
		o.probeUp[c.ID] = true
	}
	p := o.primary(decide.Observation{State: decide.Idle})
	if r, _, err := o.tick(t, t0); err != nil || r.Redundancy == nil || !r.Redundancy.Meets {
		t.Fatalf("fallbacks not confirmed healthy: %+v %v", r, err)
	}
	sends := len(p.sent)
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	o.observeTime = 175 * time.Second // a slow observation uses the budget
	o.clock = o.clock.Add(10 * time.Minute)
	r, _, err := o.tick(t, t0.Add(10*time.Minute))
	st, _ := state.Load(o.dir)
	ctl, _ := control.Load(o.dir)
	if err != nil || r.Action != "none" || !strings.Contains(r.Reason, "time budget ran out before a fallback could be confirmed") ||
		len(p.sent) != sends || st.Reprompts != 0 || st.Target() != state.Primary || ctl.AwaitingUser || !r.Recorded {
		t.Fatalf("%+v reprompts=%d %v", r, st.Reprompts, err)
	}
	if notRun, _, _ := skipped(r); notRun != 1 {
		t.Fatalf("%+v", r.Probes)
	}
	o.observeTime = time.Second
	o.clock = o.clock.Add(10 * time.Minute)
	if r, _, err := o.tick(t, t0.Add(20*time.Minute)); err != nil || r.Action != "handoff" || r.Target != "fallback-1" {
		t.Fatalf("next check did not hand off: %+v %v", r, err)
	}
}

// Stop and pause do not depend on the budget: they are handled even when the
// check has already used all of it.
func TestStopAndPauseAreHonoredWithNoBudgetLeft(t *testing.T) {
	for _, kind := range []string{"stop", "pause"} {
		t.Run(kind, func(t *testing.T) {
			o := newOutage(t, 4)
			p := o.primary(decide.Observation{State: decide.Active})
			if _, err := control.Update(o.dir, func(f *control.File) { f.Stop, f.Paused = kind == "stop", kind == "pause" }); err != nil {
				t.Fatal(err)
			}
			o.observeTime = time.Hour
			r, _, err := o.tick(t, t0)
			if err != nil || !r.Recorded || len(o.probeCalls) != 0 || len(p.sent) != 0 {
				t.Fatalf("%+v %v", r, err)
			}
			if kind == "stop" && (r.Action != "stop" || p.stopped != 1) {
				t.Fatalf("stop not delivered: %+v", r)
			}
			if kind == "pause" && r.Reason != "paused" {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// A report is part of recording the check: it is still attempted after the
// budget is spent, but only briefly.
func TestReportAfterTheBudgetIsBrief(t *testing.T) {
	o := newOutage(t, 0)
	o.cfg.FallbackSet = nil
	o.cfg.Loop.Errors.MaxReprompts = 1
	o.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrAuth})
	o.observeTime = time.Hour
	r, took, err := o.tick(t, t0)
	if err != nil || r.Action != "await_user" || o.reportTypes() != "await_user" || took != time.Hour+10*time.Second {
		t.Fatalf("%+v took=%s reports=%s %v", r, took, o.reportTypes(), err)
	}
}

// Registered worktrees that are gone from disk are reported, and do not stop
// the check from acting.
func TestMissingWorktreesAreReportedAndDoNotBlockAWake(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	p := w.primary(decide.Observation{State: decide.Idle})
	opts := w.opts(t0)
	opts.Fingerprint = func([]string) (string, error) { return "same", nil }
	opts.MissingWorktrees = func([]string) int { return 2 }
	r, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || r.Action != "wake" || len(p.sent) != 1 || r.MissingWorktrees != 2 || !strings.Contains(strings.Join(r.Warnings, "\n"), "git worktree prune") {
		t.Fatalf("%+v %v", r, err)
	}
	// A repository that cannot be read at all still fails closed.
	opts = w.opts(t0.Add(time.Minute))
	opts.Fingerprint = func([]string) (string, error) { return "", fmt.Errorf("fictional unreadable repository") }
	r, err = Tick(context.Background(), w.cfg, opts)
	if err == nil || r.Action != "none" || len(p.sent) != 1 || !r.Recorded || r.Outcome != "failed" {
		t.Fatalf("%+v %v", r, err)
	}
}

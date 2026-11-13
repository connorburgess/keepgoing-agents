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
	"github.com/connorburgess/keepgoing-agents/internal/incident"
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
	probeFast   map[string]bool // fails at once instead of hanging
	probeCalls  []string
	observeTime time.Duration
	// fingerprintTime and sendTime model slow Git and a slow send.
	fingerprintTime time.Duration
	sendTime        time.Duration
	// activateTime and yieldTime model a slow model switch and yield command.
	activateTime time.Duration
	yieldTime    time.Duration
	// checkNetwork configures a connectivity check; offline makes it run to
	// its timeout and fail.
	checkNetwork bool
	offline      bool
}

// reportTime is a webhook that fails slowly: two attempts of report.timeout.
const reportTime = 40 * time.Second

func newOutage(t *testing.T, candidates int) *outage {
	o := &outage{world: newWorld(t, true, true), clock: t0, probeUp: map[string]bool{}, probeFast: map[string]bool{}, observeTime: time.Second}
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

func (s slowAdapter) Activate(ctx context.Context) error {
	s.o.clock = s.o.clock.Add(s.o.activateTime)
	if act, ok := s.Adapter.(adapter.Activator); ok {
		return act.Activate(ctx)
	}
	return nil
}

func (s slowAdapter) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	s.o.clock = s.o.clock.Add(s.o.sendTime)
	return s.Adapter.Send(ctx, prompt, mode)
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
	if o.checkNetwork {
		opts.Online = func(context.Context) bool {
			if o.offline {
				o.clock = o.clock.Add(config.DefaultNetworkTimeout)
				return false
			}
			o.clock = o.clock.Add(time.Second)
			return true
		}
	}
	opts.Busy = func(context.Context, config.Yield) (bool, error) {
		o.clock = o.clock.Add(o.yieldTime)
		return false, nil
	}
	opts.Fingerprint = func([]string) (string, error) {
		o.clock = o.clock.Add(o.fingerprintTime)
		return "same", nil
	}
	opts.Probe = func(_ context.Context, c config.Candidate, tier probe.Tier, timeout time.Duration) probe.Result {
		o.probeCalls = append(o.probeCalls, fmt.Sprintf("%s:%s:%s", c.ID, tier, timeout))
		if o.probeUp[c.ID] {
			o.clock = o.clock.Add(2 * time.Second)
			return probe.Result{OK: true}
		}
		if o.probeFast[c.ID] {
			o.clock = o.clock.Add(2 * time.Second)
			return probe.Result{ErrorClass: string(decide.ErrAuth)}
		}
		o.clock = o.clock.Add(timeout)
		return probe.Result{ErrorClass: string(decide.ErrUnknown), Detail: "timeout", TimedOut: true}
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

func (o *outage) next(t *testing.T, i int) (Result, time.Duration) {
	t.Helper()
	at := t0.Add(time.Duration(i) * 10 * time.Minute)
	if o.clock.Before(at) {
		o.clock = at
	}
	r, took, err := o.tick(t, at)
	if err != nil || !r.Recorded {
		t.Fatalf("check %d: %+v %v", i, r, err)
	}
	return r, took
}

// With the network down, four due fallbacks each ran a 90-second probe and a
// 20-second failure report before the session was even considered: over
// seven minutes for one check, which an external scheduler with a five-minute
// timeout killed before any tick line was written. The budget keeps every
// check inside its limit, still wakes the idle session, and records it.
func TestTickBudgetBoundsProbesDuringAnOutage(t *testing.T) {
	o := newOutage(t, 4)
	p := o.primary(decide.Observation{State: decide.Idle})
	for i := 0; i < 4; i++ {
		o.probeCalls = nil
		r, took := o.next(t, i)
		if took > 3*time.Minute {
			t.Fatalf("check %d took %s, over the default budget", i, took)
		}
		// One probe fits per check. It runs for its whole timeout, so it is a
		// real failure: recorded, reported, and not due again at once. The
		// others wait their turn; none is run twice.
		want := fmt.Sprintf("fallback-%d:scheduled:1m30s", i+1)
		if notRun, cut, ran := skipped(r); ran != 1 || cut != 0 || notRun != 3-i || len(o.probeCalls) != 1 || o.probeCalls[0] != want {
			t.Fatalf("check %d: ran=%d notRun=%d cut=%d calls=%v", i, ran, notRun, cut, o.probeCalls)
		}
		if r.Action != "wake" || len(p.sent) != i+1 || r.Outcome != "succeeded" {
			t.Fatalf("check %d: the idle session was not woken: %+v", i, r)
		}
		if i < 3 && !strings.Contains(strings.Join(r.Warnings, "\n"), "loop.tick_timeout") {
			t.Fatalf("warnings: %v", r.Warnings)
		}
	}
	reg, _ := health.Load(o.dir)
	if got := reg.IDs(); len(got) != 4 {
		t.Fatalf("recorded probes: %v", got)
	}
	st, _ := state.Load(o.dir)
	if st.LastCheck == nil || st.LastCheck.Action != "wake" {
		t.Fatalf("check not recorded: %+v", st.LastCheck)
	}
}

// One fallback that hangs must not starve the others. With the first
// candidate hanging and the rest healthy, an earlier version shortened its
// probe on every check, never recorded the timeout, and re-ran it first each
// time: the healthy ones were never probed and handoffs stayed refused.
func TestHungFallbackDoesNotStarveScheduledProbes(t *testing.T) {
	o := newOutage(t, 4)
	for _, id := range []string{"fallback-2", "fallback-3", "fallback-4"} {
		o.probeUp[id] = true
	}
	o.primary(decide.Observation{State: decide.Active})
	var met int
	for i := 0; i < 6; i++ {
		r, took := o.next(t, i)
		if took > 3*time.Minute {
			t.Fatalf("check %d took %s", i, took)
		}
		if r.Redundancy != nil && r.Redundancy.Meets && met == 0 {
			met = i + 1
		}
	}
	if met != 2 {
		t.Fatalf("redundancy first met at check %d, want 2", met)
	}
	calls := strings.Join(o.probeCalls, " ")
	for i := 1; i <= 4; i++ {
		if n := strings.Count(calls, fmt.Sprintf("fallback-%d:scheduled:1m30s", i)); n != 1 {
			t.Fatalf("fallback-%d probed %d times: %s", i, n, calls)
		}
	}
	if strings.Contains(calls, "scheduled:") && strings.Count(calls, ":1m30s") != len(o.probeCalls) {
		t.Fatalf("a scheduled probe was shortened: %s", calls)
	}
	reg, _ := health.Load(o.dir)
	if e := reg.Candidates["fallback-1"]; e == nil || e.Last == nil || e.Last.OK || !e.Last.TimedOut || e.ProbesToday != 1 {
		t.Fatalf("the hung probe was not recorded and counted: %+v", e)
	}
}

// healthyThenHung confirms every fallback healthy, then makes the listed ones
// hang, with all of them due again while still listed healthy.
func healthyThenHung(t *testing.T, o *outage, hung ...string) int {
	t.Helper()
	for _, c := range o.cfg.FallbackSet.Candidates {
		o.probeUp[c.ID] = true
	}
	if r, _ := o.next(t, 0); r.Redundancy == nil || !r.Redundancy.Meets {
		t.Fatalf("fallbacks not confirmed healthy: %+v", r)
	}
	for _, id := range hung {
		o.probeUp[id] = false
	}
	o.probeCalls = nil
	return 5 * 6 // five hours on, in ten-minute checks: probes due, results still fresh
}

// A handoff must reach a working fallback even when others hang. Before, an
// exact probe stopped early by the budget ended the attempt, so with a
// hanging fallback listed first the working one was never tried: six checks
// in a row did nothing and reported nothing.
func TestHandoffReachesAWorkingFallbackPastHangingOnes(t *testing.T) {
	o := newOutage(t, 3)
	o.cfg.FallbackSet.Redundancy.MinHealthy = 1 // one confirmed fallback is enough to move
	p := o.primary(decide.Observation{State: decide.Idle})
	first := healthyThenHung(t, o, "fallback-1", "fallback-2")
	sends := len(p.sent)
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}

	// Check 1: the due scheduled probe of fallback-1 hangs for its full
	// timeout and is recorded. The exact probe of fallback-2 gets what is
	// left, is stopped early and is not held against it; nothing is left for
	// fallback-3. The handoff is put off, through the decision engine.
	r, took := o.next(t, first)
	st, _ := state.Load(o.dir)
	if r.Action != "none" || !strings.HasPrefix(r.Reason, decide.HandoffDeferredReason) || took > 3*time.Minute || len(p.sent) != sends || st.Reprompts != 0 || st.HandoffDeferrals != 1 || st.ExactCut["fallback-2"] != 1 {
		t.Fatalf("%+v took=%s state=%+v", r, took, st)
	}
	if notRun, cut, ran := skipped(r); cut != 1 || notRun < 1 || ran != 1 {
		t.Fatalf("%+v", r.Probes)
	}
	j, _ := incident.Load(o.dir)
	last := j.Records[len(j.Records)-1]
	if n := len(last.Decisions.Evaluations); n != 2 || last.Decisions.Evaluations[n-1].Stage != "handoff_deferred" || last.Cause != "handoff_deferred" {
		t.Fatalf("deferral not in incident history: %+v cause=%s", last.Decisions.Evaluations, last.Cause)
	}

	// Check 2: the working fallback is tried and takes over.
	r, took = o.next(t, first+1)
	if r.Action != "handoff" || r.Target != "fallback-3" || took > 3*time.Minute {
		t.Fatalf("the working fallback was not reached: %+v took=%s", r, took)
	}
	if st, _ := state.Load(o.dir); st.HandoffDeferrals != 0 || len(st.ExactCut) != 0 {
		t.Fatalf("deferral state kept after the handoff: %+v", st)
	}
}

// When no fallback can be confirmed, deferral is bounded. After the cap the
// hanging fallbacks are recorded as failed and the ordinary error rules act
// on the current target: re-prompts, then a hold. It never waits forever.
func TestHandoffDeferralIsBounded(t *testing.T) {
	o := newOutage(t, 2)
	p := o.primary(decide.Observation{State: decide.Idle})
	first := healthyThenHung(t, o, "fallback-1", "fallback-2")
	sends := len(p.sent)
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	var actions []string
	for i := 0; i < 8; i++ {
		r, took := o.next(t, first+i)
		if took > 3*time.Minute {
			t.Fatalf("check %d took %s", i, took)
		}
		actions = append(actions, r.Action)
		if r.Action == "await_user" {
			break
		}
	}
	got := strings.Join(actions, ",")
	if !strings.HasSuffix(got, "await_user") || strings.Count(got, "none") > 4 || len(p.sent) == sends {
		t.Fatalf("deferral was not bounded: %s (sends %d)", got, len(p.sent)-sends)
	}
	reg, _ := health.Load(o.dir)
	for _, id := range []string{"fallback-1", "fallback-2"} {
		if e := reg.Candidates[id]; e == nil || e.Last == nil || e.Last.OK {
			t.Fatalf("%s still listed healthy: %+v", id, e)
		}
	}
	if ctl, _ := control.Load(o.dir); !ctl.AwaitingUser {
		t.Fatal("hold not persisted")
	}
}

// A handoff needs an exact probe. When the check has no time left for one,
// nothing is spent: no re-prompt, no hold, no move. The next check hands off.
func TestHandoffWaitsForTheNextCheckWhenTheBudgetIsSpent(t *testing.T) {
	o := newOutage(t, 2)
	p := o.primary(decide.Observation{State: decide.Idle})
	healthyThenHung(t, o)
	sends := len(p.sent)
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	o.observeTime = 175 * time.Second // a slow observation uses the budget
	r, _ := o.next(t, 1)
	st, _ := state.Load(o.dir)
	ctl, _ := control.Load(o.dir)
	if r.Action != "none" || !strings.HasPrefix(r.Reason, decide.HandoffDeferredReason) || len(p.sent) != sends || st.Reprompts != 0 || st.Target() != state.Primary || ctl.AwaitingUser {
		t.Fatalf("%+v reprompts=%d", r, st.Reprompts)
	}
	if notRun, _, _ := skipped(r); notRun != 1 || len(o.probeCalls) != 0 {
		t.Fatalf("%+v %v", r.Probes, o.probeCalls)
	}
	o.observeTime = time.Second
	if r, _ := o.next(t, 2); r.Action != "handoff" || r.Target != "fallback-1" {
		t.Fatalf("next check did not hand off: %+v", r)
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

// The hard limit, tested. A check must end within loop.tick_timeout plus 90
// seconds whatever is slow. Every step is charged its maximum or almost
// nothing, independently: the yield command (10s), the observation (90s),
// the fingerprint (60s), every probe (its timeout), the model switch and the
// send (30s each) and every report (two attempts, 40s). Each combination is
// run against every kind of session, with and without fallbacks that were
// healthy before, for several checks. Before the steps were accounted
// together, a quick observation followed by a hung probe, two slow reports,
// a slow fingerprint and a slow send ran to 4m21s at the defaults and 4m51s
// with a two-minute probe timeout, and the yield and the model switch came
// on top.
func TestCheckEndsWithinItsHardLimitInEveryCombination(t *testing.T) {
	for _, tc := range []struct {
		name         string
		probeTimeout time.Duration
		budget       time.Duration // as config.Load would set it
	}{
		{"defaults", 90 * time.Second, 3 * time.Minute},
		{"probe timeout 2m, budget grown to 3m30s", 2 * time.Minute, 3*time.Minute + 30*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			limit := tc.budget + config.TickTail
			var worst time.Duration
			var where string
			runs := 0
			// The connectivity check is configured throughout. For a few
			// combinations the network is also down, so the check runs to its
			// own timeout as well.
			for n := 0; n < 32+4; n++ {
				mask, offline := n, n >= 32
				if offline {
					mask = []int{0, 4, 6, 31}[n-32]
				}
				slow := func(bit int, d time.Duration) time.Duration {
					if mask&(1<<bit) != 0 {
						return d
					}
					return 0
				}
				for _, obs := range []decide.Observation{
					{State: decide.Idle},
					{State: decide.Active},
					{State: decide.Error, ErrorClass: decide.ErrRateLimit},
					{State: decide.Error, ErrorClass: decide.ErrRateLimit, ResetsAt: t0.Add(72 * time.Hour)},
					{State: decide.Error, ErrorClass: decide.ErrUnknown},
					{State: decide.Error, ErrorClass: decide.ErrAuth},
					{State: decide.Idle, ErroredChildren: []string{"thr_child_a", "thr_child_b"}},
				} {
					for _, healthyFirst := range []bool{false, true} {
						o := newOutage(t, 4)
						o.cfg.Loop.TickTimeout = config.Duration{Duration: tc.budget}
						o.cfg.FallbackSet.Probe.Timeout = config.Duration{Duration: tc.probeTimeout}
						o.cfg.Loop.Progress.Repositories = []string{"/example/project"}
						o.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: time.Hour}
						o.cfg.Loop.Yield = &config.Yield{BusyCommand: []string{"x"}}
						for i := range o.cfg.FallbackSet.Candidates {
							o.cfg.FallbackSet.Candidates[i].BB.Model = "example-model" // so a handoff switches the model
						}
						p := o.primary(decide.Observation{State: decide.Idle})
						first := 0
						if healthyFirst {
							first = healthyThenHung(t, o, "fallback-1", "fallback-2", "fallback-3", "fallback-4")
						}
						o.checkNetwork, o.offline = true, offline
						o.yieldTime = slow(0, 10*time.Second)
						o.observeTime = time.Second + slow(1, observeTimeout-time.Second)
						o.fingerprintTime = slow(2, state.FingerprintTimeout)
						o.sendTime = slow(3, 30*time.Second)
						o.activateTime = slow(4, 30*time.Second)
						p.obs = obs
						for i := 1; i <= 3; i++ {
							r, took := o.next(t, first+i)
							runs++
							if took > worst {
								worst, where = took, fmt.Sprintf("offline=%t yield=%s observe=%s fingerprint=%s send=%s activate=%s healthyFirst=%t state=%s class=%s check=%d action=%s",
									offline, o.yieldTime, o.observeTime, o.fingerprintTime, o.sendTime, o.activateTime, healthyFirst, obs.State, obs.ErrorClass, i, r.Action)
							}
						}
					}
				}
			}
			t.Logf("%d checks; longest %s of a %s limit (%s)", runs, worst, limit, where)
			if worst > limit {
				t.Fatalf("a check took %s, over its hard limit of %s: %s", worst, limit, where)
			}
		})
	}
}

// A handoff that is due comes before routine probing. With one fallback due
// for a scheduled probe and hanging, and another confirmed healthy, the
// budget used to go to the hanging probe first, leaving the healthy fallback
// too little time. The exact probe now runs first and the handoff happens in
// the same check; the scheduled probe stays due.
func TestDueHandoffRunsBeforeScheduledProbes(t *testing.T) {
	o := newOutage(t, 2)
	o.cfg.FallbackSet.Redundancy.MinHealthy = 1
	p := o.primary(decide.Observation{State: decide.Idle})
	o.probeUp["fallback-2"] = true // fallback-1 never answers
	o.next(t, 0)                   // fallback-1 times out, recorded
	if r, _ := o.next(t, 1); r.Redundancy == nil || !r.Redundancy.Meets {
		t.Fatalf("fallback-2 not confirmed: %+v", r)
	}
	o.probeCalls = nil
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	r, took := o.next(t, 5*6) // five hours on: fallback-1 is due again
	if r.Action != "handoff" || r.Target != "fallback-2" || took > time.Minute {
		t.Fatalf("%+v took=%s", r, took)
	}
	if len(o.probeCalls) != 1 || !strings.HasPrefix(o.probeCalls[0], "fallback-2:exact:") {
		t.Fatalf("a scheduled probe ran before the handoff: %v", o.probeCalls)
	}

	// When the handoff does not happen after all, the scheduled probes it was
	// put ahead of still run in that check.
	o2 := newOutage(t, 2)
	o2.cfg.FallbackSet.Redundancy.MinHealthy = 1
	p2 := o2.primary(decide.Observation{State: decide.Idle})
	first := healthyThenHung(t, o2)
	p2.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, ResetsAt: t0.Add(5*time.Hour + 20*time.Minute)}
	r, _ = o2.next(t, first) // the limit resets within wait_if_reset_within: a wait, not a handoff
	if r.Action != "none" || len(o2.probeCalls) != 2 || !strings.Contains(strings.Join(o2.probeCalls, " "), ":scheduled:") {
		t.Fatalf("%+v probes=%v", r, o2.probeCalls)
	}
}

// Handoffs put off for lack of time end with the error episode, not with any
// check that happens to see the session active. A session alternating between
// error and active under a slow observation used to clear the count on every
// active check, so thirty checks in a row did nothing.
func TestDeferralCountSurvivesActiveChecks(t *testing.T) {
	o := newOutage(t, 2)
	// Keep the fallbacks' earlier results valid for the whole test. Otherwise
	// they expire partway, the handoff is refused for want of redundancy and
	// the ordinary rules act for that reason instead.
	o.cfg.FallbackSet.Probe.TTL = config.Duration{Duration: 1000 * time.Hour}
	o.cfg.FallbackSet.Probe.Every = config.Duration{Duration: 1000 * time.Hour}
	p := o.primary(decide.Observation{State: decide.Idle})
	first := healthyThenHung(t, o)
	sends := len(p.sent)
	o.observeTime = 175 * time.Second
	var actions []string
	for i := 0; i < 30; i++ {
		p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
		if i%2 == 1 {
			p.obs = decide.Observation{State: decide.Active}
		}
		r, _ := o.next(t, first+i)
		actions = append(actions, r.Action)
		if r.Action == "await_user" {
			break
		}
	}
	got := strings.Join(actions, ",")
	st, _ := state.Load(o.dir)
	if !strings.HasSuffix(got, "await_user") || len(actions) > 16 || len(p.sent) == sends {
		t.Fatalf("still doing nothing after %d checks: %s (deferrals %d, sends %d)", len(actions), got, st.HandoffDeferrals, len(p.sent)-sends)
	}
	// The episode's end does clear it.
	o3 := newOutage(t, 2)
	p3 := o3.primary(decide.Observation{State: decide.Idle})
	first = healthyThenHung(t, o3)
	o3.observeTime = 175 * time.Second
	p3.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	o3.next(t, first)
	if st, _ := state.Load(o3.dir); st.HandoffDeferrals != 1 {
		t.Fatalf("deferrals=%d", st.HandoffDeferrals)
	}
	p3.obs = decide.Observation{State: decide.Active}
	o3.next(t, first+1)
	if st, _ := state.Load(o3.dir); st.HandoffDeferrals != 1 {
		t.Fatal("an active check cleared the deferral count")
	}
	p3.obs = decide.Observation{State: decide.Idle}
	o3.observeTime = time.Second
	o3.next(t, first+2)
	if st, _ := state.Load(o3.dir); st.HandoffDeferrals != 0 {
		t.Fatal("an idle check did not end the episode")
	}
}

// Reports cannot extend a check without limit. Once the budget is spent,
// routine reports are not sent and at most three essential ones are, for ten
// seconds each.
func TestReportsAreBoundedOnceTheBudgetIsSpent(t *testing.T) {
	o := newOutage(t, 0)
	o.cfg.FallbackSet = nil
	o.primary(decide.Observation{State: decide.Idle})
	o.observeTime = time.Hour
	r, took, err := o.tick(t, t0)
	if err != nil || r.Action != "wake" || o.reportTypes() != "" || took != time.Hour || !strings.Contains(strings.Join(r.Warnings, "\n"), "report wake not sent") {
		t.Fatalf("%+v took=%s reports=%q %v", r, took, o.reportTypes(), err)
	}
	tk := &tick{ctx: context.Background(), res: &Result{}, opts: Options{Clock: func() time.Time { return t0 }, Reporter: report.Nop{}}, deadline: t0.Add(-time.Second)}
	sent := 0
	for i := 0; i < 6; i++ {
		if _, cancel, ok := tk.reportContext("await_user", ""); ok {
			cancel()
			sent++
		}
	}
	if sent != maxLateReports || len(tk.res.Warnings) != 3 {
		t.Fatalf("late essential reports sent=%d warnings=%v", sent, tk.res.Warnings)
	}
}

// Registered worktrees that are gone from disk are reported, and do not stop
// the check from acting or count as progress.
func TestMissingWorktreesAreReportedAndDoNotBlockAWake(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	p := w.primary(decide.Observation{State: decide.Idle})
	tick := func(minute int, fingerprint string, fail bool) (Result, error) {
		opts := w.opts(t0.Add(time.Duration(minute) * time.Minute))
		opts.Fingerprint = func([]string) (string, error) {
			if fail {
				return "", fmt.Errorf("fictional unreadable repository")
			}
			return fingerprint, nil
		}
		return Tick(context.Background(), w.cfg, opts)
	}
	const all = "kg3|m=0|aaaaaaaaaaaaaaaa=100.c.1111|bbbbbbbbbbbbbbbb=100.c.2222"
	const oneGone = "kg3|m=1|aaaaaaaaaaaaaaaa=100.c.1111"
	if r, err := tick(0, all, false); err != nil || r.Action != "wake" || r.MissingWorktrees != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	progressAt := func() time.Time { st, _ := state.Load(w.dir); return st.LastProgressAt }
	baseline := progressAt()
	r, err := tick(10, oneGone, false)
	if err != nil || r.Action != "wake" || len(p.sent) != 2 || r.MissingWorktrees != 1 || !strings.Contains(strings.Join(r.Warnings, "\n"), "git worktree prune") {
		t.Fatalf("%+v %v", r, err)
	}
	// Losing the worktree, and getting it back, is not progress.
	if !progressAt().Equal(baseline) {
		t.Fatal("a missing worktree counted as progress")
	}
	if _, err := tick(20, all, false); err != nil || !progressAt().Equal(baseline) {
		t.Fatalf("a returning worktree counted as progress: %v", err)
	}
	// A real change in a worktree that stayed is.
	if _, err := tick(30, "kg3|m=0|aaaaaaaaaaaaaaaa=100.c.9999|bbbbbbbbbbbbbbbb=100.c.2222", false); err != nil || !progressAt().Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("progress was lost: %v", err)
	}
	// A worktree seen for the first time is progress only when it shows work
	// of its own: a commit newer than any seen before, or changed files.
	const changed = "kg3|m=0|aaaaaaaaaaaaaaaa=100.c.9999|bbbbbbbbbbbbbbbb=100.c.2222"
	for i, tc := range []struct {
		added    string
		progress bool
	}{
		{"|cccccccccccccccc=100.c.3333", false}, // a fresh checkout of a commit already seen
		{"|cccccccccccccccc=50.c.3333", false},  // an older commit
		{"|cccccccccccccccc=101.c.3333", true},  // a new commit made there
		{"|cccccccccccccccc=100.d.3333", true},  // uncommitted work there
	} {
		at := 40 + 20*i
		if _, err := tick(at, changed, false); err != nil { // the worktree is not there
			t.Fatal(err)
		}
		before := progressAt()
		if _, err := tick(at+10, changed+tc.added, false); err != nil || progressAt().Equal(before) == tc.progress {
			t.Fatalf("new worktree %s: progress=%t, want %t (%v)", tc.added, !progressAt().Equal(before), tc.progress, err)
		}
	}
	// A repository that cannot be read at all still fails closed.
	sent := len(p.sent)
	r, err = tick(200, "", true)
	if err == nil || r.Action != "none" || len(p.sent) != sent || !r.Recorded || r.Outcome != "failed" {
		t.Fatalf("%+v %v", r, err)
	}
}

// With min_healthy 2, fallback A confirmed healthy and fallback B confirmed
// healthy but due for its scheduled probe: B's earlier result is still inside
// probe.ttl, so the redundancy rule is met and a due handoff goes to A
// without waiting for B's refresh. Evidence inside its ttl counts; a due
// scheduled probe is a refresh, not a precondition.
func TestHandoffReliesOnEvidenceInsideItsTTL(t *testing.T) {
	o := newOutage(t, 2)
	p := o.primary(decide.Observation{State: decide.Idle})
	first := healthyThenHung(t, o, "fallback-2") // B is now down, and due again five hours on
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	r, _ := o.next(t, first)
	if r.Action != "handoff" || r.Target != "fallback-1" || len(o.probeCalls) != 1 {
		t.Fatalf("%+v probes=%v", r, o.probeCalls)
	}
}

// When the confirmed fallback fails its exact probe and others were held
// back, those are probed in the same check and no re-prompt is spent before
// the next check has seen the result. The redundancy picture and the degraded
// report follow the late probes.
func TestFailedHandoffProbesHeldBackFallbacksBeforeReprompting(t *testing.T) {
	o := newOutage(t, 3)
	o.cfg.FallbackSet.Redundancy.MinHealthy = 1
	p := o.primary(decide.Observation{State: decide.Idle})
	o.probeUp["fallback-1"] = true
	o.next(t, 0) // fallback-1 healthy; 2 and 3 time out one per check
	o.next(t, 1)
	sends := len(p.sent)
	// Five hours on: fallback-1 has gone down, fallback-2 has come up, and
	// every scheduled probe is due again.
	// fallback-1 now refuses at once (a revoked login, say).
	o.probeUp["fallback-1"], o.probeUp["fallback-2"], o.probeFast["fallback-1"] = false, true, true
	o.probeCalls = nil
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
	r, took := o.next(t, 30)
	st, _ := state.Load(o.dir)
	if r.Action != "none" || !strings.HasPrefix(r.Reason, decide.HandoffDeferredReason) || !strings.Contains(r.Reason, "held back for them") ||
		len(p.sent) != sends || st.Reprompts != 0 || st.HandoffDeferrals != 1 || took > 4*time.Minute+30*time.Second {
		t.Fatalf("%+v reprompts=%d deferrals=%d took=%s", r, st.Reprompts, st.HandoffDeferrals, took)
	}
	calls := strings.Join(o.probeCalls, " ")
	if !strings.HasPrefix(calls, "fallback-1:exact:") || !strings.Contains(calls, "fallback-2:scheduled:") {
		t.Fatalf("probes: %s", calls)
	}
	// The result reflects the late probes: fallback-2 is healthy now.
	if r.Redundancy == nil || len(r.Redundancy.Healthy) != 1 || r.Redundancy.Healthy[0] != "fallback-2" {
		t.Fatalf("redundancy not re-evaluated: %+v", r.Redundancy)
	}
	if r, _ := o.next(t, 31); r.Action != "handoff" || r.Target != "fallback-2" {
		t.Fatalf("next check: %+v", r)
	}
}

// droppingReporter keeps the delivery record of reports that were not sent.
type droppingReporter struct {
	*outage
	dropped []string
}

func (d *droppingReporter) Dropped(e report.Event) error {
	d.dropped = append(d.dropped, e.Type)
	return nil
}

// A report that cannot be sent for lack of time does not use up an
// at-most-once attempt: the limit-wait and child-error reports are left for
// the next check, with nothing recorded and no request made. A routine
// report that is not sent is noted in the delivery records.
func TestReportsWithoutTimeAreLeftForTheNextCheck(t *testing.T) {
	o := newOutage(t, 0)
	o.cfg.FallbackSet = nil
	o.cfg.Loop.Report = &config.Report{Events: []string{"limit_wait", "child_error", "wake", "reprompt"}}
	o.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: time.Hour}
	p := o.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, ResetsAt: t0.Add(72 * time.Hour),
		ErroredChildren: []string{"thr_child_a"}})
	rep := &droppingReporter{outage: o}
	check := func(at time.Time) Result {
		t.Helper()
		opts := o.world.opts(at)
		opts.Reporter = rep
		opts.Clock = func() time.Time { return o.clock }
		inner := opts.NewAdapter
		opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
			ad, err := inner(a, dir, target)
			return slowAdapter{ad, o}, err
		}
		r, err := Tick(context.Background(), o.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// No time left at all, and the three late slots are not available to
	// these two: use them up first with a deadline in the past.
	o.observeTime = time.Hour
	o.clock = t0
	r := check(t0)
	st, _ := state.Load(o.dir)
	// Both are essential, so with slots free they are sent even now.
	if o.reportTypes() != "limit_wait,child_error" || st.LimitWaitReport == nil || st.ChildErrorReport == nil {
		t.Fatalf("essential reports were not sent with late slots free: %q %+v", o.reportTypes(), r.Warnings)
	}

	// With the late slots spent, they wait: nothing recorded, nothing sent.
	o2 := newOutage(t, 0)
	o2.cfg.FallbackSet = nil
	o2.cfg.Loop.Report = o.cfg.Loop.Report
	o2.cfg.Loop.Errors.ReportWaitAfter = config.Duration{Duration: time.Hour}
	o2.primary(p.obs)
	tk := &tick{ctx: context.Background(), cfg: o2.cfg, loop: o2.cfg.Loop, res: &Result{Action: "none", Target: state.Primary}, now: t0,
		opts: Options{Clock: func() time.Time { return t0 }, Reporter: rep}, deadline: t0.Add(-time.Second), lateReports: maxLateReports,
		incidentLimitWait: decide.WaitNoFallback}
	tk.st, _ = state.Load(o2.dir)
	tk.reportLongLimitWait(state.Primary, p.obs)
	tk.reportChildErrors(state.Primary, p.obs)
	saved, _ := state.Load(o2.dir)
	if tk.st.LimitWaitReport != nil || tk.st.ChildErrorReport != nil || saved.LimitWaitReport != nil || saved.ChildErrorReport != nil {
		t.Fatalf("an unsent report used its attempt: %+v %+v", tk.st.LimitWaitReport, tk.st.ChildErrorReport)
	}
	if n := strings.Count(strings.Join(tk.res.Warnings, "\n"), "put off to the next check"); n != 2 {
		t.Fatalf("warnings: %v", tk.res.Warnings)
	}
	// A routine report with no time is noted in the delivery records.
	rep.dropped = nil
	tk.report("wake", "example", nil)
	if len(rep.dropped) != 1 || rep.dropped[0] != "wake" {
		t.Fatalf("dropped: %v", rep.dropped)
	}
}

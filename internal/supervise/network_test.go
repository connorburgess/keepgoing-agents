package supervise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// An outage as observed on a supervised loop: the retry notice expired while
// the network was still down. Without a connectivity check the three allowed
// re-prompts were spent at once into the dead network (two of them before the
// network returned), leaving one failure away from a hold. With the check,
// nothing is spent while offline, each check records "network down" as its
// own reason, and the first check after connectivity returns re-prompts with
// the whole allowance intact.
func TestOutageDoesNotSpendRepromptsOrProbes(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Errors.MaxReprompts = 3
	for _, c := range w.cfg.FallbackSet.Candidates {
		w.probeOK[c.ID] = false // every probe would fail into the dead network
	}
	online, checks := false, 0
	retrying := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, Retrying: true, RetryKnown: true}
	expired := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, RetryKnown: true, RetryExpired: true}
	p := w.primary(decide.Observation{State: decide.Active, ErrorClass: decide.ErrUnknown, Retrying: true, RetryKnown: true})
	clock := func(h, m int) time.Time { return time.Date(2030, 3, 4, h, m, 0, 0, time.UTC) }
	for i, c := range []struct {
		at        time.Time
		obs       decide.Observation
		online    bool
		action    string
		reason    string
		reprompts int
	}{
		{clock(4, 50), p.obs, false, "none", "provider is retrying the turn", 0},
		{clock(5, 0), retrying, false, "none", "provider is retrying the turn", 0},
		{clock(5, 20), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(5, 30), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(5, 45), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(6, 1), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(7, 10), expired, true, "reprompt", "error: unknown", 1}, // the first check after connectivity returns
		{clock(7, 20), decide.Observation{State: decide.Active}, true, "none", "active", 1},
	} {
		p.obs, online = c.obs, c.online
		before := checks
		opts := w.opts(c.at)
		opts.Online = func(context.Context) bool { checks++; return online }
		r, err := Tick(context.Background(), w.cfg, opts)
		st, _ := state.Load(w.dir)
		if err != nil || r.Action != c.action || r.Reason != c.reason || st.Reprompts != c.reprompts || r.Outcome != "succeeded" {
			t.Fatalf("check %d (%s): %+v reprompts=%d %v", i, c.at.Format("15:04"), r, st.Reprompts, err)
		}
		if checks-before > 1 {
			t.Fatalf("check %d: connectivity checked %d times", i, checks-before)
		}
		if down := r.Network == "down"; down != !c.online || st.NetworkDownSince.IsZero() == !c.online && i < 7 {
			t.Fatalf("check %d: network=%q since=%v", i, r.Network, st.NetworkDownSince)
		}
		if !c.online {
			for _, probe := range r.Probes {
				if probe.Skipped != "network_down" {
					t.Fatalf("check %d: probe ran into the dead network: %+v", i, probe)
				}
			}
		}
	}
	if len(p.sent) != 1 || len(w.probes) != 2 {
		t.Fatalf("sends=%d probes=%v", len(p.sent), w.probes)
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("the outage ended in a hold")
	}
	// Probes skipped while offline were not recorded as failures.
	reg, _ := health.Load(w.dir)
	for _, id := range reg.IDs() {
		if e := reg.Candidates[id]; e.Last == nil || !e.Last.At.Equal(clock(7, 10)) {
			t.Fatalf("%s: a probe was recorded during the outage: %+v", id, e.Last)
		}
	}
	// The wait is its own cause in incident history. Identical consecutive
	// checks are retained once.
	j, _ := incident.Load(w.dir)
	var causes []string
	var waiting *incident.Record
	for i, r := range j.Records {
		if len(causes) == 0 || causes[len(causes)-1] != r.Cause {
			causes = append(causes, r.Cause)
		}
		if r.Cause == "network_down" && waiting == nil {
			waiting = &j.Records[i]
		}
	}
	if got := strings.Join(causes, ","); got != "retrying,network_down,other,active" {
		t.Fatal(got)
	}
	if e := waiting.Decisions.Evaluations; len(e) != 2 || e[1].Stage != "network_down" || e[0].Action != "reprompt" || e[1].Action != "none" {
		t.Fatalf("%+v", e)
	}
}

// The check is made only when something that needs the network is about to
// be done, and a loop with the check off is never gated.
func TestConnectivityIsCheckedOnlyWhenNeeded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		obs    decide.Observation
		checks int
		action string
	}{
		{"idle wake", decide.Observation{State: decide.Idle}, 0, "wake"},
		{"active", decide.Observation{State: decide.Active}, 0, "none"},
		{"provider retrying", decide.Observation{State: decide.Error, Retrying: true}, 0, "none"},
		{"queued after error", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, Queued: 1}, 0, "none"},
		{"error re-prompt", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, 1, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(tc.obs)
			checks := 0
			opts := w.opts(t0)
			opts.Online = func(context.Context) bool { checks++; return false }
			r, err := Tick(context.Background(), w.cfg, opts)
			if err != nil || checks != tc.checks || r.Action != tc.action || (tc.action == "none" && len(p.sent) != 0) {
				t.Fatalf("%+v checks=%d %v", r, checks, err)
			}
		})
	}
	// No check configured: recovery is attempted as before.
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	if r := runTick(t, w, t0); r.Action != "reprompt" || len(p.sent) != 1 || r.Network != "" {
		t.Fatalf("%+v", r)
	}
	// Stop and pause are handled without any check.
	for _, kind := range []string{"stop", "pause"} {
		w := newWorld(t, true, false)
		w.primary(decide.Observation{State: decide.Active})
		control.Update(w.dir, func(f *control.File) { f.Stop, f.Paused = kind == "stop", kind == "pause" })
		opts := w.opts(t0)
		opts.Online = func(context.Context) bool { t.Fatalf("%s checked connectivity", kind); return false }
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
	}
}

// A hold raised for an errored session may only be the outage showing: the
// re-prompt cap was spent into a dead network, or a login could not be
// refreshed. The connectivity check is made before such a hold too. Before,
// it was made only for a re-prompt or handoff, so an offline check with the
// cap spent or an auth error held the loop for a person and never looked at
// the network. Holds that are not about the session's error are unaffected.
func TestOutageDoesNotHoldAnErroredSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		obs       decide.Observation
		reprompts int
		offline   string // action while the network is down
		online    string // action once it is up
		checks    int
	}{
		{"re-prompt cap spent", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, 2, "none", "await_user", 1},
		{"auth error", decide.Observation{State: decide.Error, ErrorClass: decide.ErrAuth}, 0, "none", "await_user", 1},
		{"child waiting on a person", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, AwaitingChildren: []string{"thr_example_a"}}, 0, "await_user", "await_user", 0},
		{"target gone", decide.Observation{State: decide.Unknown, Gone: true}, 0, "await_user", "await_user", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, up := range []bool{false, true} {
				w := newWorld(t, true, false)
				p := w.primary(tc.obs)
				if err := state.Save(w.dir, state.File{Reprompts: tc.reprompts, LastRepromptAt: t0.Add(-time.Hour), LastState: "error"}); err != nil {
					t.Fatal(err)
				}
				checks := 0
				opts := w.opts(t0)
				opts.Online = func(context.Context) bool { checks++; return up }
				r, err := Tick(context.Background(), w.cfg, opts)
				want := map[bool]string{false: tc.offline, true: tc.online}[up]
				ctl, _ := control.Load(w.dir)
				if err != nil || r.Action != want || ctl.AwaitingUser != (want == "await_user") || checks != tc.checks || len(p.sent) != 0 {
					t.Fatalf("online=%t: %+v held=%t checks=%d %v", up, r, ctl.AwaitingUser, checks, err)
				}
				if !up && want == "none" && r.Reason != decide.NetworkDownReason {
					t.Fatalf("%+v", r)
				}
			}
		})
	}
}

// probeWorld is a world whose primary can be probed directly, as a
// switch-back would. reach says what that probe returns.
func probeWorld(t *testing.T, reach func() probe.Result) (*world, *fakeAdapter, func(minute int, online bool) (Result, state.File)) {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{}
	w.cfg.Loop.Probe = config.Probe{Kind: "command", Command: []string{"x"}}
	w.cfg.Loop.Network.VerifyAfter = config.Duration{Duration: time.Hour}
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	check := func(minute int, online bool) (Result, state.File) {
		t.Helper()
		opts := w.opts(t0.Add(time.Duration(minute) * time.Minute))
		opts.Online = func(context.Context) bool { return online }
		opts.Probe = func(_ context.Context, c config.Candidate, tier probe.Tier, _ time.Duration) probe.Result {
			w.probes = append(w.probes, c.ID+":"+string(tier))
			return reach()
		}
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := state.Load(w.dir)
		return r, st
	}
	return w, p, check
}

// A real outage of any length is waited out. After network.verify_after the
// provider is asked directly, once per interval; while that probe does not
// get through either, nothing is spent and no hold is raised, however long
// it lasts. A report is attempted at each verification until one is
// accepted.
func TestRealOutageIsVerifiedAndWaitedOutHoweverLong(t *testing.T) {
	w, p, check := probeWorld(t, func() probe.Result { return probe.Result{ErrorClass: "unknown", Detail: "timeout", TimedOut: true} })
	reports := func() int { return strings.Count(w.reportTypes(), "network_wait") }
	// Under an hour: waiting, no probe, no report.
	for _, minute := range []int{0, 30, 59} {
		if r, st := check(minute, false); r.Action != "none" || r.Network != "down" || len(w.probes) != 0 || reports() != 0 || st.NetworkDistrusted {
			t.Fatalf("+%dm: %+v probes=%v", minute, r, w.probes)
		}
	}
	// Checks every half hour for a day: one probe per hour at most.
	for minute := 60; minute <= 24*60; minute += 30 {
		if r, st := check(minute, false); r.Action != "none" || r.Network != "down" || st.NetworkDistrusted || st.Reprompts != 0 || len(p.sent) != 0 {
			t.Fatalf("+%dm: %+v", minute, r)
		}
	}
	if len(w.probes) != 24 || reports() != 1 {
		t.Fatalf("verification probes=%d (want 24), reports=%d", len(w.probes), reports())
	}
	// Then three weeks of occasional checks, hours apart: still nothing but
	// one probe per check, and no hold.
	checks := 0
	for minute := 24*60 + 7*60; minute <= 21*24*60; minute += 7*60 + 13 {
		checks++
		if r, st := check(minute, false); r.Action != "none" || r.Network != "down" || st.NetworkDistrusted || st.Reprompts != 0 || len(p.sent) != 0 {
			t.Fatalf("+%dm: %+v", minute, r)
		}
	}
	if len(w.probes) != 24+checks || reports() != 1 {
		t.Fatalf("verification probes=%d (want %d), reports=%d", len(w.probes), 24+checks, reports())
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("a real outage ended in a hold")
	}
	// The network returns: one re-prompt, with the whole allowance left.
	r, st := check(21*24*60+10, true)
	if r.Action != "reprompt" || r.Network != "restored" || r.NetworkDownFor != "504h10m0s" || st.Reprompts != 1 || len(p.sent) != 1 || !st.NetworkDownSince.IsZero() {
		t.Fatalf("%+v %+v", r, st)
	}
}

// A check target that cannot be reached while the provider works (a DNS
// filter, a proxy, a scheduler environment without its proxy settings) must
// not hold a loop without limit. At the first verification the provider
// answers the probe, so the check is set aside for that outage and the
// ordinary error rules apply: no worse than before the check existed. A
// provider that refuses for its own reason (an expired login) counts as an
// answer too, which is how that case reaches its hold and report. Only the
// check itself passing ends the distrust.
func TestWrongCheckTargetIsSetAsideWhenTheProviderAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer probe.Result
	}{
		{"probe passes", probe.Result{OK: true}},
		{"provider refuses: login expired", probe.Result{ErrorClass: "auth"}},
		{"provider refuses: rate limited", probe.Result{ErrorClass: "rate_limit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, p, check := probeWorld(t, func() probe.Result { return tc.answer })
			if r, _ := check(0, false); r.Action != "none" || r.Network != "down" {
				t.Fatalf("%+v", r)
			}
			r, st := check(60, false)
			if r.Network != "down_untrusted" || !st.NetworkDistrusted || r.Action != "reprompt" || len(w.probes) != 1 ||
				!strings.Contains(strings.Join(r.Warnings, "\n"), "not believed") || strings.Contains(w.reportTypes(), "network_wait") {
				t.Fatalf("%+v probes=%v reports=%s", r, w.probes, w.reportTypes())
			}
			// Ordinary rules from here: a second re-prompt, then the hold, with
			// no further probes.
			var actions []string
			for _, minute := range []int{70, 80, 90} {
				r, _ := check(minute, false)
				actions = append(actions, r.Action)
			}
			if got := strings.Join(actions, ","); got != "reprompt,await_user,none" || len(p.sent) != 2 || len(w.probes) != 1 {
				t.Fatalf("%s sends=%d probes=%v", got, len(p.sent), w.probes)
			}
			// The distrust ends only when the check passes; a new failure is
			// then believed again.
			control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
			saved, _ := state.Load(w.dir)
			saved.Reprompts = 0 // as after a resume with a raised allowance
			if err := state.Save(w.dir, saved); err != nil {
				t.Fatal(err)
			}
			if _, st := check(100, true); st.NetworkDistrusted || !st.NetworkDownSince.IsZero() {
				t.Fatalf("%+v", st)
			}
			if r, _ := check(200, false); r.Network != "down" {
				t.Fatalf("a new outage was not believed: %+v", r)
			}
		})
	}
}

// With no probe configured for the target the provider cannot be asked. The
// loop keeps waiting, and the report says that it cannot tell the two cases
// apart.
func TestOutageWithoutAProbeIsReportedAndWaitedOut(t *testing.T) {
	w, p, check := probeWorld(t, func() probe.Result { return probe.Result{OK: true} })
	w.cfg.Loop.Probe = config.Probe{}
	for _, minute := range []int{0, 60, 120, 600} {
		if r, _ := check(minute, false); r.Action != "none" || r.Network != "down" || len(w.probes) != 0 || len(p.sent) != 0 {
			t.Fatalf("+%dm: %+v", minute, r)
		}
	}
	var texts []string
	for _, e := range w.reports {
		if e.Type == "network_wait" {
			texts = append(texts, e.Text)
		}
	}
	if len(texts) != 1 || !strings.Contains(texts[0], "No probe is configured for this target") {
		t.Fatalf("%q", texts)
	}
}

// The outage shown by status must not outlive it. A check that needs no
// connectivity check and finds the session not in error clears it.
func TestRecordedOutageIsClearedWhenTheSessionRecovers(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	checks := 0
	run := func(minute int, obs decide.Observation) state.File {
		p.obs = obs
		opts := w.opts(t0.Add(time.Duration(minute) * time.Minute))
		opts.Online = func(context.Context) bool { checks++; return false }
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
		st, _ := state.Load(w.dir)
		return st
	}
	if st := run(0, p.obs); st.NetworkDownSince.IsZero() {
		t.Fatal("outage not recorded")
	}
	// Held or unobserved checks keep it; so does an errored session backing off.
	if st := run(10, decide.Observation{State: decide.Unknown}); st.NetworkDownSince.IsZero() {
		t.Fatal("an unknown observation cleared the outage")
	}
	for _, obs := range []decide.Observation{{State: decide.Active}, {State: decide.Idle}} {
		run(20, decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
		before := checks
		if st := run(30, obs); !st.NetworkDownSince.IsZero() || checks != before {
			t.Fatalf("%s: outage still recorded (checks made: %d)", obs.State, checks-before)
		}
	}
}

// A check that has already found the network down, here because fallback
// probes were due, does not go on to wake an idle session. It makes no extra
// request for that, and a dry run makes no request at all.
func TestKnownOutageWithholdsTheIdleWakeAndDryRunDoesNotCheck(t *testing.T) {
	w := newWorld(t, true, true)
	p := w.primary(decide.Observation{State: decide.Idle})
	checks := 0
	opts := w.opts(t0)
	opts.Online = func(context.Context) bool { checks++; return false }
	r, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || r.Action != "none" || r.Reason != decide.NetworkDownReason || len(p.sent) != 0 || checks != 1 || len(w.probes) != 0 {
		t.Fatalf("%+v sends=%d checks=%d probes=%v", r, len(p.sent), checks, w.probes)
	}
	// Without fallbacks nothing needs the network before an idle wake, so no
	// check is made and the wake goes out as before.
	w2 := newWorld(t, true, false)
	p2 := w2.primary(decide.Observation{State: decide.Idle})
	opts = w2.opts(t0)
	opts.Online = func(context.Context) bool { t.Fatal("connectivity checked for an idle wake"); return false }
	if r, err := Tick(context.Background(), w2.cfg, opts); err != nil || r.Action != "wake" || len(p2.sent) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	// Dry run: the decision is previewed without any request.
	w3 := newWorld(t, true, false)
	w3.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	opts = w3.opts(t0)
	opts.DryRun = true
	opts.Online = func(context.Context) bool { t.Fatal("a dry run made the connectivity request"); return false }
	if r, err := Tick(context.Background(), w3.cfg, opts); err != nil || r.Action != "reprompt" || r.Network != "" {
		t.Fatalf("%+v %v", r, err)
	}
}

// loop.limits.deadline is an absolute time the owner chose. One that passed
// while the loop was offline still holds it on the next check, and says so;
// the connectivity check is not consulted and does not soften it.
func TestDeadlinePassedWhileOfflineStillHolds(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Limits.Deadline = t0.Add(48 * time.Hour).Format(time.RFC3339)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	run := func(at time.Time) Result {
		opts := w.opts(at)
		opts.Online = func(context.Context) bool { return false }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(t0); r.Action != "none" || r.Reason != decide.NetworkDownReason {
		t.Fatalf("%+v", r)
	}
	r := run(t0.Add(21 * 24 * time.Hour)) // three weeks later, still offline
	ctl, _ := control.Load(w.dir)
	if r.Action != "await_user" || !strings.HasPrefix(r.Reason, "loop deadline reached") || !ctl.AwaitingUser || len(p.sent) != 0 {
		t.Fatalf("%+v held=%t", r, ctl.AwaitingUser)
	}
}

// loop.limits.no_progress_timeout measures time in which progress was
// possible. Time in which no check ran, and time in which the network was
// down, does not count; ordinary time between checks does.
func TestNoProgressTimeoutCountsOnlyTimeWorkWasPossible(t *testing.T) {
	setup := func() (*world, *fakeAdapter, func(time.Duration, bool) Result) {
		w := newWorld(t, true, false)
		w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
		w.cfg.Loop.Limits.NoProgressTimeout = config.Duration{Duration: 2 * time.Hour}
		w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Minute}
		p := w.primary(decide.Observation{State: decide.Active})
		run := func(after time.Duration, online bool) Result {
			opts := w.opts(t0.Add(after))
			opts.Fingerprint = func([]string) (string, error) { return "same", nil }
			opts.Online = func(context.Context) bool { return online }
			r, err := Tick(context.Background(), w.cfg, opts)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		return w, p, run
	}
	held := func(r Result) bool {
		return r.Action == "await_user" && strings.HasPrefix(r.Reason, "no Git progress within")
	}

	// Checks every ten minutes with no progress: the limit holds at two hours.
	_, _, run := setup()
	for m := 0; m < 120; m += 10 {
		if r := run(time.Duration(m)*time.Minute, true); held(r) {
			t.Fatalf("held early at +%dm", m)
		}
	}
	if r := run(2*time.Hour, true); !held(r) {
		t.Fatalf("ordinary time did not count: %+v", r)
	}

	// A three-week gap with no check at all counts as one ordinary interval
	// (30 minutes here), not as three weeks.
	_, _, run = setup()
	run(0, true)
	if r := run(21*24*time.Hour, true); held(r) {
		t.Fatalf("held because the clock advanced while nothing ran: %+v", r)
	}
	// From there ordinary checks count again: the gap's 30 minutes plus 90
	// more reach the two hours.
	for m := 10; m <= 80; m += 10 {
		if r := run(21*24*time.Hour+time.Duration(m)*time.Minute, true); held(r) {
			t.Fatalf("held before two hours of countable time, at +%dm: %+v", m, r)
		}
	}
	if r := run(21*24*time.Hour+90*time.Minute, true); !held(r) {
		t.Fatalf("the limit no longer applies after a gap: %+v", r)
	}

	// With checks running but the network down, an errored session is not
	// held, and that time is not counted afterwards either.
	_, p, run := setup()
	run(0, true)
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
	for m := 10; m <= 300; m += 10 {
		if r := run(time.Duration(m)*time.Minute, false); r.Action != "none" || r.Reason != decide.NetworkDownReason {
			t.Fatalf("+%dm offline: %+v", m, r)
		}
	}
	if r := run(310*time.Minute, true); r.Action != "reprompt" {
		t.Fatalf("first check after the outage: %+v", r)
	}
}

// The stall clock measures time in which the session was active with no Git
// change. A turn that works again after an outage, here restarted by the
// provider and not by keepgoing, is not nudged for the hours it could not
// work: it used to be nudged at once ("no progress for 72h").
func TestStallClockDoesNotCountTheOutage(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	w.cfg.Loop.Progress.MaxNudges = 3
	p := w.primary(decide.Observation{State: decide.Active})
	run := func(after time.Duration, obs decide.Observation, online bool) Result {
		t.Helper()
		p.obs = obs
		opts := w.opts(t0.Add(after))
		opts.Fingerprint = func([]string) (string, error) { return "same", nil }
		opts.Online = func(context.Context) bool { return online }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	active := decide.Observation{State: decide.Active}
	failed := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
	run(0, active, true)
	for _, h := range []int{1, 24, 71} { // three days in error with the network down
		if r := run(time.Duration(h)*time.Hour, failed, false); r.Action != "none" || r.Reason != decide.NetworkDownReason {
			t.Fatalf("+%dh: %+v", h, r)
		}
	}
	back := 72 * time.Hour
	// The provider's own retry has the turn working again when the network
	// returns. It has been active for no time at all.
	if r := run(back, active, true); r.Action != "none" || r.Reason != "active" || len(p.sent) != 0 {
		t.Fatalf("nudged for the outage: %+v", r)
	}
	if r := run(back+20*time.Minute, active, true); r.Action != "none" {
		t.Fatalf("nudged before stall_after of active time: %+v", r)
	}
	// Thirty minutes of active time without a Git change is a stall, as ever.
	if r := run(back+30*time.Minute, active, true); r.Action != "nudge" || !strings.HasPrefix(r.Reason, "no progress for 30m") {
		t.Fatalf("an ordinary stall is no longer nudged: %+v", r)
	}
}

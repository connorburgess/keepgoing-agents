package supervise

import (
	"errors"
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

// stepsWorld is a BB loop on example-medium with a step on its own thread,
// example-large, on the same allowance, and timing on with both as steps.
func stepsWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, true, true)
	w.cfg.Loop.BB.Model = "example-medium"
	w.cfg.Loop.Probe = config.Probe{Kind: "claude"}
	large := config.Candidate{ID: "example-large", Probe: config.Probe{Kind: "command", Command: []string{"x"}}}
	large.Agent = config.Agent{Adapter: "bb", Provider: "claude-code", Group: "claude-account-a", BB: &config.BB{Thread: "thr_primary", Model: "example-large"}}
	w.cfg.FallbackSet.Candidates = append(w.cfg.FallbackSet.Candidates, large)
	w.cfg.Loop.Timing = &config.Timing{Enabled: true, Windows: []string{"seven_day"}, NearReset: config.Duration{Duration: 6 * time.Hour},
		ProbeEvery: config.Duration{Duration: 24 * time.Hour}, Steps: []string{"primary", "example-large"}, Ceiling: "example-large",
		UnusedAtLeast: 0.25, DownAt: 0.95, MinOnStep: config.Duration{Duration: 2 * time.Hour}}
	for _, id := range []string{"primary", "claude-other", "codex-main", "same-account", "example-large"} {
		w.probeOK[id] = true
	}
	// Probed recently: no timing probe in these checks.
	seedHealth(t, w, func(e *health.Entry) { e.LastSched = t0 })
	return w
}

// seedHealth writes the first route's health entry, after edit.
func seedHealth(t *testing.T, w *world, edit func(*health.Entry)) {
	t.Helper()
	reg, err := health.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Candidates["primary"]
	if e == nil {
		e = &health.Entry{}
		reg.Candidates["primary"] = e
	}
	edit(e)
	if err := reg.Save(w.dir); err != nil {
		t.Fatal(err)
	}
}

// nearReset records a reading of the first route taken 20 minutes before at,
// at 60% used with the seven-day window resetting 2 h after at.
func nearReset(t *testing.T, w *world, at time.Time) time.Time {
	t.Helper()
	resets := at.Add(2 * time.Hour)
	id := probe.UsageIdentity(w.cfg.Loop.PrimaryCandidate())
	seedHealth(t, w, func(e *health.Entry) {
		e.Usage = &probe.Usage{At: at.Add(-20 * time.Minute), Source: probe.UsageSourceClaude, Identity: id,
			Windows: []probe.UsageWindow{{Key: "seven_day", Used: 0.6, ResetsAt: &resets}}}
	})
	return resets
}

func editState(t *testing.T, w *world, edit func(*state.File)) {
	t.Helper()
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	edit(&st)
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
}

func loadState(t *testing.T, w *world) state.File {
	t.Helper()
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func countReports(w *world, typ string) int {
	n := 0
	for _, e := range w.reports {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// At an idle turn boundary near a reset with 40% unused, the step is applied
// to the loop's own thread, recorded with how the loop got there, and
// reported. It sends nothing of its own: the check sends exactly what it
// sends with timing off, the engine's wake, and that wake goes to the new
// route, so the next turn runs on it.
func TestTimingStepAppliedAtATurnBoundary(t *testing.T) {
	off := stepsWorld(t)
	off.cfg.Loop.Timing = nil
	op := off.primary(decide.Observation{State: decide.Idle})
	nearReset(t, off, t0)
	offRes := runTick(t, off, t0)

	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	res := runTick(t, w, t0)
	large := w.adapters["example-large"]
	if large == nil || large.activated != 1 || res.Timing == nil || res.Timing.Outcome != "applied" || res.Target != "example-large" {
		t.Fatalf("%+v %+v", large, res.Timing)
	}
	st := loadState(t, w)
	if st.Target() != "example-large" || st.TargetSetBy != state.SetByTiming || st.Timing.LastMove == nil || st.Timing.LastMove.Action != "up" ||
		st.Timing.Pending != nil || countReports(w, "timing") != 1 {
		t.Fatalf("state %+v %+v; reports %s", st, st.Timing, w.reportTypes())
	}
	if res.Action != offRes.Action || len(large.sent)+len(p.sent) != len(op.sent) || len(p.sent) != 0 {
		t.Fatalf("timing changed what was sent: %s %v %v, without timing %s %v", res.Action, large.sent, p.sent, offRes.Action, op.sent)
	}
	// The step is reported after the check's state, which records it, is saved.
	if w.reports[0].Type != "wake" || w.reports[0].Text != "example-loop: wake example-large (idle)" ||
		w.reports[1].Type != "timing" || w.reports[1].Text != "example-loop: timing stepped up to example-large: the seven_day allowance resets in 2h 0m with 40% unused (reading 20m old)" {
		t.Fatalf("report: %+v", w.reports)
	}
}

// Timing steps only on the engine's own wake of an idle session whose turn
// ended. Every gate the check can see keeps the thread as it is, including
// each idle observation on which the engine holds back its wake.
func TestTimingStepGates(t *testing.T) {
	idle := func(edit func(*decide.Observation)) func(*testing.T, *world) {
		return func(t *testing.T, w *world) {
			o := decide.Observation{State: decide.Idle}
			edit(&o)
			w.adapters["primary"].obs = o
		}
	}
	for name, set := range map[string]func(*testing.T, *world){
		"paused":         func(t *testing.T, w *world) { control.Update(w.dir, func(f *control.File) { f.Paused = true }) },
		"disabled":       func(t *testing.T, w *world) { control.Update(w.dir, func(f *control.File) { f.Enabled = false }) },
		"awaiting user":  func(t *testing.T, w *world) { control.Update(w.dir, func(f *control.File) { f.AwaitingUser = true }) },
		"a turn running": func(t *testing.T, w *world) { w.adapters["primary"].obs = decide.Observation{State: decide.Active} },
		"in error": func(t *testing.T, w *world) {
			w.adapters["primary"].obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
		},
		"unknown": func(t *testing.T, w *world) { w.adapters["primary"].obs = decide.Observation{State: decide.Unknown} },
		// Idle, with the engine holding its wake back.
		"idle, retry scan incomplete":  idle(func(o *decide.Observation) { o.RetryUnverified = true }),
		"idle, children unverified":    idle(func(o *decide.Observation) { o.ChildrenUnverified = true }),
		"idle, children working":       idle(func(o *decide.Observation) { o.ActiveChildren = 1 }),
		"idle, an interaction pending": idle(func(o *decide.Observation) { o.AwaitingInput = true }),
		"idle, messages queued":        idle(func(o *decide.Observation) { o.Queued = 1 }),
		"idle, background jobs":        idle(func(o *decide.Observation) { o.Background = 1 }),
		"idle, outside the window": func(t *testing.T, w *world) {
			w.cfg.Loop.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Sun": {"09:00", "10:00"}}}
		},
		"idle, network down": func(t *testing.T, w *world) {
			// Probes are due and the network is down, so the engine holds
			// its idle wake back.
			seedHealth(t, w, func(e *health.Entry) { e.LastSched = time.Time{} })
			w.offline = true
		},
		"a person's retarget, reset not reached": func(t *testing.T, w *world) {
			at := t0.Add(2 * time.Hour)
			editState(t, w, func(s *state.File) {
				s.TargetSetBy = state.SetByRetarget
				s.Timing = &state.TimingRecord{Retarget: &state.TimingRetarget{At: t0.Add(-time.Hour), ResetsAt: &at}}
			})
		},
	} {
		w := stepsWorld(t)
		w.primary(decide.Observation{State: decide.Idle})
		nearReset(t, w, t0)
		set(t, w)
		res := runTick(t, w, t0)
		if large := w.adapters["example-large"]; large != nil && large.activated > 0 {
			t.Errorf("%s: stepped (action %s, %+v)", name, res.Action, res.Timing)
		}
		if st := loadState(t, w); st.Target() != state.Primary || countReports(w, "timing") != 0 {
			t.Errorf("%s: %s, reports %s", name, st.Target(), w.reportTypes())
		}
		if strings.HasPrefix(name, "idle") && res.Action == "wake" {
			t.Errorf("%s: the engine woke the session, so this case does not test a held-back wake", name)
		}
	}
}

// A stop written during the check, after its controls were read, wins: the
// thread is not changed.
func TestTimingStopDuringTheCheckWins(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	editState(t, w, func(s *state.File) { s.LastWakeAt = t0.Add(-10 * time.Minute) })
	nearReset(t, w, t0)
	p.onObserve = func() { control.Update(w.dir, func(f *control.File) { f.Stop = true }) }
	res := runTick(t, w, t0)
	if large := w.adapters["example-large"]; (large != nil && large.activated > 0) || res.Timing == nil || res.Timing.Outcome != "held" {
		t.Fatalf("%+v", res.Timing)
	}
	if st := loadState(t, w); st.Target() != state.Primary || st.Timing != nil && st.Timing.Pending != nil {
		t.Fatalf("%+v", st)
	}
}

// A failed step up keeps the loop where it is and keeps the record of the
// step for the next check to repair; the wake still goes out there. After
// max_reprompts failures for the window instance timing stops trying that
// step up and reports once, and status and the check agree it gave up.
func TestTimingStepUpFailureAndCap(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	w.adapters["example-large"] = &fakeAdapter{name: "example-large", activateErr: errors.New("fictional bb failure")}
	res := runTick(t, w, t0)
	st := loadState(t, w)
	if res.Timing.Outcome != "failed" || len(p.sent) != 1 || res.Target != state.Primary || st.Timing.Pending == nil {
		t.Fatalf("first failure: %+v, sent %v, pending %+v", res.Timing, p.sent, st.Timing.Pending)
	}
	res = runTick(t, w, t0.Add(10*time.Minute))
	if res.Timing.Outcome != "capped" || countReports(w, "timing") != 1 {
		t.Fatalf("second failure: %+v, reports %s", res.Timing, w.reportTypes())
	}
	activated := p.activated
	res = runTick(t, w, t0.Add(20*time.Minute))
	st = loadState(t, w)
	if res.Timing.Reason != "gave_up" || res.Timing.Outcome != "stay" || countReports(w, "timing") != 1 || st.Timing.Failures != 2 || !st.Timing.Capped || st.Target() != state.Primary {
		t.Fatalf("after the cap: %+v %+v, reports %s", res.Timing, st.Timing, w.reportTypes())
	}
	// The pending record left by the failures was repaired by setting the
	// route on record, primary, on the thread.
	if p.activated <= activated-1 || st.Timing.Pending != nil {
		t.Fatalf("repair: activated %d, pending %+v", p.activated, st.Timing.Pending)
	}
}

// A return to primary is never capped: it is tried at every turn boundary;
// its first failure is reported, then once more at max_reprompts failures,
// and its success once.
func TestTimingReturnIsRetriedNotCapped(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	p.activateErr = errors.New("fictional bb failure")
	w.adapters["example-large"] = &fakeAdapter{name: "example-large", obs: decide.Observation{State: decide.Idle}}
	editState(t, w, func(s *state.File) {
		s.ActiveTarget, s.TargetSetBy = "example-large", state.SetByTiming
		s.Timing = &state.TimingRecord{LastMove: &state.TimingMove{At: t0.Add(-3 * time.Hour), From: state.Primary, To: "example-large", Action: "up", Window: "seven_day", ResetsAt: t0.Add(-time.Minute)}}
	})
	for i := 0; i < 4; i++ {
		res := runTick(t, w, t0.Add(time.Duration(i)*10*time.Minute))
		if res.Timing == nil || res.Timing.Action != "back" || res.Timing.Outcome != "failed" {
			t.Fatalf("check %d: %+v", i, res.Timing)
		}
	}
	if n := countReports(w, "timing"); n != 2 || !strings.Contains(w.reports[2].Text, "still on example-large after 2 failed moves to primary; timing keeps trying at each turn boundary") {
		t.Fatalf("%d timing reports for the failures: %s %+v", n, w.reportTypes(), w.reports)
	}
	if st := loadState(t, w); st.Timing.Capped || st.Timing.Failures != 4 || st.Target() != "example-large" {
		t.Fatalf("%+v", st.Timing)
	}
	p.activateErr = nil
	res := runTick(t, w, t0.Add(50*time.Minute))
	st := loadState(t, w)
	if res.Timing.Outcome != "applied" || st.Target() != state.Primary || countReports(w, "timing") != 3 || st.Timing.Failures != 0 ||
		!strings.Contains(w.reports[len(w.reports)-1].Text+w.reports[len(w.reports)-2].Text, "returned to primary") {
		t.Fatalf("%+v %+v %s", res.Timing, st.Timing, w.reportTypes())
	}
}

// A step recorded and not settled (the check ended between recording it and
// settling it) is settled by a later check, between turns only: while a turn
// runs the record is kept; once the session is idle the route on record is
// applied again, then timing decides afresh.
func TestTimingUnsettledIntentIsSettled(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Active})
	editState(t, w, func(s *state.File) {
		s.Timing = &state.TimingRecord{Pending: &state.TimingMove{At: t0.Add(-10 * time.Minute), From: state.Primary, To: "example-large", Action: "up"}}
	})
	runTick(t, w, t0)
	if st := loadState(t, w); p.activated != 0 || st.Timing.Pending == nil {
		t.Fatalf("repaired during a turn: activated %d, %+v", p.activated, st.Timing)
	}
	p.obs = decide.Observation{State: decide.Idle}
	res := runTick(t, w, t0.Add(10*time.Minute))
	st := loadState(t, w)
	if p.activated != 1 || st.Timing.Pending != nil || st.Target() != state.Primary || !strings.Contains(strings.Join(res.Warnings, "\n"), "recorded and not settled") {
		t.Fatalf("activated %d, %+v, %v", p.activated, st.Timing, res.Warnings)
	}
}

// After the reset a step up was for, the loop returns to primary.
func TestTimingReturnsAtTheReset(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	w.adapters["example-large"] = &fakeAdapter{name: "example-large", obs: decide.Observation{State: decide.Idle}}
	editState(t, w, func(s *state.File) {
		s.ActiveTarget, s.TargetSetBy, s.LastWakeAt = "example-large", state.SetByTiming, t0.Add(-10*time.Minute)
		s.Timing = &state.TimingRecord{LastMove: &state.TimingMove{At: t0.Add(-3 * time.Hour), From: state.Primary, To: "example-large", Action: "up", Window: "seven_day", ResetsAt: t0.Add(-time.Minute)}}
	})
	res := runTick(t, w, t0)
	st := loadState(t, w)
	if p.activated != 1 || res.Timing.Outcome != "applied" || st.Target() != state.Primary || st.TargetSetBy != state.SetByTiming || st.Timing.LastMove.Action != "back" {
		t.Fatalf("%+v %+v", res.Timing, st)
	}
}

// The failover path with timing: a step's route hits a usage limit and the
// failover rules hand off to another account; timing never moves the loop
// off it; switch-back returns to primary once the limit lifts; and timing
// does not step up a second time for the same window instance.
func TestTimingFailoverSwitchBackNoSecondStepUp(t *testing.T) {
	w := stepsWorld(t)
	w.cfg.Loop.SwitchBack = config.SwitchBack{Enabled: true, MinAfter: config.Duration{Duration: 30 * time.Minute}, RetryEvery: config.Duration{Duration: 30 * time.Minute}}
	w.primary(decide.Observation{State: decide.Idle})
	resets := nearReset(t, w, t0)
	runTick(t, w, t0)
	if st := loadState(t, w); st.Target() != "example-large" {
		t.Fatalf("no step up: %+v", st)
	}
	// The step's route hits a usage limit: a handoff to another account.
	w.adapters["example-large"].obs = limited
	runTick(t, w, t0.Add(10*time.Minute))
	st := loadState(t, w)
	if st.Target() == "example-large" || st.Target() == state.Primary || st.TargetSetBy != state.SetByHandoff || st.Timing.LastMove == nil {
		t.Fatalf("no handoff from the step: %+v %+v", st, st.Timing)
	}
	fallback := st.Target()
	// On the fallback, near the reset with room: timing never moves it.
	w.adapters[fallback].obs = decide.Observation{State: decide.Idle}
	nearReset(t, w, t0.Add(20*time.Minute))
	res := runTick(t, w, t0.Add(20*time.Minute))
	if res.Timing == nil || res.Timing.Reason != "on_fallback_by_failover" || loadState(t, w).Target() != fallback {
		t.Fatalf("timing on the fallback: %+v", res.Timing)
	}
	// Switch-back returns to primary after min_after.
	runTick(t, w, t0.Add(55*time.Minute))
	if st := loadState(t, w); st.Target() != state.Primary || st.TargetSetBy != state.SetBySwitchBack {
		t.Fatalf("no switch-back: %+v", st)
	}
	// Still before the same reset: no second step up for that instance.
	w.primary(decide.Observation{State: decide.Idle})
	id := probe.UsageIdentity(w.cfg.Loop.PrimaryCandidate())
	seedHealth(t, w, func(e *health.Entry) {
		e.Usage = &probe.Usage{At: t0.Add(60 * time.Minute), Source: probe.UsageSourceClaude, Identity: id, Windows: []probe.UsageWindow{{Key: "seven_day", Used: 0.6, ResetsAt: &resets}}}
	})
	before := w.adapters["example-large"].activated
	res = runTick(t, w, t0.Add(65*time.Minute))
	if res.Timing == nil || res.Timing.Reason != "already_stepped_up" || w.adapters["example-large"].activated != before || loadState(t, w).Target() != state.Primary {
		t.Fatalf("second step up: %+v", res.Timing)
	}
}

// Switch-back returns only from a route a handoff chose, never from one
// timing chose, in table form.
func TestSwitchBackReadyNotFromATimingStep(t *testing.T) {
	sb := config.SwitchBack{Enabled: true, MinAfter: config.Duration{Duration: time.Hour}, RetryEvery: config.Duration{Duration: 30 * time.Minute}}
	for _, c := range []struct {
		name  string
		setBy string
		want  bool
	}{
		{"a handoff chose it", state.SetByHandoff, true},
		{"a new conversation chose it", state.SetByNewThread, true},
		{"state from before the record", "", true},
		{"timing chose it, after an earlier handoff and switch-back", state.SetByTiming, false},
	} {
		w := newWorld(t, true, true)
		w.cfg.Loop.SwitchBack = sb
		tk := &tick{loop: w.cfg.Loop, now: t0, st: state.File{ActiveTarget: "example-large", TargetSetBy: c.setBy, LastHandoffAt: t0.Add(-2 * time.Hour)}}
		if got := tk.switchBackReady("example-large"); got != c.want {
			t.Errorf("%s: ready %v, want %v", c.name, got, c.want)
		}
	}
}

// A dry run says what timing would do and changes nothing.
func TestTimingDryRunChangesNothing(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	opts := w.opts(t0)
	opts.DryRun = true
	res, err := Tick(t.Context(), w.cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Timing == nil || res.Timing.Outcome != "dry_run" || res.Timing.Action != "up" {
		t.Fatalf("%+v", res.Timing)
	}
	if large := w.adapters["example-large"]; large != nil && large.activated > 0 {
		t.Fatal("a dry run stepped")
	}
	if st := loadState(t, w); st.Target() != state.Primary || st.Timing != nil {
		t.Fatalf("%+v", st)
	}
}

// A loop that is held, stopped or off is never touched: a step recorded and
// not settled waits for a check that acts.
func TestTimingRepairWaitsForACheckThatActs(t *testing.T) {
	for name, set := range map[string]func(*control.File){
		"paused":        func(f *control.File) { f.Paused = true },
		"disabled":      func(f *control.File) { f.Enabled = false },
		"awaiting user": func(f *control.File) { f.AwaitingUser = true },
		"stopped":       func(f *control.File) { f.Stop = true },
	} {
		w := stepsWorld(t)
		p := w.primary(decide.Observation{State: decide.Idle})
		editState(t, w, func(s *state.File) {
			s.Timing = &state.TimingRecord{Pending: &state.TimingMove{At: t0.Add(-10 * time.Minute), From: state.Primary, To: "example-large", Action: "up"}}
		})
		control.Update(w.dir, set)
		runTick(t, w, t0)
		if large := w.adapters["example-large"]; p.activated != 0 || (large != nil && large.activated != 0) {
			t.Errorf("%s: the thread was changed", name)
		}
		if st := loadState(t, w); st.Timing == nil || st.Timing.Pending == nil {
			t.Errorf("%s: the record of the step was dropped", name)
		}
	}
}

// The step is on disk before the thread is changed.
func TestTimingStepRecordedBeforeTheThreadChanges(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	large := &fakeAdapter{name: "example-large"}
	w.adapters["example-large"] = large
	seen := false
	large.onActivate = func() {
		st := loadState(t, w)
		seen = st.Timing != nil && st.Timing.Pending != nil && st.Timing.Pending.To == "example-large"
	}
	runTick(t, w, t0)
	if !seen || large.activated != 1 {
		t.Fatalf("the step was not on disk when the thread changed (activated %d)", large.activated)
	}
}

// Leaving a step through configuration: timing turned off, the step dropped
// from steps, or its fallback removed. In each the thread is set back to
// primary's model and effort, recorded as a configuration move.
func TestTimingLeftConfigurationReturnsToPrimary(t *testing.T) {
	onStep := func(t *testing.T, w *world, setBy string) {
		editState(t, w, func(s *state.File) {
			s.ActiveTarget, s.TargetSetBy = "example-large", setBy
			s.Timing = &state.TimingRecord{LastMove: &state.TimingMove{At: t0.Add(-3 * time.Hour), From: state.Primary, To: "example-large", Action: "up", Window: "seven_day", ResetsAt: t0.Add(3 * time.Hour)}}
		})
		w.adapters["example-large"] = &fakeAdapter{name: "example-large", obs: decide.Observation{State: decide.Idle}}
	}
	for name, c := range map[string]struct {
		setBy string
		edit  func(*world)
	}{
		"timing turned off":              {state.SetByTiming, func(w *world) { w.cfg.Loop.Timing.Enabled = false }},
		"timing removed":                 {state.SetByTiming, func(w *world) { w.cfg.Loop.Timing = nil }},
		"step dropped from steps":        {state.SetByTiming, func(w *world) { w.cfg.Loop.Timing.Steps = []string{"primary"}; w.cfg.Loop.Timing.Ceiling = "primary" }},
		"step's fallback removed":        {state.SetByTiming, func(w *world) { w.cfg.FallbackSet.Candidates = w.cfg.FallbackSet.Candidates[:3] }},
		"a same-thread fallback removed": {state.SetByHandoff, func(w *world) { w.cfg.FallbackSet.Candidates = w.cfg.FallbackSet.Candidates[:3] }},
	} {
		w := stepsWorld(t)
		p := w.primary(decide.Observation{State: decide.Idle})
		onStep(t, w, c.setBy)
		c.edit(w)
		runTick(t, w, t0)
		st := loadState(t, w)
		if p.activated != 1 || st.Target() != state.Primary || st.TargetSetBy != state.SetByConfiguration || st.ReapplyRoute {
			t.Errorf("%s: activated %d, %s by %s, reapply %v", name, p.activated, st.Target(), st.TargetSetBy, st.ReapplyRoute)
		}
	}
}

// A check that moved the loop because its route left the configuration made
// another move: timing does not step in the same check.
func TestTimingConfigurationMoveIsAnotherMove(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	editState(t, w, func(s *state.File) { s.ActiveTarget, s.TargetSetBy = "deleted-candidate", state.SetByHandoff })
	res := runTick(t, w, t0)
	if large := w.adapters["example-large"]; (large != nil && large.activated > 0) || res.Timing == nil || res.Timing.Reason != "other_move" {
		t.Fatalf("%+v", res.Timing)
	}
}

// A retarget made with no reset known, here while timing was off: the first
// reset a reading shows after it is saved, and never moved again.
func TestTimingRetargetWaitIsSavedOnce(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	w.adapters["example-large"] = &fakeAdapter{name: "example-large", obs: decide.Observation{State: decide.Idle}}
	editState(t, w, func(s *state.File) { s.ActiveTarget, s.TargetSetBy = "example-large", state.SetByRetarget })
	nearReset(t, w, t0) // read 20 minutes before the check: before the wait starts
	runTick(t, w, t0)
	st := loadState(t, w)
	if st.Timing == nil || st.Timing.Retarget == nil || !st.Timing.Retarget.At.Equal(t0) || st.Timing.Retarget.ResetsAt != nil {
		t.Fatalf("first check: %+v", st.Timing)
	}
	first := nearReset(t, w, t0.Add(time.Hour)) // read after the wait started
	runTick(t, w, t0.Add(time.Hour))
	st = loadState(t, w)
	if r := st.Timing.Retarget; r.ResetsAt == nil || !r.ResetsAt.Equal(first) {
		t.Fatalf("second check: %+v", r)
	}
	nearReset(t, w, t0.Add(30*time.Hour)) // a later reading of a later instance
	runTick(t, w, t0.Add(2*time.Hour))
	if r := loadState(t, w).Timing.Retarget; !r.ResetsAt.Equal(first) {
		t.Fatalf("the saved reset moved: %+v", r)
	}
}

// A handoff from a step records the reset the provider reported: the step is
// on the first route's allowance, so switch-back waits for it.
func TestTimingHandoffFromAStepRecordsTheReset(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	runTick(t, w, t0)
	resets := t0.Add(5 * time.Hour)
	w.adapters["example-large"].obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, ResetsAt: resets}
	runTick(t, w, t0.Add(10*time.Minute))
	if st := loadState(t, w); st.TargetSetBy != state.SetByHandoff || !st.PrimaryResetsAt.Equal(resets) {
		t.Fatalf("%s by %s, primary resets %s", st.Target(), st.TargetSetBy, st.PrimaryResetsAt)
	}
}

// With timing off, a handoff records the first route's reset exactly as
// before timing existed: a handoff from a fallback in the first route's
// group leaves the recorded reset as it was, whether or not the provider
// reported one.
func TestTimingOffHandoffRecordsTheResetAsBefore(t *testing.T) {
	for _, reported := range []bool{true, false} {
		w := newWorld(t, true, true)
		w.cfg.Loop.SwitchBack = config.SwitchBack{Enabled: true, MinAfter: config.Duration{Duration: 30 * time.Minute}, RetryEvery: config.Duration{Duration: 30 * time.Minute}}
		w.primary(decide.Observation{State: decide.Idle})
		r1, r2 := t0.Add(3*time.Hour), t0.Add(5*time.Hour)
		if err := state.Save(w.dir, state.File{ActiveTarget: "same-account", LastHandoffAt: t0.Add(-time.Hour), PrimaryResetsAt: r1}); err != nil {
			t.Fatal(err)
		}
		reg := &health.Registry{Candidates: map[string]*health.Entry{}}
		for _, id := range []string{"claude-other", "codex-main"} {
			reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
		}
		if err := reg.Save(w.dir); err != nil {
			t.Fatal(err)
		}
		w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
		obs := decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}
		if reported {
			obs.ResetsAt = r2
		}
		w.adapters["same-account"] = &fakeAdapter{name: "same-account", obs: obs}
		runTick(t, w, t0)
		st := loadState(t, w)
		if st.Target() == "same-account" || st.TargetSetBy != state.SetByHandoff || !st.PrimaryResetsAt.Equal(r1) {
			t.Fatalf("reported %v: on %s by %s, primary resets %s, want %s as before", reported, st.Target(), st.TargetSetBy, st.PrimaryResetsAt, r1)
		}
	}
}

// One step up per window instance, end to end: up, timing turned off so the
// loop returns, timing on again before the same reset: no second step up.
func TestTimingOnceStepUpPerInstanceEndToEnd(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	resets := t0.Add(5 * time.Hour)
	reading := func(at time.Time) {
		id := probe.UsageIdentity(w.cfg.Loop.PrimaryCandidate())
		seedHealth(t, w, func(e *health.Entry) {
			e.Usage = &probe.Usage{At: at, Source: probe.UsageSourceClaude, Identity: id, Windows: []probe.UsageWindow{{Key: "seven_day", Used: 0.6, ResetsAt: &resets}}}
		})
	}
	w.cfg.Loop.Timing.NearReset = config.Duration{Duration: 6 * time.Hour}
	reading(t0.Add(-10 * time.Minute))
	runTick(t, w, t0)
	if st := loadState(t, w); st.Target() != "example-large" || st.Timing.LastUp == nil {
		t.Fatalf("no step up: %+v", st.Timing)
	}
	w.adapters["example-large"].obs = decide.Observation{State: decide.Idle}
	w.cfg.Loop.Timing.Enabled = false
	runTick(t, w, t0.Add(61*time.Minute))
	if st := loadState(t, w); st.Target() != state.Primary || st.Timing.LastUp == nil {
		t.Fatalf("no return, or the step up forgotten: %+v", st.Timing)
	}
	w.cfg.Loop.Timing.Enabled = true
	at := t0.Add(3*time.Hour + 5*time.Minute)
	reading(at.Add(-10 * time.Minute))
	before := w.adapters["example-large"].activated
	res := runTick(t, w, at)
	if res.Timing == nil || res.Timing.Reason != "already_stepped_up" || w.adapters["example-large"].activated != before {
		t.Fatalf("second step up for the same instance: %+v", res.Timing)
	}
}

// A step up that keeps failing while each reading reports the same reset a
// second later (within the tolerance for one window instance) is capped
// after max_reprompts failures and reported once.
func TestTimingStepUpCapWithResetJitter(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	w.adapters["example-large"] = &fakeAdapter{name: "example-large", activateErr: errors.New("fictional bb failure")}
	resets := t0.Add(2 * time.Hour)
	id := probe.UsageIdentity(w.cfg.Loop.PrimaryCandidate())
	for i := 0; i < 5; i++ {
		now := t0.Add(time.Duration(i) * 10 * time.Minute)
		r := resets.Add(time.Duration(i) * time.Second)
		seedHealth(t, w, func(e *health.Entry) {
			e.Usage = &probe.Usage{At: now.Add(-time.Minute), Source: probe.UsageSourceClaude, Identity: id, Windows: []probe.UsageWindow{{Key: "seven_day", Used: 0.6, ResetsAt: &r}}}
		})
		runTick(t, w, now)
	}
	st := loadState(t, w)
	if st.Timing.GaveUp == nil || countReports(w, "timing") != 1 || st.Timing.Failures != 2 {
		t.Fatalf("gave up %+v, failures %d, reports %s", st.Timing.GaveUp, st.Timing.Failures, w.reportTypes())
	}
}

// A step up given up stays given up for its window instance whatever moves
// are made in between.
func TestTimingGaveUpKeptAcrossOtherMoves(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	w.adapters["example-large"] = &fakeAdapter{name: "example-large", obs: decide.Observation{State: decide.Idle}}
	gave := &state.TimingMove{To: "example-large", Action: "up", Window: "seven_day", ResetsAt: t0.Add(30 * time.Hour)}
	editState(t, w, func(s *state.File) {
		s.ActiveTarget, s.TargetSetBy = "example-large", state.SetByTiming
		s.Timing = &state.TimingRecord{GaveUp: gave, Capped: true, Failures: 2, FailedTo: "example-large", FailedAction: "up",
			LastMove: &state.TimingMove{At: t0.Add(-3 * time.Hour), From: state.Primary, To: "example-large", Action: "up", Window: "seven_day", ResetsAt: t0.Add(-time.Minute)}}
	})
	runTick(t, w, t0) // returns at the reset
	st := loadState(t, w)
	if st.Target() != state.Primary || st.Timing.GaveUp == nil || !st.Timing.GaveUp.ResetsAt.Equal(gave.ResetsAt) {
		t.Fatalf("the return erased the step up given up: %+v", st.Timing)
	}
}

// A wake the engine sends only to tell the session a child failed is not a
// turn boundary for timing.
func TestTimingNotOnAChildErrorWake(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle, ActiveChildren: 1, ErroredChildren: []string{"child:example"}})
	nearReset(t, w, t0)
	res := runTick(t, w, t0)
	if !res.ChildErrorNote {
		t.Fatalf("not a child-error wake: %s %q", res.Action, res.Reason)
	}
	if large := w.adapters["example-large"]; large != nil && large.activated > 0 {
		t.Fatalf("stepped on a child-error wake: %+v", res.Timing)
	}
}

// The repair runs only when its 30 seconds are free, and never on a dry run.
func TestTimingRepairBudgetAndDryRun(t *testing.T) {
	pending := func(t *testing.T, w *world) {
		editState(t, w, func(s *state.File) {
			s.Timing = &state.TimingRecord{Pending: &state.TimingMove{At: t0.Add(-10 * time.Minute), From: state.Primary, To: "example-large", Action: "up"}}
			s.ReapplyRoute = true
		})
	}
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	pending(t, w)
	opts := w.opts(t0)
	opts.DryRun = true
	if _, err := Tick(t.Context(), w.cfg, opts); err != nil {
		t.Fatal(err)
	}
	if st := loadState(t, w); p.activated != 0 || st.Timing.Pending == nil || !st.ReapplyRoute {
		t.Fatalf("dry run: activated %d, %+v", p.activated, st)
	}

	w = stepsWorld(t)
	p = w.primary(decide.Observation{State: decide.Idle})
	pending(t, w)
	w.cfg.Loop.TickTimeout = config.Duration{Duration: time.Second}
	res := runTick(t, w, t0)
	if st := loadState(t, w); p.activated != 0 || st.Timing.Pending == nil || !strings.Contains(strings.Join(res.Warnings, "\n"), "no time left in this check to set it") {
		t.Fatalf("no time: activated %d, %+v, %v", p.activated, st.Timing, res.Warnings)
	}
}

// The history gives a check in which timing changed the thread its own
// cause, with a timing evaluation carrying the decision's stable rule and
// reason codes, beside the engine's own evaluation.
func TestTimingCausesInTheHistory(t *testing.T) {
	timingEval := func(r incident.Record) *incident.Evaluation {
		for i := range r.Decisions.Evaluations {
			if strings.HasPrefix(r.Decisions.Evaluations[i].Stage, "timing_") {
				return &r.Decisions.Evaluations[i]
			}
		}
		return nil
	}
	// A step up.
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	runTick(t, w, t0)
	r := lastIncident(t, w)
	e := timingEval(r)
	if r.Cause != incident.TimingStepUp || r.Action != "wake" || r.TargetKeyAfter != incident.TargetKey("example-large") ||
		e == nil || e.Stage != "timing_step_up" || e.Action != incident.TimingStepUp || e.Cause != "near_reset_unused" {
		t.Fatalf("step up: cause %s action %s, %+v", r.Cause, r.Action, e)
	}
	engine := false
	for _, x := range r.Decisions.Evaluations {
		engine = engine || (x.Stage == "normal" && x.Action == "wake" && x.Cause == "idle")
	}
	if !engine {
		t.Fatalf("the engine's own decision is missing: %+v", r.Decisions.Evaluations)
	}
	// The return at the reset.
	w.adapters["example-large"].obs = decide.Observation{State: decide.Idle}
	editState(t, w, func(s *state.File) { s.Timing.LastMove.ResetsAt = t0.Add(5 * time.Minute) })
	runTick(t, w, t0.Add(10*time.Minute))
	r = lastIncident(t, w)
	if e = timingEval(r); r.Cause != incident.TimingReturn || e == nil || e.Stage != "timing_return_at_reset" || e.Cause != "reset_passed" {
		t.Fatalf("return: cause %s, %+v", r.Cause, e)
	}
	// A repair: kept in the condition tier, never folded into a run of
	// routine checks.
	w = stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	editState(t, w, func(s *state.File) {
		s.Timing = &state.TimingRecord{Pending: &state.TimingMove{At: t0.Add(-10 * time.Minute), From: state.Primary, To: "example-large", Action: "up"}}
	})
	runTick(t, w, t0)
	r = lastIncident(t, w)
	if e = timingEval(r); r.Cause != incident.TimingRepair || r.Tier != incident.TierCondition || e == nil || e.Stage != "timing_repair" || e.Cause != "unsettled_step" {
		t.Fatalf("repair: cause %s tier %s, %+v", r.Cause, r.Tier, e)
	}
}

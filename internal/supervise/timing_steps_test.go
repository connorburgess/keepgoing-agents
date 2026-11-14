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
	if w.reports[0].Type != "timing" || w.reports[0].Text != "example-loop: timing stepped up to example-large: the seven_day allowance resets in 2h 0m with 40% unused (reading 20m old)" ||
		w.reports[1].Type != "wake" || w.reports[1].Text != "example-loop: wake example-large (idle)" {
		t.Fatalf("report: %+v", w.reports)
	}
}

// Every gate the check can see keeps the thread as it is.
func TestTimingStepGates(t *testing.T) {
	for name, set := range map[string]func(*testing.T, *world){
		"paused": func(t *testing.T, w *world) {
			control.Update(w.dir, func(f *control.File) { f.Paused = true })
		},
		"a turn running": func(t *testing.T, w *world) { w.adapters["primary"].obs = decide.Observation{State: decide.Active} },
		"in error": func(t *testing.T, w *world) {
			w.adapters["primary"].obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
		},
		"unknown": func(t *testing.T, w *world) { w.adapters["primary"].obs = decide.Observation{State: decide.Unknown} },
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
			t.Errorf("%s: stepped (%+v)", name, res.Timing)
		}
		if st := loadState(t, w); st.Target() != state.Primary || countReports(w, "timing") != 0 {
			t.Errorf("%s: %s, reports %s", name, st.Target(), w.reportTypes())
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

// A failed activation keeps the loop where it is, the wake still goes out
// there, and after max_reprompts failures for the step and window instance
// timing stops trying and reports once.
func TestTimingStepFailureAndCap(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	w.adapters["example-large"] = &fakeAdapter{name: "example-large", activateErr: errors.New("fictional bb failure")}
	res := runTick(t, w, t0)
	if res.Timing.Outcome != "failed" || len(p.sent) != 1 || res.Target != state.Primary {
		t.Fatalf("first failure: %+v, sent %v", res.Timing, p.sent)
	}
	res = runTick(t, w, t0.Add(10*time.Minute))
	if res.Timing.Outcome != "capped" || countReports(w, "timing") != 1 {
		t.Fatalf("second failure: %+v, reports %s", res.Timing, w.reportTypes())
	}
	res = runTick(t, w, t0.Add(20*time.Minute))
	st := loadState(t, w)
	if res.Timing.Outcome != "capped" || countReports(w, "timing") != 1 || st.Timing.Failures != 2 || !st.Timing.Capped || st.Target() != state.Primary {
		t.Fatalf("after the cap: %+v %+v, reports %s", res.Timing, st.Timing, w.reportTypes())
	}
}

// A step recorded and not settled (the check ended between recording it and
// settling it) is settled by the next check: the route on record is applied
// again, then timing decides afresh.
func TestTimingUnsettledIntentIsSettled(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Active})
	editState(t, w, func(s *state.File) {
		s.Timing = &state.TimingRecord{Pending: &state.TimingMove{At: t0.Add(-10 * time.Minute), From: state.Primary, To: "example-large", Action: "up"}}
	})
	res := runTick(t, w, t0)
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

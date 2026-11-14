package supervise

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A hold arriving after observation or during adapter construction must also
// prevent repairing an unsettled step. The saved intent survives for later.
func TestTimingRepairRechecksControls(t *testing.T) {
	for name, hold := range map[string]func(*control.File){
		"stop":          func(c *control.File) { c.Stop = true },
		"pause":         func(c *control.File) { c.Paused = true },
		"disabled":      func(c *control.File) { c.Enabled = false },
		"awaiting user": func(c *control.File) { c.AwaitingUser = true },
		"completed":     func(c *control.File) { c.Completed = true },
	} {
		for _, late := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/observation", true: "/construction"}[late], func(t *testing.T) {
				w := stepsWorld(t)
				p := w.primary(decide.Observation{State: decide.Idle})
				// No step is due; only the repair may change the thread.
				w.cfg.Loop.Timing.Enabled = false
				editState(t, w, func(s *state.File) {
					s.Timing = &state.TimingRecord{Pending: &state.TimingMove{From: "primary", To: "example-large"}}
				})
				setHold := func() {
					if _, err := control.Update(w.dir, hold); err != nil {
						t.Fatal(err)
					}
				}
				opts := w.opts(t0)
				if late {
					build := opts.NewAdapter
					calls := 0
					opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
						calls++
						if calls == 2 {
							setHold()
						}
						return build(a, dir, target)
					}
				} else {
					p.onObserve = setHold
				}
				if _, err := Tick(t.Context(), w.cfg, opts); err != nil {
					t.Fatal(err)
				}
				if p.activated != 0 || loadState(t, w).Timing.Pending == nil || countReports(w, "timing") != 0 {
					t.Fatalf("repair ignored hold: activations=%d state=%+v", p.activated, loadState(t, w).Timing)
				}
			})
		}
	}
}

// A followed list cannot grant timing authority while its references are
// unavailable. Restoring valid resolved inputs permits the ordinary step.
func TestTimingUnavailableRoutesWithholdAction(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		t.Run(map[bool]string{false: "unresolved", true: "unreadable"}[unreadable], func(t *testing.T) {
			w := stepsWorld(t)
			w.primary(decide.Observation{State: decide.Idle})
			nearReset(t, w, t0)
			w.cfg.RouteList = config.RouteStatus{Source: config.RoutesStandard, Unreadable: unreadable}
			w.cfg.TimingStepsUnavailable = "referenced step unavailable"
			res := runTick(t, w, t0)
			if a := w.adapters["example-large"]; a != nil && a.activated > 0 {
				t.Fatal("unavailable inputs activated step")
			}
			if res.Timing != nil || loadState(t, w).Target() != state.Primary {
				t.Fatalf("unavailable inputs granted authority: %+v", res)
			}
			w.cfg.RouteList = config.RouteStatus{Source: "own"}
			w.cfg.TimingStepsUnavailable = ""
			later := t0.Add(2 * time.Hour)
			nearReset(t, w, later)
			res = runTick(t, w, later)
			if res.Timing == nil || res.Timing.Outcome != "applied" || loadState(t, w).Target() != "example-large" {
				t.Fatalf("restored inputs: %+v", res.Timing)
			}
		})
	}
}

func TestTimingRepairWithUnreadableControls(t *testing.T) {
	w := stepsWorld(t)
	p := w.primary(decide.Observation{State: decide.Idle})
	editState(t, w, func(s *state.File) {
		s.Timing = &state.TimingRecord{Pending: &state.TimingMove{From: "primary", To: "example-large"}}
	})
	p.onObserve = func() {
		if err := os.WriteFile(filepath.Join(w.dir, "control.json"), []byte("broken"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	Tick(t.Context(), w.cfg, w.opts(t0))
	if p.activated != 0 || loadState(t, w).Timing.Pending == nil {
		t.Fatal("unreadable controls permitted repair")
	}
}

func TestTimingStepRechecksControlsAfterIntent(t *testing.T) {
	w := stepsWorld(t)
	w.primary(decide.Observation{State: decide.Idle})
	nearReset(t, w, t0)
	opts := w.opts(t0)
	build := opts.NewAdapter
	opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
		if target == "example-large" {
			if _, err := control.Update(w.dir, func(c *control.File) { c.Paused = true }); err != nil {
				t.Fatal(err)
			}
		}
		return build(a, dir, target)
	}
	res, err := Tick(t.Context(), w.cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	st := loadState(t, w)
	if res.Timing == nil || res.Timing.Outcome != "held" || st.Timing == nil || st.Timing.Pending == nil || st.Target() != state.Primary || w.adapters["example-large"].activated != 0 {
		t.Fatalf("hold after intent ignored: %+v %+v", res.Timing, st.Timing)
	}
}

func TestTimingRepairRespectsIdleUncertainty(t *testing.T) {
	for name, edit := range map[string]func(*decide.Observation){
		"child input":         func(o *decide.Observation) { o.AwaitingChildren = []string{"thr_example_child"} },
		"children unverified": func(o *decide.Observation) { o.ChildrenUnverified = true },
		"retry unverified":    func(o *decide.Observation) { o.RetryUnverified = true },
		"provider retrying":   func(o *decide.Observation) { o.Retrying = true },
		"turn unverified":     func(o *decide.Observation) { o.TurnUnknown = true },
		"target gone":         func(o *decide.Observation) { o.Gone = true },
	} {
		t.Run(name, func(t *testing.T) {
			w := stepsWorld(t)
			obs := decide.Observation{State: decide.Idle}
			edit(&obs)
			p := w.primary(obs)
			editState(t, w, func(s *state.File) {
				s.Timing = &state.TimingRecord{Pending: &state.TimingMove{From: "primary", To: "example-large"}}
			})
			res := runTick(t, w, t0)
			st := loadState(t, w)
			if p.activated != 0 || len(p.sent) != 0 || st.Timing.Pending == nil {
				t.Fatalf("uncertainty activated or sent: %+v activations=%d", res, p.activated)
			}
			if name == "child input" {
				c, _ := control.Load(w.dir)
				if res.Action != "await_user" || !c.AwaitingUser {
					t.Fatal("child input lost precedence")
				}
			}
		})
	}
}

func TestTimingPendingRepairWithholdsWake(t *testing.T) {
	for _, why := range []string{"failed", "unavailable", "unreadable", "budget"} {
		t.Run(why, func(t *testing.T) {
			w := stepsWorld(t)
			p := w.primary(decide.Observation{State: decide.Idle})
			w.cfg.Loop.Timing.Enabled = false
			w.cfg.FallbackSet = nil
			editState(t, w, func(s *state.File) {
				s.ReapplyRoute = true
				s.Timing = &state.TimingRecord{Pending: &state.TimingMove{From: "primary", To: "example-large"}}
			})
			switch why {
			case "failed":
				p.activateErr = errors.New("fictional lost repair response")
			case "unavailable":
				w.cfg.TimingStepsUnavailable = "referenced step unavailable"
			case "unreadable":
				w.cfg.RouteList.Unreadable = true
			case "budget":
				w.cfg.Loop.TickTimeout = config.Duration{Duration: time.Second}
			}
			res := runTick(t, w, t0)
			st := loadState(t, w)
			if res.Action != "none" || len(p.sent) != 0 || st.Timing.Pending == nil || !st.ReapplyRoute || st.SendsRecorded != 0 {
				t.Fatalf("unestablished route released wake: %+v state=%+v reapply=%v sends=%d sent=%v", res, st.Timing, st.ReapplyRoute, st.SendsRecorded, p.sent)
			}
			p.activateErr = nil
			w.cfg.TimingStepsUnavailable = ""
			w.cfg.RouteList.Unreadable = false
			w.cfg.Loop.TickTimeout = config.Duration{Duration: config.DefaultTickTimeout}
			res = runTick(t, w, t0.Add(10*time.Minute))
			st = loadState(t, w)
			if res.Action != "wake" || len(p.sent) != 1 || st.Timing.Pending != nil || st.ReapplyRoute || st.SendsRecorded != 1 {
				t.Fatalf("successful repair did not release one wake: %+v state=%+v", res, st.Timing)
			}
		})
	}
}

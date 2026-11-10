package supervise

import (
	"context"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestActiveChildAcrossStallIntervalsDoesNotSpendBudgets(t *testing.T) {
	for _, parent := range []decide.State{decide.Idle, decide.Active, decide.Error} {
		t.Run(string(parent), func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.Progress.MaxNudges = 2
			w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
			p := w.primary(decide.Observation{State: parent, ActiveChildren: 1})
			run := func(at time.Time) Result {
				opts := w.opts(at)
				opts.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
				res, err := Tick(context.Background(), w.cfg, opts)
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			for i := 0; i < 6; i++ {
				res := run(t0.Add(time.Duration(i) * time.Hour))
				st, err := state.Load(w.dir)
				if err != nil || res.Action != "none" || res.Reason != "child work running" || st.Nudges != 0 || st.Reprompts != 0 || !st.LastWakeAt.IsZero() || len(p.sent) != 0 {
					t.Fatalf("interval %d: %+v %+v %v", i, res, st, err)
				}
				ctl, err := control.Load(w.dir)
				if err != nil || ctl.AwaitingUser {
					t.Fatal("child wait escalated the parent")
				}
			}
			p.obs = decide.Observation{State: decide.Idle}
			if res := run(t0.Add(5*time.Hour + time.Second)); res.Action != "wake" || len(p.sent) != 1 {
				t.Fatalf("idle parent not woken next check: %+v", res)
			}
		})
	}
}

func TestChildInputPersistsHoldWithoutProbesOrParentActions(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "dry_run"}[dry], func(t *testing.T) {
			w := newWorld(t, true, true)
			p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, ActiveChildren: 1, AwaitingInput: true, AwaitingChildren: []string{"thr_child", "thr_second"}})
			w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
			opts := w.opts(t0)
			opts.DryRun = dry
			opts.Fingerprint = func([]string) (string, error) { t.Fatal("fingerprint must not block child escalation"); return "", nil }
			res, err := Tick(context.Background(), w.cfg, opts)
			if err != nil || res.Action != "await_user" {
				t.Fatalf("%+v %v", res, err)
			}
			ctl, _ := control.Load(w.dir)
			if ctl.AwaitingUser == dry {
				t.Fatalf("dry=%v control=%+v", dry, ctl)
			}
			if len(p.sent) != 0 || p.stopped != 0 || p.activated != 0 || len(w.probes) > 0 {
				t.Fatal("child input touched provider or ran probes")
			}
			if dry {
				if len(w.reports) != 0 {
					t.Fatal("dry run reported")
				}
				return
			}
			if len(w.reports) != 1 || w.reports[0].Type != "await_user" || w.reports[0].Data["pendingInteraction"] != true {
				t.Fatalf("%+v", w.reports)
			}
			children := w.reports[0].Data["children"].([]string)
			if len(children) != 2 || children[0] != "thr_child" || children[1] != "thr_second" {
				t.Fatalf("%v", children)
			}
			// A new tick has no dependency on the old adapter's pending observation.
			p.obs = decide.Observation{State: decide.Idle}
			if r := runTick(t, w, t0.Add(time.Hour)); r.Action != "none" || r.Reason != "awaiting user" || len(w.reports) != 1 || len(p.sent) != 0 {
				t.Fatalf("hold replayed: %+v", r)
			}
			control.Update(w.dir, func(c *control.File) { c.AwaitingUser = false })
			w.cfg.Loop.Progress.Repositories = nil
			if r := runTick(t, w, t0.Add(2*time.Hour)); r.Action != "wake" || len(p.sent) != 1 {
				t.Fatalf("explicit resume failed: %+v", r)
			}
		})
	}
}

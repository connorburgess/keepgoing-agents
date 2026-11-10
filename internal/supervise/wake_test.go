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

type wakeAdapter struct {
	fakeAdapter
	polls  int
	signal bool
}

func (a *wakeAdapter) PollWake(context.Context) bool { a.polls++; return a.signal }

func TestPollWakeGatesAndEligibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctl  control.File
		obs  decide.Observation
		want bool
	}{
		{"idle", control.File{Enabled: true}, decide.Observation{State: decide.Idle}, true},
		{"disabled", control.File{}, decide.Observation{State: decide.Idle}, false},
		{"paused", control.File{Enabled: true, Paused: true}, decide.Observation{State: decide.Idle}, false},
		{"stopped", control.File{Enabled: true, Stop: true}, decide.Observation{State: decide.Idle}, false},
		{"completed", control.File{Enabled: true, Completed: true}, decide.Observation{State: decide.Idle}, false},
		{"await_user", control.File{Enabled: true, AwaitingUser: true}, decide.Observation{State: decide.Idle}, false},
		{"active", control.File{Enabled: true}, decide.Observation{State: decide.Active}, false},
		{"error", control.File{Enabled: true}, decide.Observation{State: decide.Error}, false},
		{"unknown", control.File{Enabled: true}, decide.Observation{State: decide.Unknown}, false},
		{"gone", control.File{Enabled: true}, decide.Observation{State: decide.Idle, Gone: true}, false},
		{"child_input", control.File{Enabled: true}, decide.Observation{State: decide.Idle, AwaitingChildren: []string{"thr_child"}}, true},
		{"pending_input", control.File{Enabled: true}, decide.Observation{State: decide.Idle, AwaitingInput: true}, false},
		{"queued", control.File{Enabled: true}, decide.Observation{State: decide.Idle, Queued: 1}, false},
		{"background", control.File{Enabled: true}, decide.Observation{State: decide.Idle, Background: 1}, false},
		{"child_active", control.File{Enabled: true}, decide.Observation{State: decide.Idle, ActiveChildren: 1}, false},
		{"child_unknown", control.File{Enabled: true}, decide.Observation{State: decide.Idle, ChildrenUnverified: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			control.Save(w.dir, tc.ctl)
			a := &wakeAdapter{fakeAdapter: fakeAdapter{obs: tc.obs}, signal: true}
			made := 0
			makeAdapter := func(config.Agent, string, string) (adapter.Adapter, error) { made++; return a, nil }
			if got := PollWake(context.Background(), w.cfg, makeAdapter); got != tc.want {
				t.Fatalf("got %v", got)
			}
			held := !tc.ctl.Enabled || tc.ctl.Paused || tc.ctl.Stop || tc.ctl.Completed || tc.ctl.AwaitingUser
			if held && (made != 0 || a.polls != 0) {
				t.Fatal("held loop queried adapter")
			}
			if len(a.sent) != 0 || a.stopped != 0 || a.activated != 0 {
				t.Fatal("poll mutated provider")
			}
		})
	}
}

func TestPollWakeUsesCurrentTargetAndHandlesFailures(t *testing.T) {
	for _, which := range []string{"fallback", "removed", "no_signal", "observe_failure", "factory_failure", "unsupported", "cancel"} {
		t.Run(which, func(t *testing.T) {
			w := newWorld(t, true, true)
			target := "primary"
			if which == "fallback" {
				target = "codex-main"
			}
			if which == "removed" {
				target = "missing"
			}
			state.Save(w.dir, state.File{ActiveTarget: target})
			a := &wakeAdapter{fakeAdapter: fakeAdapter{obs: decide.Observation{State: decide.Idle}}, signal: true}
			if which == "no_signal" {
				a.signal = false
			}
			if which == "observe_failure" {
				a.obsErr = errors.New("unavailable")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if which == "cancel" {
				cancel()
			}
			called := false
			makeAdapter := func(agent config.Agent, dir, id string) (adapter.Adapter, error) {
				called = true
				if id != target || dir != w.dir {
					t.Fatal("wrong target")
				}
				c, _ := w.cfg.FallbackSet.Find(target)
				if target == "codex-main" && agent.Describe() != c.Agent.Describe() {
					t.Fatal("wrong fallback definition")
				}
				if which == "factory_failure" {
					return nil, errors.New("unavailable")
				}
				if which == "unsupported" {
					return &a.fakeAdapter, nil
				}
				return a, nil
			}
			if got := PollWake(ctx, w.cfg, makeAdapter); got != (which == "fallback") {
				t.Fatalf("got %v", got)
			}
			if which == "removed" && called {
				t.Fatal("removed target queried")
			}
		})
	}
}

// Consume exactly one notification, then cancel inside observation. This models
// a control-poll budget expiring after the cursor was committed without relying
// on machine speed, sleeps, or subprocess scheduling.
type interruptedWakeAdapter struct {
	fakeAdapter
	cancel       context.CancelFunc
	pending      bool
	observations int
}

func (a *interruptedWakeAdapter) PollWake(context.Context) bool {
	pending := a.pending
	a.pending = false
	return pending
}

func (a *interruptedWakeAdapter) Observe(context.Context) (decide.Observation, error) {
	a.observations++
	a.cancel()
	return a.obs, nil
}

func TestPollWakeCancellationAfterConsumptionDefersToOrdinaryTick(t *testing.T) {
	w := newWorld(t, true, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &interruptedWakeAdapter{fakeAdapter: fakeAdapter{obs: decide.Observation{State: decide.Idle, AwaitingChildren: []string{"thr_child"}}}, cancel: cancel, pending: true}
	makeAdapter := func(config.Agent, string, string) (adapter.Adapter, error) { return a, nil }
	if PollWake(ctx, w.cfg, makeAdapter) || ctx.Err() == nil || a.observations != 1 || a.pending {
		t.Fatal("interrupted poll must consume the notice without authorizing an early check")
	}
	if PollWake(context.Background(), w.cfg, makeAdapter) || a.observations != 1 {
		t.Fatal("consumed notification replayed")
	}
	ctl, err := control.Load(w.dir)
	if err != nil || !ctl.Enabled || ctl.AwaitingUser || len(a.sent) != 0 || a.stopped != 0 || a.activated != 0 {
		t.Fatal("interrupted poll changed controls or provider")
	}
	// The ordinary tick still observes pending child input and persists its hold.
	w.adapters[state.Primary] = &a.fakeAdapter
	result, err := Tick(context.Background(), w.cfg, w.opts(time.Now()))
	if err != nil || result.Action != "await_user" {
		t.Fatalf("ordinary tick lost escalation: %+v", result)
	}
	ctl, err = control.Load(w.dir)
	if err != nil || !ctl.AwaitingUser || len(a.sent) != 0 || a.stopped != 0 || a.activated != 0 || len(w.probes) != 0 {
		t.Fatal("ordinary tick did not hold without sending or probing")
	}
}

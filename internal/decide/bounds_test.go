package decide

import (
	"strings"
	"testing"
	"time"
)

// sends reports whether an action makes the command layer send to a session.
func sends(a Action) bool {
	return a == Wake || a == Nudge || a == Reprompt || a == Handoff || a == SwitchBack
}

// boundInputs enumerates the inputs that select between Decide's sending rules:
// every session state and error class, each observation gate, the operating
// window, switch-back, failover, and fresh versus exhausted per-episode counters.
// Controls and loop-wide limits are left for the caller to set.
func boundInputs(visit func(Input)) {
	states := []State{Idle, Active, Pending, Error, Unknown, Stopping, State("unrecognised")}
	classes := []ErrorClass{ErrNone, ErrRateLimit, ErrAuth, ErrUnknown}
	failovers := []Failover{{}, {Eligible: true, Available: true}}
	for _, state := range states {
		for _, class := range classes {
			for _, failover := range failovers {
				for bits := 0; bits < 1<<12; bits++ {
					on := func(bit int) bool { return bits&(1<<bit) != 0 }
					count := func(bit int) int {
						if on(bit) {
							return 1
						}
						return 0
					}
					in := Input{
						Obs: Observation{
							State: state, ErrorClass: class,
							Queued: count(0), Background: count(1), Gone: on(2),
							Retrying: on(3), RetryUnverified: on(4), AwaitingInput: on(5),
							ActiveChildren: count(6), ChildrenUnverified: on(7),
						},
						InWindow: on(9), SwitchBackReady: on(10), Failover: failover,
						UnchangedFor: 2 * time.Hour, SinceWake: 2 * time.Hour, SinceLastReprompt: time.Hour,
						StallAfter: 30 * time.Minute, MinWakeInterval: time.Hour, Backoff: 10 * time.Minute,
						MaxNudges: 3, MaxReprompts: 3,
					}
					if on(8) {
						in.Obs.AwaitingChildren = []string{"thr_example_child"}
					}
					if on(11) { // per-episode counters already spent
						in.Nudges, in.Reprompts = in.MaxNudges, in.MaxReprompts
					}
					visit(in)
				}
			}
		}
	}
}

// The enumeration must reach every sending rule, otherwise the limit and hold
// tables below would pass without exercising anything.
func TestBoundInputsReachEverySendingAction(t *testing.T) {
	seen := map[Action]int{}
	boundInputs(func(in Input) {
		in.Control = Control{Enabled: true}
		seen[Decide(in).Action]++
	})
	for _, want := range []Action{Wake, Nudge, Reprompt, Handoff, SwitchBack, AwaitUser, None} {
		if seen[want] == 0 {
			t.Errorf("no enumerated input produces %s", want)
		}
	}
	if seen[Stop] != 0 {
		t.Errorf("stop without a stop request: %d inputs", seen[Stop])
	}
}

// A reached loop-wide limit asks for a person whatever the session reports. No
// observation, window, failover or per-episode counter can turn it into a send.
func TestReachedLoopLimitNeverSends(t *testing.T) {
	limits := []struct {
		name                      string
		deadline, sends, progress bool
		reason                    string
	}{
		{"deadline", true, false, false, "loop.limits.deadline"},
		{"max_sends", false, true, false, "loop.limits.max_sends"},
		{"no_progress_timeout", false, false, true, "loop.limits.no_progress_timeout"},
		{"deadline before max_sends", true, true, false, "loop.limits.deadline"},
		{"deadline before no_progress_timeout", true, false, true, "loop.limits.deadline"},
		{"max_sends before no_progress_timeout", false, true, true, "loop.limits.max_sends"},
		{"all three", true, true, true, "loop.limits.deadline"},
	}
	for _, limit := range limits {
		t.Run(limit.name, func(t *testing.T) {
			failures := 0
			boundInputs(func(in Input) {
				in.Control = Control{Enabled: true}
				in.DeadlineReached, in.SendLimitReached, in.NoProgressReached = limit.deadline, limit.sends, limit.progress
				got := Decide(in)
				if (got.Action != AwaitUser || !strings.Contains(got.Reason, limit.reason)) && failures < 5 {
					failures++
					t.Errorf("%+v: got %s (%s)", in, got.Action, got.Reason)
				}
			})
		})
	}
}

// Stop and the other control holds win over every reached limit and every
// observation: nothing is sent, and no hold is replaced by a limit escalation.
func TestControlHoldsWinOverLimitsAndObservations(t *testing.T) {
	holds := []struct {
		name    string
		control Control
	}{
		{"stop", Control{Enabled: true, Stop: true}},
		{"stop while disabled", Control{Stop: true}},
		{"stop with every other hold", Control{Stop: true, Paused: true, AwaitingUser: true, Completed: true}},
		{"disabled", Control{}},
		{"paused", Control{Enabled: true, Paused: true}},
		{"awaiting user", Control{Enabled: true, AwaitingUser: true}},
		{"completed", Control{Enabled: true, Completed: true}},
	}
	for _, hold := range holds {
		for _, reached := range []bool{false, true} {
			name := hold.name + " without limits"
			if reached {
				name = hold.name + " with every limit reached"
			}
			t.Run(name, func(t *testing.T) {
				failures := 0
				boundInputs(func(in Input) {
					in.Control = hold.control
					in.DeadlineReached, in.SendLimitReached, in.NoProgressReached = reached, reached, reached
					want := None
					if hold.control.Stop && (in.Obs.State == Active || in.Obs.State == Pending) {
						want = Stop
					}
					if got := Decide(in); got.Action != want && failures < 5 {
						failures++
						t.Errorf("%+v: got %s (%s), want %s", in, got.Action, got.Reason, want)
					}
				})
			})
		}
	}
}

// The nudge and re-prompt caps bound one stalled turn or one error episode.
// The command layer resets both counters when a turn ends, so they place no
// bound on work repeated across completed turns: only the loop-wide limits do.
func TestPerEpisodeCapsAreNotLoopWideBounds(t *testing.T) {
	stalled := func(i *Input) { i.Obs.State, i.UnchangedFor = Active, 2*time.Hour }
	errored := func(i *Input) { i.Obs.State, i.Obs.ErrorClass = Error, ErrUnknown }
	cases := []struct {
		name string
		mut  func(*Input)
		want Action
	}{
		{"idle wakes with both caps spent", func(i *Input) { i.Nudges, i.MaxNudges, i.Reprompts = 3, 3, 3 }, Wake},
		{"idle wakes immediately after a wake", func(i *Input) { i.SinceWake = 0 }, Wake},
		{"idle wakes however long Git is unchanged", func(i *Input) { i.UnchangedFor = 30 * 24 * time.Hour }, Wake},
		{"stalled turn at its nudge cap asks a person", func(i *Input) { stalled(i); i.Nudges, i.MaxNudges = 3, 3 }, AwaitUser},
		{"next stalled turn is nudged again after the reset", func(i *Input) { stalled(i); i.Nudges, i.MaxNudges = 0, 3 }, Nudge},
		{"zero nudge cap never asks a person", func(i *Input) { stalled(i); i.Nudges, i.MaxNudges = 1000, 0 }, Nudge},
		{"error episode at its re-prompt cap asks a person", func(i *Input) { errored(i); i.Reprompts = 3 }, AwaitUser},
		{"next error episode is re-prompted again after the reset", func(i *Input) { errored(i); i.Reprompts = 0 }, Reprompt},
		{"send limit holds the idle wake", func(i *Input) { i.SendLimitReached = true }, AwaitUser},
		{"deadline holds the idle wake", func(i *Input) { i.DeadlineReached = true }, AwaitUser},
		{"no-progress timeout holds the idle wake", func(i *Input) { i.NoProgressReached = true }, AwaitUser},
	}
	for _, tc := range cases {
		in := base()
		tc.mut(&in)
		if got := Decide(in); got.Action != tc.want {
			t.Errorf("%s: got %s (%s), want %s", tc.name, got.Action, got.Reason, tc.want)
		}
	}
}

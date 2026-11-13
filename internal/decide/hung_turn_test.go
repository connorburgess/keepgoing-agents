package decide

import (
	"testing"
	"time"
)

// The hung-turn wait, in the engine. It replaces the stall nudge for an
// active session the supervisor has found hung through an outage; it becomes
// a hold after the patience; and controls, loop-wide limits, children, a
// person asked, a provider retry and the network gate all come first.
func TestHungTurnWaitInTheEngine(t *testing.T) {
	hung := func() Input {
		in := base()
		in.Obs = Observation{State: Active}
		in.ProgressTracked, in.StallAfter, in.UnchangedFor = true, 30*time.Minute, 3*time.Hour
		in.MinWakeInterval, in.SinceWake, in.MaxNudges = time.Hour, 3*time.Hour, 3
		in.HungTurn, in.HungTurnFor, in.HungTurnPatience = true, 10*time.Minute, 90*time.Minute
		return in
	}
	for name, tc := range map[string]struct {
		change func(*Input)
		action Action
		reason string
	}{
		"waits instead of nudging":  {func(*Input) {}, None, HungTurnReason},
		"holds after the patience":  {func(in *Input) { in.HungTurnFor = 90 * time.Minute }, AwaitUser, HungTurnHoldReason},
		"not hung: nudged as usual": {func(in *Input) { in.HungTurn, in.HungTurnFor = false, 0 }, Nudge, "no progress for 3h0m0s"},
		"network down":              {func(in *Input) { in.NetworkDown = true }, None, NetworkDownReason},
		"paused":                    {func(in *Input) { in.Control.Paused = true }, None, "paused"},
		"stop":                      {func(in *Input) { in.Control.Stop = true }, Stop, ""},
		"deadline":                  {func(in *Input) { in.DeadlineReached = true }, AwaitUser, ""},
		"children running":          {func(in *Input) { in.Obs.ActiveChildren = 1 }, None, "child work running"},
		"a person is asked":         {func(in *Input) { in.Obs.AwaitingInput = true }, None, "waiting on a person (pending interaction)"},
		"provider retrying":         {func(in *Input) { in.Obs.Retrying = true }, None, "provider is retrying the turn"},
	} {
		in := hung()
		tc.change(&in)
		d := Decide(in)
		if d.Action != tc.action || (tc.reason != "" && d.Reason != tc.reason) {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

package decide

import (
	"testing"
	"time"
)

// The hung-turn wait, in the engine. It replaces the stall nudge for an
// active session the supervisor has found hung through an outage; it becomes
// a hold when the stall rule would have held (stall_after plus a
// min_wake_interval per nudge it has left on the stall clock, and one more
// interval since the last wake), counting nudges already sent; and controls,
// loop-wide limits, children, a person asked, a provider retry and the
// network gate all come first.
func TestHungTurnWaitInTheEngine(t *testing.T) {
	hung := func() Input {
		in := base()
		in.Obs = Observation{State: Active}
		in.ProgressTracked, in.StallAfter, in.UnchangedFor = true, 30*time.Minute, 3*time.Hour
		in.MinWakeInterval, in.SinceWake, in.MaxNudges = time.Hour, 3*time.Hour, 3
		in.HungTurn = true
		return in
	}
	for name, tc := range map[string]struct {
		change func(*Input)
		action Action
		reason string
	}{
		"waits instead of nudging":                {func(*Input) {}, None, HungTurnReason},
		"holds when the stall rule would":         {func(in *Input) { in.UnchangedFor, in.SinceWake = 3*time.Hour+30*time.Minute, 4*time.Hour }, AwaitUser, HungTurnHoldReason},
		"not before the time since the last wake": {func(in *Input) { in.UnchangedFor = 3*time.Hour + 30*time.Minute }, None, HungTurnReason},
		"nudges already sent count": {func(in *Input) {
			in.Nudges, in.UnchangedFor, in.SinceWake = 2, 90*time.Minute, 2*time.Hour
		}, AwaitUser, HungTurnHoldReason},
		"grace: not nudged": {func(in *Input) { in.HungTurn, in.HungTurnGrace = false, true }, None, HungTurnGraceReason},
		"grace: the same hold": {func(in *Input) {
			in.HungTurn, in.HungTurnGrace, in.UnchangedFor, in.SinceWake = false, true, 4*time.Hour, 5*time.Hour
		}, AwaitUser, HungTurnHoldReason},
		"a huge interval never holds": {func(in *Input) { in.MinWakeInterval, in.UnchangedFor, in.SinceWake = 1000000*time.Hour, 1<<62, 1<<62 }, None, HungTurnReason},
		"not hung: nudged as usual":   {func(in *Input) { in.HungTurn = false }, Nudge, "no progress for 3h0m0s"},
		"network down":                {func(in *Input) { in.NetworkDown = true }, None, NetworkDownReason},
		"paused":                      {func(in *Input) { in.Control.Paused = true }, None, "paused"},
		"stop":                        {func(in *Input) { in.Control.Stop = true }, Stop, ""},
		"deadline":                    {func(in *Input) { in.DeadlineReached = true }, AwaitUser, ""},
		"children running":            {func(in *Input) { in.Obs.ActiveChildren = 1 }, None, "child work running"},
		"a person is asked":           {func(in *Input) { in.Obs.AwaitingInput = true }, None, "waiting on a person (pending interaction)"},
		"provider retrying":           {func(in *Input) { in.Obs.Retrying = true }, None, "provider is retrying the turn"},
	} {
		in := hung()
		tc.change(&in)
		d := Decide(in)
		if d.Action != tc.action || (tc.reason != "" && d.Reason != tc.reason) {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

package decide

import (
	"strings"
	"testing"
	"time"
)

// The hung-turn watch, in the engine. The stall rule decides as always for
// an active session found hung; a nudge it would send is held back (counted,
// not sent), its hold after the nudges says why they were, and controls,
// loop-wide limits, children, a person asked, a provider retry and the
// network gate all come first, as without the watch.
func TestHungTurnInTheEngine(t *testing.T) {
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
		held   bool
	}{
		"a nudge due is held back":  {func(*Input) {}, None, HungTurnNudgeReason, true},
		"no nudge due: waits":       {func(in *Input) { in.SinceWake = 10 * time.Minute }, None, HungTurnReason, false},
		"the stall rule's hold":     {func(in *Input) { in.Nudges = 3 }, AwaitUser, "no progress after 3 nudges; " + HungTurnHoldReason, false},
		"not hung: nudged as usual": {func(in *Input) { in.HungTurn = false }, Nudge, "no progress for 3h0m0s", false},
		"network down":              {func(in *Input) { in.NetworkDown = true }, None, NetworkDownReason, false},
		"network down at the hold":  {func(in *Input) { in.Nudges, in.NetworkDown = 3, true }, None, NetworkDownReason, false},
		"paused":                    {func(in *Input) { in.Control.Paused = true }, None, "paused", false},
		"stop":                      {func(in *Input) { in.Control.Stop = true }, Stop, "", false},
		"deadline":                  {func(in *Input) { in.DeadlineReached = true }, AwaitUser, "", false},
		"children running":          {func(in *Input) { in.Obs.ActiveChildren = 1 }, None, "child work running", false},
		"a person is asked":         {func(in *Input) { in.Obs.AwaitingInput = true }, None, "waiting on a person (pending interaction)", false},
		"provider retrying":         {func(in *Input) { in.Obs.Retrying = true }, None, "provider is retrying the turn", false},
	} {
		in := hung()
		tc.change(&in)
		d := Decide(in)
		if d.Action != tc.action || (tc.reason != "" && d.Reason != tc.reason) || d.HeldNudge != tc.held {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// The hold keeps the stall rule's fields.
	in := hung()
	in.Nudges = 3
	if d := Decide(in); !d.StallHold || !d.EpisodeSpent {
		t.Errorf("hold: %+v", d)
	}
}

// When the stall rule would hold, if every check comes on time: the nudges
// it has left at the first moment each may be sent, then one more interval.
// Never within a year is no time.
func TestStallHoldAt(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	in := base()
	in.StallAfter, in.MinWakeInterval, in.MaxNudges = 30*time.Minute, time.Hour, 3
	for _, tc := range []struct {
		name             string
		unchanged, since time.Duration
		nudges           int
		want             time.Duration
	}{
		{"just back: stall_after, then three nudges an hour apart", 0, 100 * 365 * 24 * time.Hour, 0, 30*time.Minute + 3*time.Hour},
		{"one nudge just sent", 2 * time.Hour, 0, 1, 2*time.Hour + time.Hour},
		{"all spent, the last 20m ago", 4 * time.Hour, 20 * time.Minute, 3, 40 * time.Minute},
	} {
		in.UnchangedFor, in.SinceWake, in.Nudges = tc.unchanged, tc.since, tc.nudges
		if got := StallHoldAt(in, now); !got.Equal(now.Add(tc.want)) {
			t.Errorf("%s: %s, want %s", tc.name, got.Sub(now), tc.want)
		}
	}
	in.MinWakeInterval, in.Nudges = 1000000*time.Hour, 0
	if got := StallHoldAt(in, now); !got.IsZero() {
		t.Errorf("a huge interval: %v", got)
	}
	in.MinWakeInterval, in.MaxNudges = time.Hour, 0
	if got := StallHoldAt(in, now); !got.IsZero() {
		t.Errorf("max_nudges 0: %v", got)
	}
	_ = strings.Contains
}

package decide

import (
	"testing"
	"time"
)

func TestIdleSpacingWait(t *testing.T) {
	m := time.Minute
	for _, c := range []struct {
		first, max time.Duration
		n          int
		want       time.Duration
	}{
		{10 * m, 120 * m, 0, 0},
		{10 * m, 120 * m, 1, 10 * m},
		{10 * m, 120 * m, 2, 20 * m},
		{10 * m, 120 * m, 3, 40 * m},
		{10 * m, 120 * m, 4, 80 * m},
		{10 * m, 120 * m, 5, 120 * m},
		{10 * m, 120 * m, 1000000, 120 * m},
		{10 * m, 0, 3, 0},           // off
		{30 * m, 20 * m, 1, 20 * m}, // first above the cap is capped
	} {
		if got := IdleSpacingWait(c.first, c.max, c.n); got != c.want {
			t.Errorf("IdleSpacingWait(%v, %v, %d) = %v, want %v", c.first, c.max, c.n, got, c.want)
		}
	}
}

// spaced is an idle session whose last idle wake, 5 minutes ago, was the
// second in a row after which nothing changed: the next waits 20 minutes.
func spaced() Input {
	in := base()
	in.IdleSpacing = IdleSpacing{First: 10 * time.Minute, Max: 2 * time.Hour, Unchanged: 2, SinceIdleWake: 5 * time.Minute}
	return in
}

func TestIdleSpacingTable(t *testing.T) {
	m := time.Minute
	cases := []struct {
		name string
		mut  func(*Input)
		want Action
		wait IdleWait
	}{
		{"key off: woken at every check as today", func(i *Input) { i.IdleSpacing = IdleSpacing{Unchanged: 9} }, Wake, ""},
		// The case from a live trial: a coordinator idle and waiting on CI,
		// no lanes running, the previous wake's turn ended with nothing to
		// do and no structured fact changed. The next wake is spaced.
		{"idle, no lanes, previous wake's turn ended, nothing changed: spaced", func(i *Input) {
			i.IdleSpacing.Unchanged, i.IdleSpacing.SinceIdleWake = 1, 5*m
		}, None, IdleUnchanged},
		{"spaced until the wait has passed", func(*Input) {}, None, IdleUnchanged},
		{"wakes once the wait has passed", func(i *Input) { i.IdleSpacing.SinceIdleWake = 20 * m }, Wake, ""},
		{"wait grows with each unchanged wake", func(i *Input) { i.IdleSpacing.Unchanged, i.IdleSpacing.SinceIdleWake = 4, 79*m }, None, IdleUnchanged},
		{"wait is capped", func(i *Input) { i.IdleSpacing.Unchanged, i.IdleSpacing.SinceIdleWake = 40, 2*time.Hour }, Wake, ""},
		{"a changed fact resets the spacing", func(i *Input) { i.IdleSpacing.Unchanged = 0 }, Wake, ""},
		{"stop wins", func(i *Input) { i.Control.Stop = true }, None, ""},
		{"pause wins", func(i *Input) { i.Control.Paused = true }, None, ""},
		{"a hold wins", func(i *Input) { i.Control.AwaitingUser = true }, None, ""},
		{"window wins", func(i *Input) { i.InWindow = false }, None, ""},
		{"child work suppression wins", func(i *Input) { i.Obs.ActiveChildren = 1 }, None, ""},
		{"queued messages win", func(i *Input) { i.Obs.Queued = 1 }, None, ""},
		{"network down wins", func(i *Input) { i.NetworkDown = true }, None, ""},
		{"switch-back is not spaced", func(i *Input) { i.SwitchBackReady = true }, SwitchBack, ""},
		{"a child-error notice is not spaced", func(i *Input) {
			i.Obs.ErroredChildren, i.UnannouncedErroredChildren, i.MaxChildErrorNotices = []string{"thr_example_child"}, 1, 3
		}, Wake, ""},
		{"the child-error wake beside running children is not spaced", func(i *Input) {
			i.Obs.ActiveChildren, i.Obs.ErroredChildren, i.UnannouncedErroredChildren, i.MaxChildErrorNotices = 1, []string{"thr_example_child"}, 1, 3
		}, Wake, ""},
		{"an errored session follows the error rules", func(i *Input) { i.Obs.State = Error }, Reprompt, ""},
		{"an active session follows the stall rule", func(i *Input) { i.Obs.State, i.UnchangedFor = Active, 31*m }, Nudge, ""},
		{"the send limit still holds", func(i *Input) { i.SendLimitReached = true }, AwaitUser, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := spaced()
			c.mut(&in)
			got := Decide(in)
			if got.Action != c.want || got.IdleWait != c.wait {
				t.Fatalf("got %+v, want %s %q", got, c.want, c.wait)
			}
			if got.IdleWait != "" && got.Reason != IdleSpacedReason {
				t.Fatalf("reason %q", got.Reason)
			}
			if got.IdleWake != (got.Action == Wake && got.Reason == "idle") {
				t.Fatalf("IdleWake %v on %+v", got.IdleWake, got)
			}
		})
	}
}

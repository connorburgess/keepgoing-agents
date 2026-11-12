package decide

import (
	"testing"
)

// Errored children change which action Decide picks in exactly one case: an
// idle parent left alone only because another child is still running is woken
// once for a set it has not been told about. Everywhere else the decision is
// identical, and the note rides only on a wake: every hold, limit and gate
// that stops the wake stops the note. Checked over the full input enumeration.
func TestErroredChildrenChangeTheDecisionInOneCaseOnly(t *testing.T) {
	controls := []Control{
		{Enabled: true}, {}, {Enabled: true, Paused: true}, {Enabled: true, Stop: true},
		{Enabled: true, AwaitingUser: true}, {Enabled: true, Completed: true},
	}
	noted, withNote, exceptions := 0, 0, 0
	failures := 0
	boundInputs(func(in Input) {
		for _, control := range controls {
			for _, limit := range []bool{false, true} {
				in.Control, in.SendLimitReached = control, limit
				in.Obs.ErroredChildren, in.ErroredChildrenNoted = nil, false
				plain := Decide(in)
				if n := ChildErrorNote(in, plain); n != 0 {
					t.Fatalf("note without errored children: %+v", in)
				}
				in.Obs.ErroredChildren = []string{"thr_example_a", "thr_example_b", "thr_example_c"}
				for _, already := range []bool{false, true} {
					in.ErroredChildrenNoted = already
					got := Decide(in)
					exception := !already && plain == none("child work running") && in.Obs.State == Idle && in.InWindow &&
						!in.Obs.ChildrenUnverified && !in.Obs.RetryUnverified && !in.Obs.Retrying && in.Obs.Queued == 0 && in.Obs.Background == 0
					want := plain
					if exception {
						want = Decision{Action: Wake, Reason: ChildErrorWakeReason}
						exceptions++
					}
					if got != want && failures < 5 {
						failures++
						t.Errorf("got %+v, want %+v (without errored children %+v) for %+v", got, want, plain, in)
					}
					n := ChildErrorNote(in, got)
					wantNote := 0
					if got.Action == Wake && !already {
						wantNote = 3
					}
					if n != wantNote && failures < 5 {
						failures++
						t.Errorf("note=%d want %d: action %s noted %t %+v", n, wantNote, got.Action, already, in)
					}
					if n > 0 {
						withNote++
					} else if got.Action == Wake {
						noted++
					}
				}
			}
		}
	})
	if withNote == 0 || noted == 0 || exceptions == 0 {
		t.Fatalf("enumeration did not reach every case: with note %d, already noted %d, exceptions %d", withNote, noted, exceptions)
	}
}

func TestChildErrorNote(t *testing.T) {
	errored := []string{"thr_example_a", "thr_example_b"}
	cases := []struct {
		name string
		mut  func(*Input)
		want int
	}{
		{"idle parent with errored children", func(i *Input) {}, 2},
		{"already announced", func(i *Input) { i.ErroredChildrenNoted = true }, 0},
		{"no errored children", func(i *Input) { i.Obs.ErroredChildren = nil }, 0},
		{"paused", func(i *Input) { i.Control.Paused = true }, 0},
		{"stop", func(i *Input) { i.Control.Stop = true }, 0},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, 0},
		{"awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, 0},
		{"completed", func(i *Input) { i.Control.Completed = true }, 0},
		{"send limit reached", func(i *Input) { i.SendLimitReached = true }, 0},
		{"deadline reached", func(i *Input) { i.DeadlineReached = true }, 0},
		{"no-progress limit reached", func(i *Input) { i.NoProgressReached = true }, 0},
		{"outside the window", func(i *Input) { i.InWindow = false }, 0},
		{"a child is waiting on a person", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_example_a"} }, 0},
		{"another child is still running: the one wake errored children cause", func(i *Input) { i.Obs.ActiveChildren = 1 }, 2},
		{"another child running, set already announced", func(i *Input) { i.Obs.ActiveChildren, i.ErroredChildrenNoted = 1, true }, 0},
		{"another child running, something sent too recently", func(i *Input) { i.Obs.ActiveChildren, i.SinceWake = 1, i.MinWakeInterval-1 }, 0},
		{"another child running, exactly the wake interval since the last send", func(i *Input) { i.Obs.ActiveChildren, i.SinceWake = 1, i.MinWakeInterval }, 2},
		{"another child running, parent active", func(i *Input) { i.Obs.ActiveChildren, i.Obs.State = 1, Active }, 0},
		{"another child running, parent errored", func(i *Input) { i.Obs.ActiveChildren, i.Obs.State = 1, Error }, 0},
		{"another child running, outside the window", func(i *Input) { i.Obs.ActiveChildren, i.InWindow = 1, false }, 0},
		{"another child running, messages queued", func(i *Input) { i.Obs.ActiveChildren, i.Obs.Queued = 1, 1 }, 0},
		{"another child running, background work", func(i *Input) { i.Obs.ActiveChildren, i.Obs.Background = 1, 1 }, 0},
		{"another child running, discovery incomplete", func(i *Input) { i.Obs.ActiveChildren, i.Obs.ChildrenUnverified = 1, true }, 0},
		{"another child running, provider retrying", func(i *Input) { i.Obs.ActiveChildren, i.Obs.Retrying = 1, true }, 0},
		{"another child running, paused", func(i *Input) { i.Obs.ActiveChildren, i.Control.Paused = 1, true }, 0},
		{"another child running, stop", func(i *Input) { i.Obs.ActiveChildren, i.Control.Stop = 1, true }, 0},
		{"another child running, send limit reached", func(i *Input) { i.Obs.ActiveChildren, i.SendLimitReached = 1, true }, 0},
		{"another child running, a child waiting on a person", func(i *Input) { i.Obs.ActiveChildren, i.Obs.AwaitingChildren = 1, []string{"thr_example_a"} }, 0},
		{"another child running, parent waiting on a person", func(i *Input) { i.Obs.ActiveChildren, i.Obs.AwaitingInput = 1, true }, 0},
		{"another child running, no child in error", func(i *Input) { i.Obs.ActiveChildren, i.Obs.ErroredChildren = 1, nil }, 0},
		{"child discovery incomplete", func(i *Input) { i.Obs.ChildrenUnverified = true }, 0},
		{"parent waiting on a person", func(i *Input) { i.Obs.AwaitingInput = true }, 0},
		{"messages already queued", func(i *Input) { i.Obs.Queued = 1 }, 0},
		{"background work running", func(i *Input) { i.Obs.Background = 1 }, 0},
		{"parent active", func(i *Input) { i.Obs.State = Active }, 0},
		{"parent active and stalled is nudged without the note", func(i *Input) { i.Obs.State, i.UnchangedFor = Active, 2*i.StallAfter }, 0},
		{"parent errored is re-prompted without the note", func(i *Input) { i.Obs.State, i.Obs.ErrorClass = Error, ErrUnknown }, 0},
		{"switch-back instead of a wake", func(i *Input) { i.SwitchBackReady = true }, 0},
		{"parent gone", func(i *Input) { i.Obs.Gone = true }, 0},
	}
	for _, tc := range cases {
		in := base()
		in.Obs.ErroredChildren = errored
		tc.mut(&in)
		if got := ChildErrorNote(in, Decide(in)); got != tc.want {
			t.Errorf("%s: got %d, want %d (%+v)", tc.name, got, tc.want, Decide(in))
		}
	}
}

func TestErroredChildrenKey(t *testing.T) {
	a := ErroredChildrenKey([]string{"thr_example_a", "thr_example_b"})
	if len(a) != 32 || a != ErroredChildrenKey([]string{"thr_example_b", "thr_example_a", "thr_example_a"}) {
		t.Fatalf("order or repeats changed the key: %q", a)
	}
	for name, other := range map[string][]string{
		"one fewer": {"thr_example_a"}, "one more": {"thr_example_a", "thr_example_b", "thr_example_c"}, "a different child": {"thr_example_a", "thr_example_c"},
	} {
		if ErroredChildrenKey(other) == a {
			t.Errorf("%s: same key", name)
		}
	}
	if ErroredChildrenKey(nil) != "" || ErroredChildrenKey([]string{}) != "" {
		t.Error("empty set has a key")
	}
	// IDs that would collide if joined without a separator.
	if ErroredChildrenKey([]string{"thr_ab", "thr_c"}) == ErroredChildrenKey([]string{"thr_a", "bthr_c"}) {
		t.Error("joined IDs collide")
	}
}

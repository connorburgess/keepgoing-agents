package decide

import (
	"reflect"
	"testing"
	"time"
)

// Errored children change which action Decide picks in exactly one case: an
// idle parent left alone only because another child is still running is woken
// for a newly errored child, spaced and capped. Everywhere else the decision
// is identical, and the note rides only on a wake: every hold, limit and gate
// that stops the wake stops the note. Checked over the full input enumeration,
// with and without enough time since the last send.
func TestErroredChildrenChangeTheDecisionInOneCaseOnly(t *testing.T) {
	controls := []Control{
		{Enabled: true}, {}, {Enabled: true, Paused: true}, {Enabled: true, Stop: true},
		{Enabled: true, AwaitingUser: true}, {Enabled: true, Completed: true},
	}
	type pending struct{ unannounced, attempts int }
	pendings := []pending{{0, 0}, {2, 0}, {2, 2}, {2, 3}, {0, 3}}
	withNote, withoutNote, exceptions, failures := 0, 0, 0, 0
	boundInputs(func(in Input) {
		in.MaxChildErrorNotices = 3
		for _, control := range controls {
			for _, limit := range []bool{false, true} {
				for _, since := range []time.Duration{2 * time.Hour, 10 * time.Minute} {
					in.Control, in.SendLimitReached, in.SinceWake = control, limit, since
					in.Obs.ErroredChildren, in.UnannouncedErroredChildren, in.ChildErrorNoticeAttempts = nil, 0, 0
					plain := Decide(in)
					if n := ChildErrorNote(in, plain); n != 0 || plain.ChildErrorWake {
						t.Fatalf("note or child-error wake without errored children: %+v", in)
					}
					in.Obs.ErroredChildren = []string{"thr_example_a", "thr_example_b", "thr_example_c"}
					for _, p := range pendings {
						in.UnannouncedErroredChildren, in.ChildErrorNoticeAttempts = p.unannounced, p.attempts
						due := p.unannounced > 0 && p.attempts < in.MaxChildErrorNotices
						got := Decide(in)
						exception := due && plain == none("child work running") && in.Obs.State == Idle && in.InWindow &&
							!in.Obs.ChildrenUnverified && !in.Obs.RetryUnverified && !in.Obs.Retrying && in.Obs.Queued == 0 && in.Obs.Background == 0 &&
							since >= in.MinWakeInterval
						want := plain
						if exception {
							want = Decision{Action: Wake, Reason: ChildErrorWakeReason, ChildErrorWake: true}
							exceptions++
						}
						if got != want && failures < 5 {
							failures++
							t.Errorf("got %+v, want %+v (without errored children %+v) for %+v", got, want, plain, in)
						}
						n := ChildErrorNote(in, got)
						wantNote := 0
						if (got.Action == Wake || got.Action == Reprompt) && due {
							wantNote = 3
						}
						if n != wantNote && failures < 5 {
							failures++
							t.Errorf("note=%d want %d: action %s pending %+v %+v", n, wantNote, got.Action, p, in)
						}
						if n > 0 {
							withNote++
						} else if got.Action == Wake {
							withoutNote++
						}
					}
				}
			}
		}
	})
	if withNote == 0 || withoutNote == 0 || exceptions == 0 {
		t.Fatalf("enumeration did not reach every case: with note %d, wake without note %d, exceptions %d", withNote, withoutNote, exceptions)
	}
}

func TestChildErrorNote(t *testing.T) {
	errored := []string{"thr_example_a", "thr_example_b"}
	running := func(i *Input) { i.Obs.ActiveChildren = 1 }
	cases := []struct {
		name      string
		mut       func(*Input)
		want      int
		childWake bool
	}{
		{"idle parent with a newly errored child", func(i *Input) {}, 2, false},
		{"every errored child already announced", func(i *Input) { i.UnannouncedErroredChildren = 0 }, 0, false},
		{"one new child among announced ones: the count is all in error", func(i *Input) { i.UnannouncedErroredChildren = 1 }, 2, false},
		{"one rejected send below the cap", func(i *Input) { i.ChildErrorNoticeAttempts = 2 }, 2, false},
		{"given up after the cap of rejected sends", func(i *Input) { i.ChildErrorNoticeAttempts = 3 }, 0, false},
		{"no errored children", func(i *Input) { i.Obs.ErroredChildren = nil }, 0, false},
		{"paused", func(i *Input) { i.Control.Paused = true }, 0, false},
		{"stop", func(i *Input) { i.Control.Stop = true }, 0, false},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, 0, false},
		{"awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, 0, false},
		{"completed", func(i *Input) { i.Control.Completed = true }, 0, false},
		{"send limit reached", func(i *Input) { i.SendLimitReached = true }, 0, false},
		{"deadline reached", func(i *Input) { i.DeadlineReached = true }, 0, false},
		{"no-progress limit reached", func(i *Input) { i.NoProgressReached = true }, 0, false},
		{"outside the window", func(i *Input) { i.InWindow = false }, 0, false},
		{"a child is waiting on a person", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_example_a"} }, 0, false},
		{"child discovery incomplete", func(i *Input) { i.Obs.ChildrenUnverified = true }, 0, false},
		{"parent waiting on a person", func(i *Input) { i.Obs.AwaitingInput = true }, 0, false},
		{"messages already queued", func(i *Input) { i.Obs.Queued = 1 }, 0, false},
		{"background work running", func(i *Input) { i.Obs.Background = 1 }, 0, false},
		{"parent active", func(i *Input) { i.Obs.State = Active }, 0, false},
		{"parent active and stalled is nudged without the note", func(i *Input) { i.Obs.State, i.UnchangedFor = Active, 2*i.StallAfter }, 0, false},
		{"parent errored: the re-prompt carries the note", func(i *Input) { i.Obs.State, i.Obs.ErrorClass = Error, ErrUnknown }, 2, false},
		{"parent errored and backing off: nothing is sent, so no note", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass, i.Reprompts, i.SinceLastReprompt = Error, ErrUnknown, 1, time.Minute
		}, 0, false},
		{"parent errored, network down: nothing is sent, so no note", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass, i.NetworkDown = Error, ErrUnknown, true
		}, 0, false},
		{"switch-back instead of a wake", func(i *Input) { i.SwitchBackReady = true }, 0, false},
		{"parent gone", func(i *Input) { i.Obs.Gone = true }, 0, false},

		{"another child running: the one wake errored children cause", running, 2, true},
		{"another child running, nothing new to announce", func(i *Input) { running(i); i.UnannouncedErroredChildren = 0 }, 0, false},
		{"another child running, announcement given up", func(i *Input) { running(i); i.ChildErrorNoticeAttempts = 3 }, 0, false},
		{"another child running, something sent too recently", func(i *Input) { running(i); i.SinceWake = i.MinWakeInterval - 1 }, 0, false},
		{"another child running, exactly the wake interval since the last send", func(i *Input) { running(i); i.SinceWake = i.MinWakeInterval }, 2, true},
		{"another child running, parent active", func(i *Input) { running(i); i.Obs.State = Active }, 0, false},
		{"another child running, parent errored", func(i *Input) { running(i); i.Obs.State = Error }, 0, false},
		{"another child running, outside the window", func(i *Input) { running(i); i.InWindow = false }, 0, false},
		{"another child running, messages queued", func(i *Input) { running(i); i.Obs.Queued = 1 }, 0, false},
		{"another child running, background work", func(i *Input) { running(i); i.Obs.Background = 1 }, 0, false},
		{"another child running, discovery incomplete", func(i *Input) { running(i); i.Obs.ChildrenUnverified = true }, 0, false},
		{"another child running, provider retrying", func(i *Input) { running(i); i.Obs.Retrying = true }, 0, false},
		{"another child running, paused", func(i *Input) { running(i); i.Control.Paused = true }, 0, false},
		{"another child running, stop", func(i *Input) { running(i); i.Control.Stop = true }, 0, false},
		{"another child running, send limit reached", func(i *Input) { running(i); i.SendLimitReached = true }, 0, false},
		{"another child running, a child waiting on a person", func(i *Input) { running(i); i.Obs.AwaitingChildren = []string{"thr_example_a"} }, 0, false},
		{"another child running, parent waiting on a person", func(i *Input) { running(i); i.Obs.AwaitingInput = true }, 0, false},
		{"another child running, no child in error", func(i *Input) { running(i); i.Obs.ErroredChildren = nil }, 0, false},
	}
	for _, tc := range cases {
		in := base()
		in.Obs.ErroredChildren, in.UnannouncedErroredChildren, in.MaxChildErrorNotices = errored, 2, 3
		tc.mut(&in)
		d := Decide(in)
		if got := ChildErrorNote(in, d); got != tc.want || d.ChildErrorWake != tc.childWake {
			t.Errorf("%s: note %d, want %d; child-error wake %t, want %t (%+v)", tc.name, got, tc.want, d.ChildErrorWake, tc.childWake, d)
		}
	}
}

func TestChildHashes(t *testing.T) {
	a := ChildHashes([]string{"thr_example_b", "thr_example_a", "thr_example_a"})
	if len(a) != 2 || len(a[0]) != 24 || a[0] >= a[1] || !reflect.DeepEqual(a, ChildHashes([]string{"thr_example_a", "thr_example_b"})) {
		t.Fatalf("hashes %v", a)
	}
	for _, h := range a {
		if h == "thr_example_a" || h == "thr_example_b" {
			t.Fatal("hash is the ID")
		}
	}
	if got := ChildHashes(nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}

package supervise

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

// idleSpacingInput's facts, one row each. An idle wake is recorded at t0 on
// a session whose log holds 3 turn requests and 3 turn starts; the check 5
// minutes later sees what the row changed. Unchanged is 1 when the wake
// counts as having changed nothing, and 0 when the spacing starts over.
func TestIdleSpacingInputFacts(t *testing.T) {
	m := time.Minute
	before := &decide.TurnCounts{Requests: 3, Starts: 3}
	ended := &decide.TurnCounts{Requests: 4, Starts: 4} // the wake's own request and turn
	type world struct {
		tk  *tick
		obs decide.Observation
		ctl control.File
	}
	cases := []struct {
		name    string
		mut     func(t *testing.T, w *world)
		want    int
		cleared bool
		noTurns bool // an adapter that cannot count turns, at the wake and after
	}{
		{"an adapter that cannot count turns: a later idle check counts", func(*testing.T, *world) {}, 1, false, true},
		{"nothing changed and the wake's turn ended: unchanged", func(*testing.T, *world) {}, 1, false, false},
		{"the wake's request is not in the log yet: not counted", func(_ *testing.T, w *world) { w.obs.Turns = before }, 0, false, false},
		{"the wake's turn has not started: not counted", func(_ *testing.T, w *world) { w.obs.Turns = &decide.TurnCounts{Requests: 4, Starts: 3} }, 0, false, false},
		{"a person's message started another turn: reset", func(_ *testing.T, w *world) { w.obs.Turns = &decide.TurnCounts{Requests: 5, Starts: 5} }, 0, false, false},
		{"a person's message queued into the wake's turn: reset", func(_ *testing.T, w *world) { w.obs.Turns = &decide.TurnCounts{Requests: 5, Starts: 4} }, 0, false, false},
		{"another turn started without a new request: reset", func(_ *testing.T, w *world) { w.obs.Turns = &decide.TurnCounts{Requests: 4, Starts: 5} }, 0, false, false},
		{"a child outcome notice: reset", func(_ *testing.T, w *world) { w.obs.ChildOutcome = 9 }, 0, false, false},
		{"a goal revision: reset", func(t *testing.T, w *world) {
			if _, err := control.WriteGoal(w.tk.loop.StateDir, "example-loop", "thr_example", control.GoalSpec{Title: "Example goal", ObjectiveRefs: []string{"example/objective.md"}, AcceptanceRefs: []string{"example/acceptance.md"}}, "", 0); err != nil {
				t.Fatal(err)
			}
		}, 0, false, false},
		{"a target change: reset", func(_ *testing.T, w *world) {
			w.tk.st.ActiveTarget, w.tk.st.TargetSetBy = "example-fallback", state.SetByHandoff
		}, 0, false, false},
		{"a handoff or switch-back: reset", func(_ *testing.T, w *world) { w.tk.st.LastHandoffAt = t0.Add(m) }, 0, false, false},
		{"Git progress: reset", func(_ *testing.T, w *world) { w.tk.st.ProgressChangedAt = t0.Add(m) }, 0, false, false},
		{"a control change, such as resume: reset", func(_ *testing.T, w *world) { w.ctl.UpdatedAt = t0.Add(m) }, 0, false, false},
		{"the session in error: record cleared", func(_ *testing.T, w *world) { w.obs.State = decide.Error }, 0, true, false},
		{"the key turned off: record cleared", func(_ *testing.T, w *world) { w.tk.loop.Progress.IdleSpacing = nil }, 0, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loop := config.Loop{Name: "example-loop", StateDir: t.TempDir()}
			loop.Progress.Repositories = []string{"/example/repo"}
			loop.Progress.IdleSpacing = &config.IdleSpacing{First: config.Duration{Duration: 10 * m}, Max: config.Duration{Duration: 2 * time.Hour}}
			w := &world{tk: &tick{loop: loop, now: t0}, ctl: control.File{UpdatedAt: t0.Add(-time.Hour)}}
			w.obs = decide.Observation{State: decide.Idle, Turns: before}
			if c.noTurns {
				w.obs.Turns = nil
			}
			in := decide.Input{IdleSpacing: w.tk.idleSpacingInput(w.obs, w.ctl)}
			w.tk.recordIdleWake(in, false)
			if w.tk.st.IdleSpacing == nil {
				t.Fatal("no record after the wake")
			}
			w.tk.now = t0.Add(5 * m)
			w.obs.Turns = ended
			c.mut(t, w)
			if c.noTurns {
				w.obs.Turns = nil
			}
			got := w.tk.idleSpacingInput(w.obs, w.ctl)
			if got.Unchanged != c.want {
				t.Fatalf("Unchanged %d, want %d", got.Unchanged, c.want)
			}
			if c.want == 1 && got.SinceIdleWake != 5*m {
				t.Fatalf("SinceIdleWake %v", got.SinceIdleWake)
			}
			if (w.tk.st.IdleSpacing == nil) != c.cleared {
				t.Fatalf("record %+v, cleared want %v", w.tk.st.IdleSpacing, c.cleared)
			}
		})
	}
}

// A failed idle wake is not recorded: it started no turn, so it is not a
// wake that changed nothing, and the next check wakes again rather than
// waiting a growing interval. The send still counts toward max_sends as
// every other attempt does.
func TestIdleSpacingIgnoresFailedWakes(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.IdleSpacing = &config.IdleSpacing{First: config.Duration{Duration: 10 * time.Minute}, Max: config.Duration{Duration: 2 * time.Hour}}
	p := w.primary(decide.Observation{State: decide.Idle, Turns: &decide.TurnCounts{}})
	p.sendErr = errors.New("uncertain delivery")
	for i := range 3 {
		before := len(p.sent)
		_, _ = Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*5*time.Minute))) // the send's error is returned
		if len(p.sent) != before+1 {
			t.Fatalf("check %d sent nothing", i)
		}
	}
	st, err := state.Load(w.cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.IdleSpacing != nil {
		t.Fatalf("failed wakes recorded: %+v", st.IdleSpacing)
	}
}

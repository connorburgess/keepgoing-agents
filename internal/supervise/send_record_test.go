package supervise

import (
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// unsaved records an intended send the way a check does before making it,
// and then leaves state.json as it was: the check that made it was killed,
// before or after the send, or could not save what happened.
func unsaved(t *testing.T, w *world, at time.Time, action, target string) {
	t.Helper()
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	max := w.cfg.Loop.Limits.MaxSends
	if action == "stop" || strings.HasPrefix(action, "report:") {
		max = 0
	}
	if _, err := state.Reserve(w.dir, max, st.SendsRecorded, state.SendIntent{At: at, Action: action, Target: target}); err != nil {
		t.Fatal(err)
	}
}

// The kill points. Each case is a check that recorded a send and was then
// killed (before or after making it: the next check cannot tell), and says
// what the next check does.
func TestASendRecordedWithoutAnOutcomeAtEachKillPoint(t *testing.T) {
	t.Run("turn retry or re-prompt: counted once, the backoff waited out", func(t *testing.T) {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
		unsaved(t, w, t0, "reprompt", state.Primary)
		r := runTick(t, w, t0.Add(time.Minute))
		if st, _ := state.Load(w.dir); r.Reason != "backing off after error" || len(p.sent) != 0 || st.Reprompts != 1 || st.UnconfirmedReprompts != 1 {
			t.Fatalf("%s (%s) %+v", r.Action, r.Reason, st)
		}
		if r = runTick(t, w, t0.Add(2*time.Minute)); r.Reason != "backing off after error" {
			t.Fatalf("counted twice? %+v", r)
		}
		// The cap reached with an unconfirmed attempt says so.
		runTick(t, w, t0.Add(11*time.Minute))
		r = runTick(t, w, t0.Add(22*time.Minute))
		if r.Action != "await_user" || !strings.Contains(r.Reason, "1 of them were recorded but could not be confirmed as sent") {
			t.Fatalf("%s (%s)", r.Action, r.Reason)
		}
	})
	t.Run("stop: sent again, never taken as made", func(t *testing.T) {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Active})
		control.Update(w.dir, func(f *control.File) { f.Stop = true })
		unsaved(t, w, t0, "stop", state.Primary)
		if r := runTick(t, w, t0.Add(time.Minute)); r.Action != "stop" || p.stopped != 1 {
			t.Fatalf("a stop recorded and perhaps never made was not made: %s (%s)", r.Action, r.Reason)
		}
	})
	t.Run("stop recorded, then resumed: a later stop is still sent", func(t *testing.T) {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Active})
		unsaved(t, w, t0, "stop", state.Primary)
		runTick(t, w, t0.Add(time.Minute)) // not a stop: the loop was resumed
		control.Update(w.dir, func(f *control.File) { f.Stop = true })
		if r := runTick(t, w, t0.Add(2*time.Minute)); r.Action != "stop" || p.stopped != 1 {
			t.Fatalf("%s (%s) stopped=%d", r.Action, r.Reason, p.stopped)
		}
	})
	t.Run("handoff with two fallbacks: held for a person, no second brief", func(t *testing.T) {
		w, _, fallbacks := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		r := runTick(t, w, t0.Add(time.Minute))
		if r.Action != "await_user" || r.Reason != "a handoff brief to claude-other may have been delivered; check that conversation before resuming" {
			t.Fatalf("%s (%s)", r.Action, r.Reason)
		}
		for id, f := range fallbacks {
			if len(f.sent) != 0 {
				t.Fatalf("%s was briefed", id)
			}
		}
		if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser {
			t.Fatal("not held")
		}
	})
	t.Run("switch-back: spaced by retry_every", func(t *testing.T) {
		w, p, _ := switchBackWorld(t) // retry_every 30m
		w.probeOK["primary"] = true
		p.obs = decide.Observation{State: decide.Idle}
		after := t0.Add(2*time.Hour + time.Minute)
		unsaved(t, w, after, "switch_back", state.Primary)
		if r := runTick(t, w, after.Add(time.Minute)); len(p.sent) != 0 || r.Target == state.Primary {
			t.Fatalf("second brief a minute later: %+v", r)
		}
		if st, _ := state.Load(w.dir); st.SwitchBackRejected != 1 || !st.LastSwitchBackTry.Equal(after) {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("the waiting-on-a-person report: not repeated", func(t *testing.T) {
		w := newWorld(t, true, false)
		w.primary(decide.Observation{State: decide.Active, AwaitingInput: true})
		unsaved(t, w, t0, "report:awaiting_input", state.Primary)
		runTick(t, w, t0.Add(time.Minute))
		if strings.Contains(w.reportTypes(), "await_user") {
			t.Fatalf("reported again: %s", w.reportTypes())
		}
	})
	t.Run("degraded report: not repeated", func(t *testing.T) {
		w := newWorld(t, true, true)
		w.probeOK = map[string]bool{}
		w.primary(decide.Observation{State: decide.Active})
		unsaved(t, w, t0, "report:degraded", state.Primary)
		runTick(t, w, t0.Add(time.Minute))
		if strings.Contains(w.reportTypes(), "degraded") {
			t.Fatalf("reported again: %s", w.reportTypes())
		}
	})
	t.Run("idle wake: woken again, as before", func(t *testing.T) {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Idle})
		unsaved(t, w, t0, "wake", state.Primary)
		if r := runTick(t, w, t0.Add(time.Minute)); r.Action != "wake" || len(p.sent) != 1 {
			t.Fatalf("%s (%s)", r.Action, r.Reason)
		}
	})
	t.Run("a record the state already covers is not counted", func(t *testing.T) {
		// state.json written by an older binary drops sendsRecorded; a check
		// that saved at or after the send's time recorded its outcome.
		w := newWorld(t, true, false)
		w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
		runTick(t, w, t0) // re-prompts and records it
		st, _ := state.Load(w.dir)
		st.SendsRecorded = 0
		if err := state.Save(w.dir, st); err != nil {
			t.Fatal(err)
		}
		runTick(t, w, t0.Add(time.Minute))
		if st, _ = state.Load(w.dir); st.Reprompts != 1 {
			t.Fatalf("counted again: %d", st.Reprompts)
		}
	})
}

// Without max_sends a handoff brief and a switch-back brief are still
// recorded before they are made.
func TestBriefsAreRecordedWithoutASendLimit(t *testing.T) {
	w, p, _ := switchBackWorld(t) // made a handoff to claude-other
	l, err := state.LoadLedger(w.dir)
	if err != nil || len(l.Pending) == 0 || l.Pending[len(l.Pending)-1].Action != "handoff" || *l.Used != 0 {
		t.Fatalf("handoff: %+v %v", l, err)
	}
	w.probeOK["primary"] = true
	p.obs = decide.Observation{State: decide.Idle}
	runTick(t, w, t0.Add(2*time.Hour+time.Minute))
	l, _ = state.LoadLedger(w.dir)
	if len(l.Pending) == 0 || l.Pending[len(l.Pending)-1].Action != "switch_back" || *l.Used != 0 {
		t.Fatalf("switch-back: %+v", l)
	}
}

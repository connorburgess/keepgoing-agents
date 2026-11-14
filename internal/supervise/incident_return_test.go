package supervise

import (
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A check that begins on a fallback the loop can no longer use returns it to
// the primary before anything else. The history records which fallback it was
// on, by its hashed alias, and why; the check itself is the primary's.
func TestIncidentRecordsAReturnToThePrimary(t *testing.T) {
	primary, from := incident.TargetKey(state.Primary), incident.TargetKey("claude-other")
	check := func(t *testing.T, w *world, reason string, tick func() Result) {
		t.Helper()
		if err := state.Save(w.dir, state.File{ActiveTarget: "claude-other"}); err != nil {
			t.Fatal(err)
		}
		w.primary(decide.Observation{State: decide.Idle})
		if tick == nil {
			tick = func() Result { return runTick(t, w, t0) }
		}
		res := tick()
		r := lastIncident(t, w)
		if res.Target != state.Primary || r.TargetKey != primary || r.Returned == nil || r.Returned.FromKey != from || r.Returned.Reason != reason || r.Tier != incident.TierKey {
			t.Fatalf("result %+v record %+v returned %+v", res, r, r.Returned)
		}
		if st, err := state.Load(w.dir); err != nil || st.Target() != state.Primary {
			t.Fatalf("the return was not saved: %+v %v", st, err)
		}
	}
	t.Run("removed", func(t *testing.T) {
		w := newWorld(t, true, true)
		var kept = w.cfg.FallbackSet.Candidates[:0]
		for _, c := range w.cfg.FallbackSet.Candidates {
			if c.ID != "claude-other" {
				kept = append(kept, c)
			}
		}
		w.cfg.FallbackSet.Candidates = kept
		check(t, w, incident.ReturnRemoved, nil)
	})
	t.Run("disabled", func(t *testing.T) {
		w := newWorld(t, true, true)
		for i := range w.cfg.FallbackSet.Candidates {
			if c := &w.cfg.FallbackSet.Candidates[i]; c.ID == "claude-other" {
				c.Disabled = true
			}
		}
		check(t, w, incident.ReturnDisabled, nil)
	})
	t.Run("no created thread", func(t *testing.T) {
		w, _, f := newThreadWorld(t)
		check(t, w, incident.ReturnNoCreatedThread, func() Result { return mustThreadTick(t, w, f, t0) })
	})
}

// A check on a usable target records no return, and neither does a check
// that starts on the primary.
func TestIncidentRecordsNoReturnWhenTheTargetIsUsable(t *testing.T) {
	for _, target := range []string{state.Primary, "claude-other"} {
		w := newWorld(t, true, true)
		if err := state.Save(w.dir, state.File{ActiveTarget: target}); err != nil {
			t.Fatal(err)
		}
		w.primary(decide.Observation{State: decide.Idle})
		w.adapters["claude-other"] = &fakeAdapter{obs: decide.Observation{State: decide.Idle}}
		runTick(t, w, t0)
		if r := lastIncident(t, w); r.Returned != nil || r.TargetKey != incident.TargetKey(target) {
			t.Fatalf("%s: %+v %+v", target, r, r.Returned)
		}
	}
}

// A dry run sends nothing, but it saves its state, and with it the return:
// that is a move, kept in the key tier like any other.
func TestADryRunRecordsTheReturnItSaves(t *testing.T) {
	w := newWorld(t, true, true)
	for i := range w.cfg.FallbackSet.Candidates {
		if c := &w.cfg.FallbackSet.Candidates[i]; c.ID == "claude-other" {
			c.Disabled = true
		}
	}
	if err := state.Save(w.dir, state.File{ActiveTarget: "claude-other"}); err != nil {
		t.Fatal(err)
	}
	w.primary(decide.Observation{State: decide.Idle})
	o := w.opts(t0)
	o.DryRun = true
	if _, err := Tick(t.Context(), w.cfg, o); err != nil {
		t.Fatal(err)
	}
	r := lastIncident(t, w)
	if !r.DryRun || r.Returned == nil || r.Returned.Reason != incident.ReturnDisabled || r.Tier != incident.TierKey {
		t.Fatalf("%+v %+v", r, r.Returned)
	}
	if st, _ := state.Load(w.dir); st.Target() != state.Primary {
		t.Fatalf("the dry run did not save the return: %+v", st)
	}
}

package supervise

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
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
		if r.Action != "await_user" || !strings.HasPrefix(r.Reason, "a handoff brief to claude-other may have been delivered. Check that conversation: if it took the task, run `keepgoing retarget <id>`") {
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
	t.Run("switch-back: held for a person, the fallback not woken", func(t *testing.T) {
		w, p, fb := switchBackWorld(t)
		w.probeOK["primary"] = true
		p.obs = decide.Observation{State: decide.Idle}
		sentBefore := len(fb.sent)
		after := t0.Add(2*time.Hour + time.Minute)
		unsaved(t, w, after, "switch_back", state.Primary)
		r := runTick(t, w, after.Add(time.Minute))
		if r.Action != "await_user" || !strings.HasPrefix(r.Reason, "a switch-back brief to primary may have been delivered.") || len(p.sent) != 0 || len(fb.sent) != sentBefore {
			t.Fatalf("%s (%s) primary=%d fallback=%d", r.Action, r.Reason, len(p.sent), len(fb.sent)-sentBefore)
		}
	})
	t.Run("handoff, then held by pause and resumed: still held, no second brief", func(t *testing.T) {
		w, _, fallbacks := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		control.Update(w.dir, func(f *control.File) { f.Paused = true })
		runTick(t, w, t0.Add(time.Minute)) // held: does not read the records
		control.Update(w.dir, func(f *control.File) { f.Paused = false })
		if r := runTick(t, w, t0.Add(2*time.Minute)); r.Action != "await_user" || !strings.HasPrefix(r.Reason, "a handoff brief to claude-other") {
			t.Fatalf("%s (%s)", r.Action, r.Reason)
		}
		for id, f := range fallbacks {
			if len(f.sent) != 0 {
				t.Fatalf("%s was briefed", id)
			}
		}
	})
	t.Run("handoff, then a stop request: both conversations stopped, and held", func(t *testing.T) {
		w, p, fallbacks := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		control.Update(w.dir, func(f *control.File) { f.Stop = true })
		r := runTick(t, w, t0.Add(time.Minute))
		if p.stopped != 1 || fallbacks["claude-other"].stopped != 1 || fallbacks["codex-main"].stopped != 0 {
			t.Fatalf("%s (%s) stopped primary=%d claude-other=%d codex-main=%d", r.Action, r.Reason, p.stopped, fallbacks["claude-other"].stopped, fallbacks["codex-main"].stopped)
		}
		for id, f := range fallbacks {
			if len(f.sent) != 0 {
				t.Fatalf("%s was briefed", id)
			}
		}
		if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser || !ctl.Stop || !strings.Contains(ctl.Note, "keepgoing resume --keep-stop") {
			t.Fatalf("%+v", ctl)
		}
	})
	t.Run("a stop requested after the hold stops every conversation it names", func(t *testing.T) {
		w, p, fallbacks := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		runTick(t, w, t0.Add(time.Minute)) // holds; nothing is stopped
		if p.stopped != 0 {
			t.Fatal("stopped before a stop was asked for")
		}
		control.Update(w.dir, func(f *control.File) { f.Stop = true })
		r := runTick(t, w, t0.Add(2*time.Minute))
		if r.Action != "stop" || p.stopped != 1 || fallbacks["claude-other"].stopped != 1 || fallbacks["codex-main"].stopped != 0 {
			t.Fatalf("%s (%s) primary=%d claude-other=%d", r.Action, r.Reason, p.stopped, fallbacks["claude-other"].stopped)
		}
		runTick(t, w, t0.Add(3*time.Minute))
		if p.stopped != 1 || fallbacks["claude-other"].stopped != 1 {
			t.Fatal("the stop was repeated for the same request")
		}
	})
	t.Run("killed after the hold was saved, before state: resume still settles", func(t *testing.T) {
		w, _, fallbacks := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		// The hold was saved, then the check was killed: state.json does not
		// say the record is settled. A person resumes (what resume does).
		st, _ := state.Load(w.dir)
		st.ResumedBriefs = []state.Brief{{Action: "handoff", Target: "claude-other"}}
		st.SkipHandoffOnce = true
		state.Save(w.dir, st)
		r := runTick(t, w, t0.Add(2*time.Minute))
		if r.Action == "await_user" || r.Action == "handoff" {
			t.Fatalf("held or briefed again after resume: %s (%s)", r.Action, r.Reason)
		}
		for id, f := range fallbacks {
			if len(f.sent) != 0 {
				t.Fatalf("%s was briefed", id)
			}
		}
		if st, _ = state.Load(w.dir); len(st.ResumedBriefs) != 0 {
			t.Fatalf("%+v", st.ResumedBriefs)
		}
	})
	t.Run("handoff, then a dry run: previewed, not settled", func(t *testing.T) {
		w, _, _ := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		opts := w.opts(t0.Add(time.Minute))
		opts.DryRun = true
		if r, _ := Tick(context.Background(), w.cfg, opts); r.Action != "await_user" {
			t.Fatalf("dry run: %s (%s)", r.Action, r.Reason)
		}
		if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
			t.Fatal("a dry run raised the hold")
		}
		if r := runTick(t, w, t0.Add(2*time.Minute)); r.Action != "await_user" {
			t.Fatalf("the dry run settled the record: %s (%s)", r.Action, r.Reason)
		}
	})
	t.Run("a deleted sends.json does not reuse settled numbers", func(t *testing.T) {
		w := newWorld(t, true, false) // backoff 10m
		p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
		runTick(t, w, t0) // re-prompt 1, record 1 settled
		os.Remove(filepath.Join(w.dir, state.SendsName))
		unsaved(t, w, t0.Add(11*time.Minute), "reprompt", state.Primary)
		if l, _ := state.LoadLedger(w.dir); l.Seq != 2 {
			t.Fatalf("numbering restarted: %+v", l)
		}
		// Counted, so the next check does not send (here it is the second
		// of two allowed, so the loop holds); if it were taken as settled, a
		// fresh re-prompt would be sent.
		if r := runTick(t, w, t0.Add(12*time.Minute)); r.Action == "reprompt" || len(p.sent) != 1 {
			t.Fatalf("an interrupted re-prompt after the file was deleted was not counted: %s (%s), sends %d", r.Action, r.Reason, len(p.sent))
		}
	})
	t.Run("the deadline's hold settles nothing", func(t *testing.T) {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
		unsaved(t, w, t0, "reprompt", state.Primary)
		w.cfg.Loop.Limits.Deadline = t0.Format(time.RFC3339)
		if r := runTick(t, w, t0.Add(time.Minute)); r.Action != "await_user" {
			t.Fatalf("%s (%s)", r.Action, r.Reason)
		}
		if st, _ := state.Load(w.dir); st.SendsRecorded != 0 || st.Reprompts != 0 {
			t.Fatalf("the deadline's hold settled the record: %+v", st)
		}
		if l, _ := state.LoadLedger(w.dir); l.Settled != 0 {
			t.Fatalf("%+v", l)
		}
		// The deadline is moved and the loop resumed: the record is counted.
		w.cfg.Loop.Limits.Deadline = ""
		control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false })
		if r := runTick(t, w, t0.Add(2*time.Minute)); r.Reason != "backing off after error" || len(p.sent) != 0 {
			t.Fatalf("%s (%s)", r.Action, r.Reason)
		}
	})
	t.Run("a stop with an unsettled switch-back stops both conversations", func(t *testing.T) {
		w, p, fb := switchBackWorld(t) // the loop is on claude-other
		after := t0.Add(2*time.Hour + time.Minute)
		unsaved(t, w, after, "switch_back", state.Primary)
		control.Update(w.dir, func(f *control.File) { f.Stop = true })
		runTick(t, w, after.Add(time.Minute))
		if fb.stopped != 1 || p.stopped != 1 {
			t.Fatalf("stopped fallback %d, primary %d", fb.stopped, p.stopped)
		}
		if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser || len(ctl.BriefHold) != 1 || !ctl.Stop {
			t.Fatalf("%+v", ctl)
		}
	})
	t.Run("two unsettled briefs are both named", func(t *testing.T) {
		w, _, _ := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		unsaved(t, w, t0, "handoff", "codex-main")
		r := runTick(t, w, t0.Add(time.Minute))
		if !strings.Contains(r.Reason, "a handoff brief to claude-other and a handoff brief to codex-main may have been delivered") {
			t.Fatalf("%s", r.Reason)
		}
	})
	t.Run("a brief whose hold cannot be saved is not settled", func(t *testing.T) {
		w, _, fallbacks := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		held, err := lock.AcquireNamed(w.dir, lock.Control)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Minute)))
		held.Release()
		if err == nil {
			t.Fatal("the check succeeded without saving the hold")
		}
		if st, _ := state.Load(w.dir); st.SendsRecorded != 0 {
			t.Fatalf("settled without a hold: %+v", st)
		}
		if r := runTick(t, w, t0.Add(2*time.Minute)); r.Action != "await_user" {
			t.Fatalf("the next check did not hold: %s (%s)", r.Action, r.Reason)
		}
		for id, f := range fallbacks {
			if len(f.sent) != 0 {
				t.Fatalf("%s was briefed", id)
			}
		}
	})
	t.Run("after a brief hold is resumed, no route is briefed on the next check", func(t *testing.T) {
		w, _, fallbacks := rejectingWorld(t, false)
		unsaved(t, w, t0, "handoff", "claude-other")
		runTick(t, w, t0.Add(time.Minute)) // holds
		// What resume does (cmd/keepgoing resumeEpisode): the brief counted
		// as delivered and not taken, one handoff skipped, the hold cleared.
		st, _ := state.Load(w.dir)
		st.HandoffBriefs = map[string]state.BriefRecord{"claude-other": {Failed: 1, LastAt: t0.Add(2 * time.Minute)}}
		st.SkipHandoffOnce = true
		state.Save(w.dir, st)
		control.Update(w.dir, func(f *control.File) { f.AwaitingUser, f.BriefHold = false, nil })
		if r := runTick(t, w, t0.Add(3*time.Minute)); r.Action == "handoff" || !strings.Contains(r.Reason, "no route is briefed on this check") {
			t.Fatalf("%s (%s)", r.Action, r.Reason)
		}
		for id, f := range fallbacks {
			if len(f.sent) != 0 {
				t.Fatalf("%s was briefed", id)
			}
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
	// Each send gets a numbered record, settled once the check saves.
	l, err := state.LoadLedger(w.dir)
	if err != nil || l.Seq != 1 || l.Settled != 1 || *l.Used != 0 {
		t.Fatalf("handoff: %+v %v", l, err)
	}
	w.probeOK["primary"] = true
	p.obs = decide.Observation{State: decide.Idle}
	if r := runTick(t, w, t0.Add(2*time.Hour+time.Minute)); r.Target != state.Primary || len(p.sent) != 1 {
		t.Fatalf("no switch-back: %+v", r)
	}
	l, _ = state.LoadLedger(w.dir)
	if l.Seq < 2 || l.Settled != l.Seq || *l.Used != 0 {
		t.Fatalf("switch-back: %+v", l)
	}
}

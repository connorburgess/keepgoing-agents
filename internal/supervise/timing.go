package supervise

import (
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/timing"
)

// TimingNote is what usage-aware timing decided in a check, and what came of
// it: applied, failed (with the cap reached or not), not applied because a
// stop or pause arrived, or a stay.
type TimingNote struct {
	Action string `json:"action"`
	To     string `json:"to,omitempty"`
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	Text   string `json:"text"`
	// Outcome is applied, failed, capped, held, dry_run or stay.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// settleTimingIntent: a step was recorded and the check that recorded it did
// not settle it, so the thread may be on either route. The record wins: the
// route state names is applied again (a model change is idempotent), and
// timing decides afresh at the next turn boundary.
func (t *tick) settleTimingIntent(target string, agent config.Agent) {
	tr := t.st.Timing
	if tr == nil || tr.Pending == nil || t.opts.DryRun {
		return
	}
	p := tr.Pending
	a, err := t.newAdapter(agent, t.loop.StateDir, target)
	if err == nil {
		if act, ok := a.(adapter.Activator); ok {
			err = act.Activate(t.ctx)
		}
	}
	if err != nil {
		t.warn("timing: a step to %s was recorded and not settled, and %s could not be applied again (%v); the next check tries again", p.To, target, err)
		return
	}
	tr.Pending = nil
	t.warn("timing: a step from %s to %s was recorded and not settled; the thread was set to %s, the route on record", p.From, p.To, target)
}

// timingStep decides with usage-aware timing at a turn boundary and, when it
// decides a move, applies it to the loop's own BB thread before the check's
// wake, if any, so the next turn runs on the new route. It sends nothing of
// its own. The move is recorded before the thread is changed, and stop and
// pause, read again just before, win. It returns the adapter the check's
// send, if any, goes through: the new route's when the step was applied.
func (t *tick) timingStep(a adapter.Adapter, d decide.Decision, obs decide.Observation, gate decide.Control) adapter.Adapter {
	l := t.loop
	if !l.TimingOn() || len(l.Timing.Steps) == 0 {
		return a
	}
	in := timing.FromRecords(t.cfg, t.st, t.reg, t.now)
	in.Held = gate.Paused || gate.Stop || gate.AwaitingUser || gate.Completed || d.Action == decide.AwaitUser || d.Action == decide.Stop
	in.InError = obs.State == decide.Error
	in.RefusalOrUnknown = obs.State == decide.Unknown
	in.TurnEnded = obs.State == decide.Idle && (d.Action == decide.None || d.Action == decide.Wake)
	in.OtherMove = t.resolvedMove
	if at := timing.RetargetReset(in); at != nil {
		in.Retarget.ResetsAt = at
		if !t.opts.DryRun && t.st.Timing != nil && t.st.Timing.Retarget != nil {
			t.st.Timing.Retarget.ResetsAt = at
		}
	}
	dec := timing.Decide(in)
	note := &TimingNote{Action: dec.Action, To: dec.To, Rule: dec.Rule, Reason: dec.Reason, Text: dec.Text(timingSpan), Outcome: "stay"}
	t.res.Timing = note
	if dec.Action == timing.Stay {
		return a
	}
	if t.opts.DryRun {
		note.Outcome = "dry_run"
		return a
	}
	if t.st.Timing == nil {
		t.st.Timing = &state.TimingRecord{}
	}
	tr := t.st.Timing
	var resets time.Time
	if dec.ResetsAt != nil {
		resets = dec.ResetsAt.UTC()
	}
	if tr.Capped && tr.FailedTo == dec.To && tr.FailedResetsAt.Equal(resets) {
		note.Outcome = "capped"
		return a
	}
	// Stop and pause win: a control change written during this check means
	// the thread is not changed.
	if c, err := control.Load(l.StateDir); err != nil || c.Paused || c.Stop || c.AwaitingUser || c.Completed {
		note.Outcome = "held"
		return a
	}
	move := state.TimingMove{At: t.now, From: in.Current, To: dec.To, Action: dec.Action, Rule: dec.Rule, Reason: dec.Reason, Window: dec.Window, ResetsAt: resets, Text: note.Text}
	if err := t.recordTimingIntent(&move); err != nil {
		t.warn("timing: the step to %s was not recorded, so it was not made: %v", dec.To, err)
		note.Outcome, note.Error = "failed", "not recorded"
		return a
	}
	_, agent := t.resolve(dec.To)
	na, err := t.newAdapter(agent, l.StateDir, dec.To)
	if err == nil {
		if act, ok := na.(adapter.Activator); ok {
			err = act.Activate(t.ctx)
		}
	}
	tr.Pending = nil
	if err != nil {
		if tr.FailedTo != dec.To || !tr.FailedResetsAt.Equal(resets) {
			tr.FailedTo, tr.FailedResetsAt, tr.Failures, tr.Capped = dec.To, resets, 0, false
		}
		tr.Failures++
		note.Outcome, note.Error = "failed", err.Error()
		t.warn("timing: could not step to %s (%v); staying on %s", dec.To, err, in.Current)
		if tr.Failures >= l.Errors.MaxReprompts {
			tr.Capped = true
			note.Outcome = "capped"
			t.report("timing", fmt.Sprintf("%s: timing could not step to %s %d times for this %s window and stops trying until it resets; staying on %s", l.Name, dec.To, tr.Failures, dec.Window, in.Current),
				map[string]any{"from": in.Current, "to": dec.To, "rule": dec.Rule, "reason": dec.Reason, "failures": tr.Failures, "outcome": "capped"})
		}
		return a
	}
	tr.LastMove = &move
	tr.Failures, tr.FailedTo, tr.FailedResetsAt, tr.Capped = 0, "", time.Time{}, false
	t.st.ActiveTarget, t.st.TargetSetBy = dec.To, state.SetByTiming
	t.res.Target = dec.To
	note.Outcome = "applied"
	t.report("timing", fmt.Sprintf("%s: timing %s", l.Name, timingPast(note.Text)),
		map[string]any{"from": in.Current, "to": dec.To, "rule": dec.Rule, "reason": dec.Reason, "window": dec.Window, "outcome": "applied"})
	return na
}

// recordTimingIntent saves the step in state.json before the thread is
// changed, ahead of the rest of the check's state.
func (t *tick) recordTimingIntent(m *state.TimingMove) error {
	saved, err := state.Load(t.loop.StateDir)
	if err != nil {
		return err
	}
	if saved.Timing == nil {
		saved.Timing = &state.TimingRecord{}
	}
	saved.Timing.Pending = m
	if err := state.Save(t.loop.StateDir, saved); err != nil {
		return err
	}
	t.st.Timing.Pending = m
	return nil
}

// timingSpan words a length of time as status does.
func timingSpan(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
}

// timingPast turns "step up to x: …" into "stepped up to x: …".
func timingPast(text string) string {
	for _, p := range [][2]string{{"step up ", "stepped up "}, {"step down ", "stepped down "}, {"return ", "returned "}} {
		if len(text) >= len(p[0]) && text[:len(p[0])] == p[0] {
			return p[1] + text[len(p[0]):]
		}
	}
	return text
}

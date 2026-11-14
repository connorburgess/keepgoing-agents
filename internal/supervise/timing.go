package supervise

import (
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/timing"
)

// TimingNote is what usage-aware timing decided in a check, and what came of
// it: applied, failed, capped (a step up timing stopped trying), held (a stop
// or pause arrived during the check), dry_run or stay.
type TimingNote struct {
	Action  string `json:"action"`
	To      string `json:"to,omitempty"`
	Rule    string `json:"rule"`
	Reason  string `json:"reason"`
	Text    string `json:"text"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// LeftConfiguration is the reason a loop on a step timing chose returns to
// the first route when that step left the configuration.
const LeftConfiguration = "left_configuration"

// reapplyRoute sets the model and effort of the route on record on the
// thread, when the thread may not match it: a step was recorded and not
// settled (the check that recorded it ended, or its activation failed), or
// the loop was moved to the first route because its route left the
// configuration. It runs in a check that acts: after stop, pause and the
// other holds, with the session claimed and observed idle with nothing
// waiting for it, and with its time free. A model change is idempotent.
func (t *tick) reapplyRoute(target string, obs decide.Observation) {
	tr := t.st.Timing
	pending := tr != nil && tr.Pending != nil
	if (!pending && !t.st.ReapplyRoute) || t.opts.DryRun {
		return
	}
	// Between turns only: not while a turn runs or anything waits for the
	// session (an interaction, queued messages, lanes, background jobs).
	if obs.State != decide.Idle || obs.AwaitingInput || obs.DeferredInteraction || obs.Queued > 0 || obs.ActiveChildren > 0 || obs.Background > 0 {
		return
	}
	if t.left() < repairRouteTime {
		t.warn("the thread may not be on %s, the route on record; there is no time left in this check to set it, so the next check does", target)
		return
	}
	agent := t.stepAgent(target)
	a, err := t.newAdapter(agent, t.loop.StateDir, target)
	if err == nil {
		if act, ok := a.(adapter.Activator); ok {
			err = act.Activate(t.ctx)
		}
	}
	if err != nil {
		t.warn("the thread may not be on %s, the route on record, and it could not be set (%v); the next check tries again", target, err)
		return
	}
	reason := "route_left_configuration"
	if pending {
		reason = "unsettled_step"
	}
	t.incidentTiming("repair", incident.TimingRepair, reason)
	if pending {
		t.warn("timing: a step from %s to %s was recorded and not settled; the thread was set to %s, the route on record", tr.Pending.From, tr.Pending.To, target)
		tr.Pending = nil
	}
	if t.st.ReapplyRoute {
		t.warn("the route the loop was on is no longer configured; the thread was set to %s's model and effort", target)
		t.st.ReapplyRoute = false
	}
}

// stepAgent is the agent for a route timing applies. A step on the loop's
// own BB thread that sets only its model or only its effort takes the other
// from the first route, so a step's effort never carries over from the step
// before it.
func (t *tick) stepAgent(id string) config.Agent {
	_, agent := t.resolve(id)
	if id == state.Primary || agent.BB == nil || t.loop.BB == nil {
		return agent
	}
	bb := *agent.BB
	if bb.Model == "" {
		bb.Model = t.loop.BB.Model
	}
	if bb.Reasoning == "" {
		bb.Reasoning = t.loop.BB.Reasoning
	}
	agent.BB = &bb
	return agent
}

// atTurnBoundary: the engine is waking an idle session whose turn ended. An
// idle session it does not wake is one something held back (a retry or child
// scan it could not verify, children working, an interaction or queued
// messages pending, background jobs, outside the window, the network down),
// and timing does not act on it either.
func atTurnBoundary(d decide.Decision, obs decide.Observation) bool {
	return obs.State == decide.Idle && d.Action == decide.Wake && !d.ChildErrorWake
}

// timingStep decides with usage-aware timing at a turn boundary and, when it
// decides a move, applies it to the loop's own BB thread before the check's
// wake, so the next turn runs on the new route. It sends nothing of its own.
// The move is recorded before the thread is changed, and stop and pause, read
// again just before, win. It returns the adapter the wake goes through: the
// new route's when the step was applied.
func (t *tick) timingStep(a adapter.Adapter, d decide.Decision, obs decide.Observation, gate decide.Control) adapter.Adapter {
	l := t.loop
	boundary := atTurnBoundary(d, obs)
	if timing.LeftConfiguration(l, t.st) {
		return t.returnFromLeftStep(a, boundary)
	}
	if !l.TimingOn() || len(l.Timing.Steps) == 0 {
		return a
	}
	in := timing.FromRecords(t.cfg, t.st, t.reg, t.now)
	in.Held = gate.Paused || gate.Stop || gate.AwaitingUser || gate.Completed || d.Action == decide.AwaitUser || d.Action == decide.Stop
	in.InError = obs.State == decide.Error
	in.Unknown = obs.State == decide.Unknown
	in.TurnEnded = boundary
	in.OtherMove = t.resolvedMove
	if !t.opts.DryRun && in.Retarget != nil && (t.st.Timing == nil || t.st.Timing.Retarget == nil) {
		// A retarget with no record of its own: save the wait now, so the
		// reset it waits for is not moved again.
		t.timingRecord().Retarget = &state.TimingRetarget{At: in.Retarget.At}
	}
	if at := timing.RetargetReset(in); at != nil {
		in.Retarget.ResetsAt = at
		if !t.opts.DryRun {
			t.timingRecord().Retarget.ResetsAt = at
		}
	}
	dec := timing.Decide(in)
	note := &TimingNote{Action: dec.Action, To: dec.To, Rule: dec.Rule, Reason: dec.Reason, Text: dec.Text(timingSpan), Outcome: "stay"}
	t.res.Timing = note
	if dec.Action == timing.Stay {
		return a
	}
	var resets time.Time
	if dec.ResetsAt != nil {
		resets = dec.ResetsAt.UTC()
	}
	move := state.TimingMove{At: t.now, From: in.Current, To: dec.To, Action: dec.Action, Rule: dec.Rule, Reason: dec.Reason, Window: dec.Window, ResetsAt: resets, Text: note.Text}
	return t.applyStep(a, move, note)
}

// returnFromLeftStep returns a loop on a step that left the configuration to
// the first route at the next turn boundary.
func (t *tick) returnFromLeftStep(a adapter.Adapter, boundary bool) adapter.Adapter {
	from := t.st.Target()
	note := &TimingNote{Action: timing.Back, To: state.Primary, Rule: timing.RuleGate, Reason: LeftConfiguration,
		Text: fmt.Sprintf("return to %s: %s is no longer a timing step", state.Primary, from), Outcome: "stay"}
	t.res.Timing = note
	if !boundary {
		return a
	}
	move := state.TimingMove{At: t.now, From: from, To: state.Primary, Action: timing.Back, Rule: timing.RuleGate, Reason: LeftConfiguration, Text: note.Text}
	return t.applyStep(a, move, note)
}

// applyStep makes a decided move: stop and pause are read again, the move is
// recorded before the thread is changed, then the thread is changed.
func (t *tick) applyStep(a adapter.Adapter, move state.TimingMove, note *TimingNote) adapter.Adapter {
	l := t.loop
	if t.opts.DryRun {
		note.Outcome = "dry_run"
		return a
	}
	// Stop and pause win: a control change written during this check means
	// the thread is not changed.
	if c, err := control.Load(l.StateDir); err != nil || !c.Enabled || c.Paused || c.Stop || c.AwaitingUser || c.Completed {
		note.Outcome = "held"
		return a
	}
	tr := t.timingRecord()
	if err := t.recordTimingIntent(&move); err != nil {
		t.warn("timing: the step to %s was not recorded, so it was not made: %v", move.To, err)
		note.Outcome, note.Error = "failed", "not recorded"
		return a
	}
	na, err := t.newAdapter(t.stepAgent(move.To), l.StateDir, move.To)
	if err == nil {
		if act, ok := na.(adapter.Activator); ok {
			err = act.Activate(t.ctx)
		}
	}
	if err != nil {
		// The record of the step is kept: the thread may be partly changed,
		// and the next check sets it back to the route on record.
		t.timingFailed(move, note, err)
		return a
	}
	failed := 0
	if tr.FailedTo == move.To && tr.FailedAction == move.Action {
		failed = tr.Failures
	}
	tr.Pending = nil
	tr.LastMove = &move
	if move.Action == timing.Up {
		tr.LastUp = &move
	}
	// A step up timing gave up on stays given up until its window resets.
	tr.Failures, tr.FailedTo, tr.FailedAction, tr.FailedWindow, tr.FailedResetsAt, tr.Capped, tr.FailReported = 0, "", "", "", time.Time{}, false, false
	t.st.ActiveTarget, t.st.TargetSetBy, t.st.ReapplyRoute = move.To, state.SetByTiming, false
	if move.Reason == LeftConfiguration {
		t.st.TargetSetBy = state.SetByConfiguration
	}
	t.res.Target = move.To
	note.Outcome = "applied"
	cause := incident.TimingReturn
	switch move.Action {
	case timing.Up:
		cause = incident.TimingStepUp
	case timing.Down:
		cause = incident.TimingStepDown
	}
	t.incidentTiming(move.Rule, cause, move.Reason)
	text := fmt.Sprintf("%s: timing %s", l.Name, timingPast(note.Text))
	if failed > 0 {
		text += fmt.Sprintf(" (after %d failed attempts)", failed)
	}
	data := map[string]any{"from": move.From, "to": move.To, "rule": move.Rule, "reason": move.Reason, "window": move.Window, "outcome": "applied"}
	// The step is reported once the check's state, which records it, is saved.
	t.afterSave = append(t.afterSave, func() { t.report("timing", text, data) })
	return na
}

// timingFailed records a failed activation. Only a step up is capped: after
// errors.max_reprompts failures for one window instance timing stops trying
// it and reports once. A return or a step down is tried again at every turn
// boundary, and its first failure is reported once.
func (t *tick) timingFailed(move state.TimingMove, note *TimingNote, err error) {
	l, tr := t.loop, t.timingRecord()
	if tr.FailedTo != move.To || tr.FailedAction != move.Action || !sameInstance(tr.FailedResetsAt, move.ResetsAt) {
		tr.FailedTo, tr.FailedAction, tr.FailedWindow, tr.FailedResetsAt, tr.Failures, tr.Capped, tr.FailReported = move.To, move.Action, move.Window, move.ResetsAt, 0, false, false
	}
	tr.Failures++
	note.Outcome, note.Error = "failed", err.Error()
	t.warn("timing: could not move to %s (%v); staying on %s", move.To, err, move.From)
	data := map[string]any{"from": move.From, "to": move.To, "rule": move.Rule, "reason": move.Reason, "failures": tr.Failures}
	switch {
	case move.Action == timing.Up && tr.Failures >= l.Errors.MaxReprompts:
		tr.Capped = true
		gave := move
		tr.GaveUp = &gave
		note.Outcome = "capped"
		data["outcome"] = "capped"
		t.report("timing", fmt.Sprintf("%s: timing could not step up to %s %d times for this %s window and stops trying until it resets; staying on %s", l.Name, move.To, tr.Failures, move.Window, move.From), data)
	case move.Action != timing.Up && !tr.FailReported:
		tr.FailReported = true
		data["outcome"] = "failed"
		t.report("timing", fmt.Sprintf("%s: timing could not move from %s to %s (%v); it tries again at each turn boundary", l.Name, move.From, move.To, err), data)
	case move.Action != timing.Up && tr.Failures == l.Errors.MaxReprompts:
		data["outcome"] = "still_failing"
		t.report("timing", fmt.Sprintf("%s: still on %s after %d failed moves to %s; timing keeps trying at each turn boundary", l.Name, move.From, tr.Failures, move.To), data)
	}
}

// sameInstance: two reset times of one window instance, allowing for the
// rounding a provider's reported reset has between readings.
func sameInstance(a, b time.Time) bool {
	d := a.Sub(b)
	return d <= allowance.SameReset && d >= -allowance.SameReset
}

// incidentTiming gives the check's history record a timing cause and the
// timing evaluation with the decision's rule and reason codes. A move
// outranks a repair in the same check.
func (t *tick) incidentTiming(rule, cause, reason string) {
	if t.incidentTimingCause != "" && cause == incident.TimingRepair {
		return
	}
	t.incidentTimingCause = cause
	t.incidentDecisions.AddTiming(rule, cause, reason)
}

func (t *tick) timingRecord() *state.TimingRecord {
	if t.st.Timing == nil {
		t.st.Timing = &state.TimingRecord{}
	}
	return t.st.Timing
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
	t.timingRecord().Pending = m
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

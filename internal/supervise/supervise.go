// Package supervise runs one tick: observe, probe, decide, act, record.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/handoff"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

// ProbeFunc runs one probe.
type ProbeFunc func(ctx context.Context, c config.Candidate, tier probe.Tier, timeout time.Duration) probe.Result

// Options tune a tick. Zero values use the real implementations.
type Options struct {
	Now         time.Time
	DryRun      bool // decide and record; send nothing and run no probes
	Fingerprint func([]string) (string, error)
	NewAdapter  factory.Func
	Probe       ProbeFunc
	Reporter    report.Reporter
	Busy        func(ctx context.Context, y config.Yield) (bool, error)
}

// ProbeNote summarises a probe that ran during the tick.
type ProbeNote struct {
	ID         string     `json:"id"`
	Tier       probe.Tier `json:"tier"`
	OK         bool       `json:"ok"`
	ErrorClass string     `json:"errorClass,omitempty"`
	Detail     string     `json:"detail,omitempty"`
}

// HandoffNote describes a handoff attempt.
type HandoffNote struct {
	From   string   `json:"from"`
	To     string   `json:"to,omitempty"`
	Reason string   `json:"reason"`
	Tried  []string `json:"tried,omitempty"`
	DryRun bool     `json:"dryRun,omitempty"`
}

// Result is what a tick did, printed as JSON by the CLI.
type Result struct {
	At           time.Time       `json:"at"`
	Loop         string          `json:"loop"`
	Target       string          `json:"target"`
	Control      string          `json:"control"`
	State        string          `json:"state"`
	ErrorClass   string          `json:"errorClass,omitempty"`
	ResetsAt     *time.Time      `json:"resetsAt,omitempty"`
	Action       string          `json:"action"`
	Reason       string          `json:"reason"`
	Window       string          `json:"window"`
	UnchangedFor string          `json:"unchangedFor,omitempty"`
	Reprompts    int             `json:"reprompts,omitempty"`
	Handoff      *HandoffNote    `json:"handoff,omitempty"`
	Redundancy   *health.Summary `json:"redundancy,omitempty"`
	Probes       []ProbeNote     `json:"probes,omitempty"`
	Yielded      bool            `json:"yielded,omitempty"`
	DryRun       bool            `json:"dryRun,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`
}

type tick struct {
	ctx  context.Context
	cfg  *config.Config
	loop config.Loop
	fb   *config.Fallbacks
	opts Options
	now  time.Time
	res  *Result
	st   state.File
	reg  *health.Registry
}

func (t *tick) warn(format string, a ...any) {
	t.res.Warnings = append(t.res.Warnings, fmt.Sprintf(format, a...))
}

func (t *tick) report(typ, text string, data map[string]any) {
	if t.opts.DryRun {
		return
	}
	if err := t.opts.Reporter.Report(t.ctx, report.Event{Type: typ, Loop: t.loop.Name, Text: text, At: t.now, Data: data}); err != nil {
		t.warn("report %s: %v", typ, err)
	}
}

// Tick performs one supervision pass. The caller holds the lock.
func Tick(ctx context.Context, cfg *config.Config, opts Options) (Result, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Fingerprint == nil {
		opts.Fingerprint = state.Fingerprint
	}
	if opts.NewAdapter == nil {
		opts.NewAdapter = factory.New
	}
	if opts.Probe == nil {
		opts.Probe = probe.Run
	}
	if opts.Reporter == nil {
		opts.Reporter = report.Nop{}
	}
	if opts.Busy == nil {
		opts.Busy = runBusy
	}
	now := opts.Now.UTC()
	res := Result{At: now, Loop: cfg.Loop.Name, Action: string(decide.None), DryRun: opts.DryRun}
	t := &tick{ctx: ctx, cfg: cfg, loop: cfg.Loop, fb: cfg.FallbackSet, opts: opts, now: now, res: &res}
	err := t.run()
	return res, err
}

// resolve returns the agent for a target id, reverting to primary when a
// candidate has been removed from the fallbacks file.
func (t *tick) resolve(target string) (string, config.Agent) {
	if target == state.Primary {
		return target, t.loop.Agent
	}
	c, ok := t.fb.Find(target)
	if !ok || c.Disabled {
		t.warn("active target %q is no longer an enabled fallback; returning to primary", target)
		return state.Primary, t.loop.Agent
	}
	return c.ID, c.Agent
}

func (t *tick) label(target string) string {
	_, a := t.resolve(target)
	return target + " (" + a.Describe() + ")"
}

func (t *tick) run() error {
	loop, res := t.loop, t.res
	ctl, err := control.Load(loop.StateDir)
	if err != nil {
		return err
	}
	res.Control = ctl.Summary()
	if t.st, err = state.Load(loop.StateDir); err != nil {
		return err
	}
	if !ctl.Stop {
		t.st.StopRequestedSent = false
	}
	target, agent := t.resolve(t.st.Target())
	if target != t.st.Target() {
		t.st.ActiveTarget = target
	}
	res.Target = target

	gate := ctl.Gate()
	if !gate.Enabled || gate.Paused || gate.AwaitingUser {
		switch {
		case !gate.Enabled:
			res.Reason = "disabled"
		case gate.Paused:
			res.Reason = "paused"
		default:
			res.Reason = "awaiting user"
		}
		res.State, res.Window = "not observed", "not checked"
		return t.record(nil)
	}
	if y := loop.Yield; y != nil && !gate.Stop {
		busy, err := t.opts.Busy(t.ctx, *y)
		if err != nil {
			t.warn("busy check failed; not yielding: %v", err)
		} else if busy {
			res.Yielded, res.Reason, res.State, res.Window = true, "yielded: busy check reports other work", "not observed", "not checked"
			return t.record(nil)
		}
	}

	a, err := t.opts.NewAdapter(agent, loop.StateDir, target)
	if err != nil {
		return err
	}
	obs, err := a.Observe(t.ctx)
	if err != nil {
		res.State, res.Reason, res.Window = string(decide.Unknown), "could not verify state; no action taken", "not checked"
		t.report("error", fmt.Sprintf("%s: could not observe %s", loop.Name, target), map[string]any{"target": target})
		return t.record(err)
	}
	res.State = string(obs.State)
	if obs.State == decide.Error {
		res.ErrorClass = string(obs.ErrorClass)
	}
	if !obs.ResetsAt.IsZero() {
		r := obs.ResetsAt.UTC()
		res.ResetsAt = &r
	}
	inWindow, why, err := window.Inside(loop.Window, t.now)
	if err != nil {
		return err
	}
	res.Window = why

	// Fallback health: scheduled probes, then the redundancy picture.
	var summary health.Summary
	if t.fb != nil {
		if t.reg, err = health.Load(loop.StateDir); err != nil {
			return err
		}
		if inWindow && !t.opts.DryRun && !gate.Stop {
			t.scheduledProbes()
		}
		summary = health.Evaluate(t.fb, t.reg, t.now, target, agent.Group)
		res.Redundancy = &summary
		if degraded := !summary.Meets; degraded != t.st.Degraded && !t.opts.DryRun {
			t.st.Degraded = degraded
			if degraded {
				t.report("degraded", fmt.Sprintf("%s: only %d of the required %d distinct healthy fallbacks; handoffs are refused", loop.Name, summary.Distinct, summary.Min), map[string]any{"redundancy": summary})
			} else {
				t.report("recovered", fmt.Sprintf("%s: %d distinct healthy fallbacks again", loop.Name, summary.Distinct), map[string]any{"redundancy": summary})
			}
		}
	}

	unchangedFor := time.Duration(0)
	if len(loop.Progress.Repositories) > 0 {
		f, err := t.opts.Fingerprint(loop.Progress.Repositories)
		if err != nil {
			res.Reason = "could not fingerprint repositories; no action taken"
			return t.record(err)
		}
		if f != t.st.Fingerprint || t.st.LastProgressAt.IsZero() {
			t.st.Fingerprint, t.st.LastProgressAt = f, t.now
		}
		unchangedFor = t.now.Sub(t.st.LastProgressAt)
		res.UnchangedFor = unchangedFor.Truncate(time.Second).String()
	}
	sinceWake := 100 * 365 * 24 * time.Hour
	if !t.st.LastWakeAt.IsZero() {
		sinceWake = t.now.Sub(t.st.LastWakeAt)
	}
	sinceReprompt := time.Duration(0)
	if !t.st.LastRepromptAt.IsZero() {
		sinceReprompt = t.now.Sub(t.st.LastRepromptAt)
	}
	if obs.State == decide.Active || obs.State == decide.Idle {
		t.st.Reprompts, t.st.LastRepromptAt = 0, time.Time{}
	}

	failover := decide.Failover{
		Eligible:          contains(loop.Errors.FailoverOn, string(obs.ErrorClass)),
		Available:         t.fb != nil && summary.Meets,
		WaitIfResetWithin: loop.Errors.WaitIfResetWithin.Duration,
	}
	if obs.ResetsAt.After(t.now) {
		failover.ResetIn = obs.ResetsAt.Sub(t.now)
	}
	in := decide.Input{
		Obs: obs, Control: gate, InWindow: inWindow,
		UnchangedFor: unchangedFor, SinceWake: sinceWake,
		StallAfter: loop.Progress.StallAfter.Duration, MinWakeInterval: loop.Progress.MinWakeInterval.Duration,
		Failover:  failover,
		Reprompts: t.st.Reprompts, MaxReprompts: loop.Errors.MaxReprompts,
		SinceLastReprompt: sinceReprompt, Backoff: loop.Errors.Backoff.Duration,
	}
	if failover.Eligible && obs.State == decide.Error && t.fb != nil && !summary.Meets {
		t.warn("handoff refused: %d of the required %d distinct healthy fallbacks", summary.Distinct, summary.Min)
	}
	d := decide.Decide(in)
	if d.Action == decide.Stop && t.st.StopRequestedSent {
		d = decide.Decision{Action: decide.None, Reason: "stop already requested"}
	}
	res.Action, res.Reason = string(d.Action), d.Reason

	var actErr error
	if d.Action == decide.Handoff {
		moved, err := t.handoff(target, agent, obs.ErrorClass, summary)
		if err != nil {
			actErr = err
		} else if !moved && !t.opts.DryRun {
			// Every candidate failed its exact probe: fall back to the
			// ordinary error rules for the current target.
			in.Failover.Available = false
			d = decide.Decide(in)
			res.Action, res.Reason = string(d.Action), d.Reason+" (no fallback passed its exact probe)"
			actErr = t.act(a, d)
		}
	} else if !t.opts.DryRun {
		actErr = t.act(a, d)
	}
	if t.reg != nil && !t.opts.DryRun {
		if err := t.reg.Save(loop.StateDir); err != nil && actErr == nil {
			actErr = err
		}
	}
	t.st.LastState = string(obs.State)
	res.Reprompts = t.st.Reprompts
	return t.record(actErr)
}

func (t *tick) scheduledProbes() {
	for _, c := range t.fb.Candidates {
		due, _ := t.reg.Due(c, t.fb.Probe, t.now)
		if !due {
			continue
		}
		r := t.opts.Probe(t.ctx, c, probe.Scheduled, t.fb.Probe.Timeout.Duration)
		r.At, r.Tier = t.now, probe.Scheduled
		t.reg.Record(c.ID, r)
		t.res.Probes = append(t.res.Probes, ProbeNote{ID: c.ID, Tier: r.Tier, OK: r.OK, ErrorClass: r.ErrorClass, Detail: r.Detail})
		if !r.OK {
			t.report("probe_failed", fmt.Sprintf("%s: scheduled probe of %s failed (%s)", t.loop.Name, c.ID, r.ErrorClass), map[string]any{"candidate": c.ID, "probe": r})
		}
	}
}

// handoff tries healthy candidates in order. Each must pass an exact-target
// probe right now before it receives the brief.
func (t *tick) handoff(from string, fromAgent config.Agent, class decide.ErrorClass, summary health.Summary) (bool, error) {
	note := &HandoffNote{From: from, Reason: string(class), DryRun: t.opts.DryRun}
	t.res.Handoff = note
	if t.opts.DryRun {
		if len(summary.Healthy) > 0 {
			note.To = summary.Healthy[0]
		}
		return true, nil
	}
	for _, id := range summary.Healthy {
		c, _ := t.fb.Find(id)
		r := t.opts.Probe(t.ctx, c, probe.Exact, t.fb.Probe.Timeout.Duration)
		r.At, r.Tier = t.now, probe.Exact
		t.reg.Record(id, r)
		t.res.Probes = append(t.res.Probes, ProbeNote{ID: id, Tier: r.Tier, OK: r.OK, ErrorClass: r.ErrorClass, Detail: r.Detail})
		note.Tried = append(note.Tried, id)
		if !r.OK {
			continue
		}
		ta, err := t.opts.NewAdapter(c.Agent, t.loop.StateDir, c.ID)
		if err != nil {
			t.warn("handoff to %s: %v", id, err)
			continue
		}
		if act, ok := ta.(adapter.Activator); ok {
			if err := act.Activate(t.ctx); err != nil {
				t.warn("handoff to %s: activate: %v", id, err)
				continue
			}
		}
		brief, err := handoff.Brief(t.loop, handoff.Facts{Loop: t.loop.Name, At: t.now, From: t.label(from), To: id + " (" + c.Describe() + ")", Reason: string(class)})
		if err != nil {
			return false, err
		}
		if err := ta.Send(t.ctx, brief, adapter.ModeAuto); err != nil {
			t.warn("handoff to %s: send: %v", id, err)
			continue
		}
		note.To = id
		t.st.ActiveTarget, t.st.LastHandoffAt, t.st.LastWakeAt = id, t.now, t.now
		t.st.Handoffs++
		t.st.Reprompts, t.st.LastRepromptAt = 0, time.Time{}
		t.res.Target = id
		t.report("handoff", fmt.Sprintf("%s: handed off from %s to %s after a %s error", t.loop.Name, from, id, class), map[string]any{"from": from, "to": id, "reason": string(class), "tried": note.Tried})
		return true, nil
	}
	return false, nil
}

func (t *tick) act(a adapter.Adapter, d decide.Decision) error {
	loop := t.loop
	switch d.Action {
	case decide.Wake, decide.Nudge, decide.Reprompt:
		prompt, err := loop.PromptText()
		if err != nil {
			return err
		}
		mode := adapter.ModeAuto
		if d.Action == decide.Nudge {
			mode = adapter.ModeQueue
		}
		if err := a.Send(t.ctx, prompt, mode); err != nil {
			var unsupported adapter.ErrUnsupported
			if errors.As(err, &unsupported) {
				t.warn("%s adapter: %v", a.Name(), err)
				return nil
			}
			return err
		}
		t.st.LastWakeAt = t.now
		if d.Action == decide.Reprompt {
			t.st.Reprompts++
			t.st.LastRepromptAt = t.now
		}
		t.report(string(d.Action), fmt.Sprintf("%s: %s %s (%s)", loop.Name, d.Action, t.res.Target, d.Reason), nil)
	case decide.AwaitUser:
		if _, err := control.Update(loop.StateDir, func(f *control.File) { f.AwaitingUser, f.Note = true, d.Reason }); err != nil {
			return err
		}
		t.report("await_user", fmt.Sprintf("%s: needs a person: %s", loop.Name, d.Reason), map[string]any{"target": t.res.Target})
	case decide.Stop:
		err := a.Stop(t.ctx)
		var unsupported adapter.ErrUnsupported
		if errors.As(err, &unsupported) {
			t.warn("%s adapter cannot stop a running turn; nothing more will be sent", a.Name())
			err = nil
		}
		if err != nil {
			return err
		}
		t.st.StopRequestedSent = true
		t.report("stop", fmt.Sprintf("%s: stopped %s", loop.Name, t.res.Target), nil)
	}
	return nil
}

func (t *tick) record(actErr error) error {
	t.st.CheckedAt = t.now
	if err := state.Save(t.loop.StateDir, t.st); err != nil {
		return err
	}
	ev := state.Event{At: t.now, Loop: t.loop.Name, Event: "tick", Action: t.res.Action, Reason: t.res.Reason, State: t.res.State, Target: t.res.Target}
	extra := map[string]any{}
	if actErr != nil {
		ev.Error = actErr.Error()
		t.report("error", fmt.Sprintf("%s: tick failed: %v", t.loop.Name, actErr), nil)
	}
	if t.res.DryRun {
		extra["dryRun"] = true
	}
	if t.res.Handoff != nil {
		extra["handoff"] = t.res.Handoff
	}
	if len(t.res.Probes) > 0 {
		extra["probes"] = t.res.Probes
	}
	if len(t.res.Warnings) > 0 {
		extra["warnings"] = t.res.Warnings
	}
	if len(extra) > 0 {
		ev.Extra = extra
	}
	if err := state.Append(t.loop.StateDir, ev); err != nil {
		return err
	}
	return actErr
}

// runBusy runs the yield command: exit 0 means busy, any other exit means
// free. A command that cannot run is an error, and the tick does not yield.
func runBusy(ctx context.Context, y config.Yield) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, y.Timeout.Duration)
	defer cancel()
	cmd := exec.CommandContext(ctx, y.BusyCommand[0], y.BusyCommand[1:]...)
	cmd.Env = os.Environ()
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return false, nil
	}
	return false, err
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

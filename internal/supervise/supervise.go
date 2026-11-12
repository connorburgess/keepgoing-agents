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
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

// ProbeFunc runs one probe.
type ProbeFunc func(ctx context.Context, c config.Candidate, tier probe.Tier, timeout time.Duration) probe.Result

// Options tune a tick. Zero values use the real implementations.
type Options struct {
	Version     string // build label of the caller, for historical diagnostics
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
	// Skipped lists fallbacks not probed this check because of rejected
	// briefs, as "id: backing_off" or "id: capped".
	Skipped []string `json:"skipped,omitempty"`
	DryRun  bool     `json:"dryRun,omitempty"`
}

// Result is what a tick did, printed as JSON by the CLI.
type Result struct {
	At           time.Time  `json:"at"`
	Loop         string     `json:"loop"`
	Target       string     `json:"target"`
	Control      string     `json:"control"`
	State        string     `json:"state"`
	ErrorClass   string     `json:"errorClass,omitempty"`
	ResetsAt     *time.Time `json:"resetsAt,omitempty"`
	Action       string     `json:"action"`
	Reason       string     `json:"reason"`
	Window       string     `json:"window"`
	UnchangedFor string     `json:"unchangedFor,omitempty"`
	Reprompts    int        `json:"reprompts,omitempty"`
	// ErroredChildren is how many direct child threads were in error.
	// ChildErrorNote is set when this check's wake announces them.
	ErroredChildren int             `json:"erroredChildren,omitempty"`
	ChildErrorNote  bool            `json:"childErrorNote,omitempty"`
	Handoff         *HandoffNote    `json:"handoff,omitempty"`
	Redundancy      *health.Summary `json:"redundancy,omitempty"`
	Probes          []ProbeNote     `json:"probes,omitempty"`
	Yielded         bool            `json:"yielded,omitempty"`
	DryRun          bool            `json:"dryRun,omitempty"`
	Warnings        []string        `json:"warnings,omitempty"`
}

type tick struct {
	incidentDecisions   incident.Decisions
	incidentStateLoaded bool
	incidentAdapter     string
	incidentTarget      string
	incidentObservation incident.Observation
	incidentControl     *incident.Controls
	incidentProbes      incident.Probes
	incidentLimitWait   decide.LimitWait // from the last engine decision, which sets the tick's reason
	progress            *state.Progress
	observedTarget      string
	awaitingInput       bool
	awaitingChildren    []string
	// decisionInput is the input of the latest ordinary decision, and
	// erroredChildrenKey the errored child set it saw; act uses both to decide
	// whether a wake carries the child-error notice.
	decisionInput       decide.Input
	erroredChildrenKey  string
	deferredInteraction bool
	ctx                 context.Context
	cfg                 *config.Config
	loop                config.Loop
	fb                  *config.Fallbacks
	opts                Options
	now                 time.Time
	res                 *Result
	st                  state.File
	reg                 *health.Registry
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
	if journalErr := t.recordIncident(err != nil); journalErr != nil {
		t.warn("incident history write failed; diagnostic evidence is incomplete")
		err = errors.Join(err, journalErr)
	}
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
	t.incidentControl = incident.Gate(ctl.Gate())
	if t.st, err = state.Load(loop.StateDir); err != nil {
		return err
	}
	t.incidentStateLoaded = true
	if !ctl.Stop {
		t.st.StopRequestedSent = false
	}
	target, agent := t.resolve(t.st.Target())
	if target != t.st.Target() {
		t.st.ActiveTarget = target
	}
	res.Target = target
	t.incidentTarget = target
	t.incidentAdapter = agent.Adapter

	gate := ctl.Gate()
	if gate.Stop {
		return t.stop(target, agent, gate)
	}
	if !gate.Enabled || gate.Paused || gate.AwaitingUser || gate.Completed {
		switch {
		case !gate.Enabled:
			res.Reason = "disabled"
		case gate.Paused:
			res.Reason = "paused"
		case gate.AwaitingUser:
			res.Reason = "awaiting user"
		default:
			res.Reason = "completed"
		}
		res.State, res.Window = "not observed", "not checked"
		return t.record(nil)
	}
	if loop.Limits.DeadlineReached(t.now) {
		d := t.decide("deadline", decide.Input{Control: gate, DeadlineReached: true})
		res.Action, res.Reason = string(d.Action), d.Reason
		res.State, res.Window = "not observed", "not checked"
		var actErr error
		if !t.opts.DryRun {
			actErr = t.act(nil, d)
		}
		return t.record(actErr)
	}
	if err := t.sendAllowance(); err != nil {
		res.State, res.Window = "not observed", "not checked"
		return t.record(t.sendAllowanceFailure(err))
	}
	if timeout := loop.Limits.NoProgressTimeout.Duration; timeout > 0 {
		p, err := t.observeProgress()
		if err != nil {
			res.State, res.Window, res.Reason = "not observed", "not checked", "progress allowance unavailable; no automatic provider work"
			return t.record(err)
		}
		t.progress = &p
		if p.Reached(t.now, timeout) {
			d := t.decide("progress_limit", decide.Input{Control: gate, NoProgressReached: true})
			res.Action, res.Reason = string(d.Action), d.Reason
			res.State, res.Window = "not observed", "not checked"
			var actErr error
			if !t.opts.DryRun {
				actErr = t.act(nil, d)
			}
			return t.record(actErr)
		}
	}
	if y := loop.Yield; y != nil {
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
	t.observedTarget = target
	obs, err := a.Observe(t.ctx)
	t.incidentObservation = incident.Observed(obs, agent.Adapter, err != nil)
	t.awaitingInput = obs.AwaitingInput
	if err != nil {
		res.State, res.Reason, res.Window = string(decide.Unknown), "could not verify state; no action taken", "not checked"
		t.report("error", fmt.Sprintf("%s: could not observe %s", loop.Name, target), map[string]any{"target": target})
		return t.record(err)
	}
	res.State = string(obs.State)
	if obs.State == decide.Error {
		res.ErrorClass = string(obs.ErrorClass)
	}
	if len(obs.AwaitingChildren) > 0 {
		// Escalate before probes, fingerprints, and recovery. Child input must
		// not depend on those succeeding, and never prompts the parent.
		t.awaitingChildren = obs.AwaitingChildren
		d := t.decide("child_input", decide.Input{Control: gate, Obs: obs})
		res.Action, res.Reason, res.Window = string(d.Action), d.Reason, "not checked"
		res.Reprompts, t.st.LastState = t.st.Reprompts, string(obs.State)
		var actErr error
		if !t.opts.DryRun {
			actErr = t.act(a, d)
		}
		return t.record(actErr)
	}
	if obs.DeferredInteraction {
		// Same precedence as child input: save the hold before probes, the
		// fingerprint or recovery can fail. The adapter is told only after the hold is saved, so a crash in
		// between raises it again instead of losing it.
		d := t.decide("normal", decide.Input{Control: gate, Obs: obs})
		res.Action, res.Reason, res.Window = string(d.Action), d.Reason, "not checked"
		res.Reprompts, t.st.LastState = t.st.Reprompts, string(obs.State)
		var actErr error
		if !t.opts.DryRun {
			// Another rule (a gone target) may own this hold; adopt only our own.
			t.deferredInteraction = d.DeferredInteraction
			actErr = t.act(a, d)
			if adopter, ok := a.(adapter.InteractionAdopter); ok && actErr == nil && t.deferredInteraction {
				actErr = adopter.AdoptInteraction(t.ctx)
			}
		}
		return t.record(actErr)
	}
	if obs.AwaitingInput && !t.st.AwaitingReported && !t.opts.DryRun {
		t.st.AwaitingReported = true
		t.report("await_user", fmt.Sprintf("%s: %s is waiting on a person (pending interaction)", loop.Name, target), map[string]any{"target": target, "pendingInteraction": true})
	} else if !obs.AwaitingInput {
		t.st.AwaitingReported = false
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
	// Kept to tell verified Git progress from a first baseline further down.
	priorFingerprint, priorProgressAt := t.st.Fingerprint, t.st.LastProgressAt

	// Fallback health: scheduled probes, then the redundancy picture.
	var summary health.Summary
	if t.fb != nil {
		if t.reg, err = health.Load(loop.StateDir); err != nil {
			return err
		}
		if inWindow && !t.opts.DryRun {
			t.scheduledProbes(target, agent.Group)
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
		var f string
		var err error
		if t.progress != nil {
			f = t.progress.Fingerprint
		} else {
			f, err = t.opts.Fingerprint(loop.Progress.Repositories)
		}
		if err != nil {
			res.Reason = "could not fingerprint repositories; no action taken"
			return t.record(err)
		}
		if f != t.st.Fingerprint || t.st.LastProgressAt.IsZero() {
			t.st.Fingerprint, t.st.LastProgressAt = f, t.now
			t.st.Nudges = 0
		}
		if t.progress != nil {
			t.st.LastProgressAt = t.progress.ChangedAt
		}
		unchangedFor = t.now.Sub(t.st.LastProgressAt)
		res.UnchangedFor = unchangedFor.Truncate(time.Second).String()
	}
	unchangedFor = t.stallAfterRetry(obs, target, unchangedFor)
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
	if obs.State != decide.Active {
		t.st.Nudges = 0 // the turn ended; a new one starts a fresh count
	}

	// Rejected handoff briefs cover one error episode. It ends on evidence that
	// the session recovered: a turn that finished without an error (idle), or
	// verified Git progress while active. Seeing the session active is not
	// enough: our own re-prompt, or a provider retry, makes it active, and a
	// turn that errors again must not hand every fallback a fresh allowance.
	gitProgress := priorFingerprint != "" && !priorProgressAt.IsZero() && t.st.Fingerprint != priorFingerprint
	if obs.State == decide.Idle || (obs.State == decide.Active && gitProgress) {
		t.st.HandoffBriefs = nil
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
		Failover: failover,
		Nudges:   t.st.Nudges, MaxNudges: loop.Progress.MaxNudges,
		Reprompts: t.st.Reprompts, MaxReprompts: loop.Errors.MaxReprompts,
		SinceLastReprompt: sinceReprompt, Backoff: loop.Errors.Backoff.Duration,
		SwitchBackReady: t.switchBackReady(target),
	}
	if failover.Eligible && obs.State == decide.Error && t.fb != nil && !summary.Meets {
		t.warn("handoff refused: %d of the required %d distinct healthy fallbacks", summary.Distinct, summary.Min)
	}
	// An errored child is not running work and changes no decision. The set is
	// announced in one wake, then again only when it changes. A verified empty
	// set forgets the last announcement, so a recurrence is announced again.
	t.erroredChildrenKey = decide.ErroredChildrenKey(obs.ErroredChildren)
	if t.erroredChildrenKey == "" && !obs.ChildrenUnverified {
		t.st.ErroredChildrenNoted = ""
	}
	res.ErroredChildren = len(obs.ErroredChildren)
	in.ErroredChildrenNoted = t.erroredChildrenKey != "" && t.erroredChildrenKey == t.st.ErroredChildrenNoted
	t.decisionInput = in
	d := t.decide("normal", in)
	res.Action, res.Reason = string(d.Action), d.Reason
	res.ChildErrorNote = decide.ChildErrorNote(in, d) > 0

	var actErr error
	if d.Action == decide.SwitchBack {
		if t.opts.DryRun {
			res.Handoff = &HandoffNote{From: target, To: state.Primary, Reason: "switch_back", DryRun: true}
		} else if moved, err := t.switchBack(target); err != nil {
			actErr = err
		} else if !moved {
			// The primary is not ready yet: keep the fallback working.
			in.SwitchBackReady = false
			t.decisionInput = in
			d = t.decide("switch_back_failed", in)
			res.ChildErrorNote = decide.ChildErrorNote(in, d) > 0
			res.Action, res.Reason = string(d.Action), d.Reason+" (primary failed its probe; staying on the fallback)"
			actErr = t.act(a, d)
		}
	} else if d.Action == decide.Handoff {
		moved, err := t.handoff(target, agent, obs.ErrorClass, summary)
		if moved && err == nil && !t.opts.DryRun && target == state.Primary {
			t.st.PrimaryResetsAt = obs.ResetsAt // switch-back waits for this
		}
		if err != nil {
			actErr = err
		} else if !moved {
			// No candidate took over: fall back to the ordinary error rules
			// for the current target. A dry run only previews that decision.
			in.Failover.Available = false
			d = t.decide("handoff_failed", in)
			why := " (no fallback could take over; see probes and warnings)"
			if res.Handoff != nil && len(res.Handoff.Tried) == 0 && len(res.Handoff.Skipped) > 0 {
				why = " (every confirmed fallback is capped or backing off after rejecting its handoff brief)"
			}
			// The hold note and report carry the explanation too.
			d.Reason += why
			res.Action, res.Reason = string(d.Action), d.Reason
			if !t.opts.DryRun {
				actErr = t.act(a, d)
			}
		}
	} else if !t.opts.DryRun {
		actErr = t.act(a, d)
	}
	if errors.Is(actErr, state.ErrSendLimit) || errors.Is(actErr, state.ErrSendLedger) {
		actErr = t.sendAllowanceFailure(actErr)
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

// stop handles a stop request before anything else: it does not depend on
// the loop being enabled or unpaused, on the window, on yield, or on the
// fingerprint and health files being readable.
func (t *tick) stop(target string, agent config.Agent, gate decide.Control) error {
	res := t.res
	res.Window = "not checked"
	a, err := t.opts.NewAdapter(agent, t.loop.StateDir, target)
	if err != nil {
		return err
	}
	t.observedTarget = target
	obs, err := a.Observe(t.ctx)
	t.incidentObservation = incident.Observed(obs, agent.Adapter, err != nil)
	t.awaitingInput = obs.AwaitingInput
	if err != nil {
		res.State, res.Reason = string(decide.Unknown), "stop requested; could not verify state"
		return t.record(err)
	}
	res.State = string(obs.State)
	d := t.decide("stop", decide.Input{Obs: obs, Control: gate})
	if d.Action == decide.Stop && t.st.StopRequestedSent {
		d = decide.Decision{Action: decide.None, Reason: "stop already requested"}
	}
	res.Action, res.Reason = string(d.Action), d.Reason
	var actErr error
	if !t.opts.DryRun && d.Action == decide.Stop {
		actErr = t.act(a, d)
	}
	t.st.LastState = string(obs.State)
	return t.record(actErr)
}

// scheduledProbes runs due probes. The active target and candidates in its
// quota group are skipped: the running session is already the evidence for
// that pool, and probing it would spend the quota the loop depends on.
func (t *tick) scheduledProbes(active, activeGroup string) {
	for _, c := range t.fb.Candidates {
		if c.ID == active || (activeGroup != "" && c.Group == activeGroup) {
			continue
		}
		due, _ := t.reg.Due(c, t.fb.Probe, t.now)
		if !due {
			continue
		}
		r := t.opts.Probe(t.ctx, c, probe.Scheduled, t.fb.Probe.Timeout.Duration)
		r.At, r.Tier = t.now, probe.Scheduled
		t.reg.Record(c.ID, r)
		t.keepFailure(c.ID, r)
		t.res.Probes = append(t.res.Probes, ProbeNote{ID: c.ID, Tier: r.Tier, OK: r.OK, ErrorClass: r.ErrorClass, Detail: r.Detail})
		t.incidentProbes.Add(c.ID, string(r.Tier), r.OK, r.ErrorClass)
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
		for _, id := range summary.Healthy {
			if v := decide.BriefGate(t.handoffBriefs(id), t.now, t.loop.Errors.MaxReprompts, t.loop.Errors.Backoff.Duration); v != decide.BriefAllowed {
				note.Skipped = append(note.Skipped, id+": "+string(v))
				continue
			}
			note.To = id
			return true, nil
		}
		// Nothing would be offered a brief: preview the fall-through instead.
		return len(summary.Healthy) == 0, nil
	}
	for _, id := range summary.Healthy {
		if err := t.sendAllowance(); err != nil {
			return false, err
		}
		// A fallback that keeps rejecting its brief is neither probed nor sent
		// to again until its own backoff passes, and not at all past its cap.
		if v := decide.BriefGate(t.handoffBriefs(id), t.now, t.loop.Errors.MaxReprompts, t.loop.Errors.Backoff.Duration); v != decide.BriefAllowed {
			note.Skipped = append(note.Skipped, id+": "+string(v))
			continue
		}
		c, _ := t.fb.Find(id)
		brief, err := handoff.ComposeBrief(t.loop, handoff.Facts{Loop: t.loop.Name, At: t.now, From: t.label(from), To: id + " (" + c.Describe() + ")", Reason: string(class)})
		if err != nil {
			return false, fmt.Errorf("handoff brief: %w", err)
		}
		r := t.opts.Probe(t.ctx, c, probe.Exact, t.fb.Probe.Timeout.Duration)
		r.At, r.Tier = t.now, probe.Exact
		t.reg.Record(id, r)
		t.keepFailure(id, r)
		t.res.Probes = append(t.res.Probes, ProbeNote{ID: id, Tier: r.Tier, OK: r.OK, ErrorClass: r.ErrorClass, Detail: r.Detail})
		t.incidentProbes.Add(id, string(r.Tier), r.OK, r.ErrorClass)
		note.Tried = append(note.Tried, id)
		if !r.OK {
			continue
		}
		ta, err := t.opts.NewAdapter(c.Agent, t.loop.StateDir, c.ID)
		if err != nil {
			t.warn("handoff to %s: %v", id, err)
			t.rejectHandoffBrief(id)
			continue
		}
		if act, ok := ta.(adapter.Activator); ok {
			if err := act.Activate(t.ctx); err != nil {
				t.warn("handoff to %s: activate: %v", id, err)
				t.rejectHandoffBrief(id)
				continue
			}
		}
		if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
			return false, err
		}
		if err := t.send(ta, brief, adapter.ModeAuto, id, "handoff"); err != nil {
			t.warn("handoff to %s: send: %v", id, err)
			t.rejectHandoffBrief(id)
			continue
		}
		t.st.ClearBriefs() // a new target, a new episode
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

func (t *tick) handoffBriefs(id string) decide.BriefAttempts {
	r := t.st.HandoffBriefs[id]
	return decide.BriefAttempts{Failed: r.Failed, LastAt: r.LastAt}
}

func (t *tick) rejectHandoffBrief(id string) {
	a := decide.BriefRejected(t.handoffBriefs(id), t.now)
	if t.st.HandoffBriefs == nil {
		t.st.HandoffBriefs = map[string]state.BriefRecord{}
	}
	t.st.HandoffBriefs[id] = state.BriefRecord{Failed: a.Failed, LastAt: a.LastAt}
}

// rejectSwitchBack counts a switch-back the primary did not accept after
// passing its probe, and reports once when that ends further attempts.
func (t *tick) rejectSwitchBack(from string) {
	t.st.SwitchBackRejected++
	if t.st.SwitchBackRejected == t.loop.Errors.MaxReprompts {
		t.report("switch_back", fmt.Sprintf("%s: switch-back stopped after the primary rejected %d briefs; staying on %s until resume, retarget or the next handoff", t.loop.Name, t.st.SwitchBackRejected, from),
			map[string]any{"from": from, "to": state.Primary, "stopped": true, "rejected": t.st.SwitchBackRejected})
	}
}

// switchBackReady reports whether the loop may try to return to its primary:
// switch-back is on, a fallback is active, the primary's reported reset time
// (or min_after since the handoff, when none was reported) has passed, and
// the last failed attempt is at least retry_every ago.
func (t *tick) switchBackReady(target string) bool {
	sb := t.loop.SwitchBack
	if !sb.Enabled || target == state.Primary || t.st.LastHandoffAt.IsZero() {
		return false
	}
	earliest := t.st.LastHandoffAt.Add(sb.MinAfter.Duration)
	if !t.st.PrimaryResetsAt.IsZero() {
		earliest = t.st.PrimaryResetsAt
	}
	if t.now.Before(earliest) {
		return false
	}
	// retry_every already spaces attempts; the cap ends them.
	if decide.BriefGate(decide.BriefAttempts{Failed: t.st.SwitchBackRejected}, t.now, t.loop.Errors.MaxReprompts, 0) == decide.BriefCapped {
		t.warn("switch-back stopped: the primary rejected %d switch-back briefs; staying on %s until resume, retarget or the next handoff", t.st.SwitchBackRejected, target)
		return false
	}
	return t.st.LastSwitchBackTry.IsZero() || t.now.Sub(t.st.LastSwitchBackTry) >= sb.RetryEvery.Duration
}

// switchBack confirms the primary with an exact probe, then activates it and
// sends it a brief. It returns false when the primary is not ready.
func (t *tick) switchBack(from string) (bool, error) {
	t.st.LastSwitchBackTry = t.now
	c := t.loop.PrimaryCandidate()
	timeout := config.DefaultProbeTimeout
	if t.fb != nil {
		timeout = t.fb.Probe.Timeout.Duration
	}
	r := t.opts.Probe(t.ctx, c, probe.Exact, timeout)
	r.At, r.Tier = t.now, probe.Exact
	if t.reg != nil {
		t.reg.Record(state.Primary, r)
	}
	t.keepFailure(state.Primary, r)
	t.res.Probes = append(t.res.Probes, ProbeNote{ID: state.Primary, Tier: r.Tier, OK: r.OK, ErrorClass: r.ErrorClass, Detail: r.Detail})
	t.incidentProbes.Add(state.Primary, string(r.Tier), r.OK, r.ErrorClass)
	if !r.OK {
		t.report("probe_failed", fmt.Sprintf("%s: switch-back probe of the primary failed (%s); staying on %s", t.loop.Name, r.ErrorClass, from), map[string]any{"candidate": state.Primary, "probe": r})
		return false, nil
	}
	brief, err := handoff.ComposeBrief(t.loop, handoff.Facts{Loop: t.loop.Name, At: t.now, From: t.label(from), To: "primary (" + t.loop.Describe() + ")", SwitchBack: true})
	if err != nil {
		return false, fmt.Errorf("switch-back brief: %w", err)
	}
	pa, err := t.opts.NewAdapter(t.loop.Agent, t.loop.StateDir, state.Primary)
	if err != nil {
		t.rejectSwitchBack(from)
		return false, err
	}
	if act, ok := pa.(adapter.Activator); ok {
		if err := act.Activate(t.ctx); err != nil {
			t.warn("switch-back: activate primary: %v", err)
			t.rejectSwitchBack(from)
			return false, nil
		}
	}
	if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
		return false, err
	}
	if err := t.send(pa, brief, adapter.ModeAuto, state.Primary, "switch_back"); err != nil {
		t.warn("switch-back: send to primary: %v", err)
		t.rejectSwitchBack(from)
		return false, nil
	}
	t.res.Handoff = &HandoffNote{From: from, To: state.Primary, Reason: "switch_back"}
	t.res.Target = state.Primary
	t.st.ActiveTarget, t.st.LastHandoffAt, t.st.LastWakeAt = state.Primary, t.now, t.now
	t.st.PrimaryResetsAt, t.st.LastSwitchBackTry = time.Time{}, time.Time{}
	t.st.ClearBriefs()
	t.st.Reprompts, t.st.LastRepromptAt, t.st.Nudges = 0, time.Time{}, 0
	t.report("switch_back", fmt.Sprintf("%s: switched back from %s to the primary", t.loop.Name, from), map[string]any{"from": from, "to": state.Primary})
	return true, nil
}

// keepFailure saves a failed probe's raw output for later mapping. Losing
// it is only a warning.
func (t *tick) keepFailure(id string, r probe.Result) {
	if err := probe.KeepFailure(t.loop.StateDir, id, r); err != nil {
		t.warn("could not keep failed probe output for %s: %v", id, err)
	}
}

func (t *tick) act(a adapter.Adapter, d decide.Decision) error {
	loop := t.loop
	switch d.Action {
	case decide.Wake, decide.Nudge, decide.Reprompt:
		text, err := prompt.ComposeWake(loop)
		if err != nil {
			return err
		}
		mode := adapter.ModeAuto
		if d.Action == decide.Nudge {
			mode = adapter.ModeQueue
		}
		childErrors := decide.ChildErrorNote(t.decisionInput, d)
		text = prompt.WithChildErrors(text, childErrors)
		if err := state.ReserveSend(loop.StateDir, loop.Limits.MaxSends); err != nil {
			return err
		}
		err = t.send(a, text, mode, t.res.Target, string(d.Action))
		if err == nil && childErrors > 0 {
			// Announced: this set is not mentioned again. A send that failed or
			// was unsupported leaves it for the next wake.
			t.st.ErroredChildrenNoted = t.erroredChildrenKey
		}
		// The adapter was called: count the attempt whatever it returned, so a
		// failing or unsupported send cannot repeat on every check outside the
		// per-episode caps and spacing.
		c := decide.Attempted(d.Action, decide.Counters{
			Nudges: t.st.Nudges, Reprompts: t.st.Reprompts, LastSendAt: t.st.LastWakeAt, LastRepromptAt: t.st.LastRepromptAt,
		}, t.now)
		t.st.Nudges, t.st.Reprompts, t.st.LastWakeAt, t.st.LastRepromptAt = c.Nudges, c.Reprompts, c.LastSendAt, c.LastRepromptAt
		if err != nil {
			var unsupported adapter.ErrUnsupported
			if errors.As(err, &unsupported) {
				t.warn("%s adapter: %v", a.Name(), err)
				return nil
			}
			return err
		}
		t.report(string(d.Action), fmt.Sprintf("%s: %s %s (%s)", loop.Name, d.Action, t.res.Target, d.Reason), nil)
	case decide.AwaitUser:
		if _, err := control.Update(loop.StateDir, func(f *control.File) { f.AwaitingUser, f.Note = true, d.Reason }); err != nil {
			return err
		}
		data := map[string]any{"target": t.res.Target}
		if len(t.awaitingChildren) > 0 {
			data["children"] = t.awaitingChildren
			data["pendingInteraction"] = true
		}
		if t.deferredInteraction {
			data["deferredInteraction"] = true
		}
		t.report("await_user", fmt.Sprintf("%s: needs a person: %s", loop.Name, d.Reason), data)
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

// send records only a returned adapter call. It is persisted with the completed
// tick, not a durable pre-send intent or an acknowledgement of agent execution.
func (t *tick) send(a adapter.Adapter, message prompt.Message, mode adapter.Mode, target, action string) error {
	err := a.Send(t.ctx, message.Text, mode)
	outcome := "adapter_returned"
	if err != nil {
		outcome = "adapter_error"
	}
	t.st.LastPromptSend = &state.PromptSend{
		Version: 1, CheckAt: t.now, Target: target, Action: action, Mode: string(mode), Outcome: outcome,
		GoalID: message.GoalID, GoalRevision: message.GoalRevision,
	}
	return err
}

func (t *tick) record(actErr error) error {
	t.st.CheckedAt = t.now
	outcome := "succeeded"
	if t.opts.DryRun {
		outcome = "dry_run"
	}
	if actErr != nil {
		outcome = "failed"
	}
	t.st.LastCheck = &state.Check{At: t.now, Version: t.opts.Version,
		Target: t.res.Target, ObservedTarget: t.observedTarget, ObservedState: t.res.State, AwaitingInput: t.awaitingInput, ErroredChildren: t.res.ErroredChildren,
		Action: t.res.Action, Reason: t.res.Reason, Outcome: outcome, DryRun: t.opts.DryRun}
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

// sendAllowance is checked before automatic provider work and each candidate.
// Reservations also re-read under the caller's tick lock immediately before Send.
func (t *tick) sendAllowance() error {
	if t.loop.Limits.MaxSends == 0 {
		return nil
	}
	used, err := state.LoadSends(t.loop.StateDir)
	if err != nil {
		return err
	}
	if used >= t.loop.Limits.MaxSends {
		return state.ErrSendLimit
	}
	return nil
}

func (t *tick) sendAllowanceFailure(err error) error {
	if !errors.Is(err, state.ErrSendLimit) {
		t.res.Action, t.res.Reason = string(decide.None), "send allowance unavailable; no further automatic work"
		return err
	}
	d := t.decide("send_limit", decide.Input{Control: decide.Control{Enabled: true}, SendLimitReached: true})
	t.res.Action, t.res.Reason = string(d.Action), d.Reason
	if t.opts.DryRun {
		return nil
	}
	return t.act(nil, d)
}

func (t *tick) observeProgress() (state.Progress, error) {
	previous, err := state.LoadProgress(t.loop.StateDir)
	if err != nil {
		return state.Progress{}, err
	}
	if previous != nil && t.now.Before(previous.ObservedAt) {
		return state.Progress{}, state.ErrProgressLedger
	}
	repositories := state.ProgressRepositories(t.loop.Progress.Repositories)
	fingerprint, err := t.opts.Fingerprint(repositories)
	if err != nil {
		return state.Progress{}, fmt.Errorf("could not verify Git progress; no automatic provider work")
	}
	return state.ObserveProgress(t.loop.StateDir, previous, state.ProgressSources(repositories), fingerprint, t.now, t.opts.DryRun)
}

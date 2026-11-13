// Package supervise runs one tick: observe, probe, decide, act, record.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
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
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
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
	// Clock reads wall time for the check's time budget. Now is the logical
	// check time and may be fixed; Clock always advances.
	Clock func() time.Time
	// Routine is the scheduler identity supplied with this check, if any. It
	// is recorded in the run graph only and never reaches a decision.
	Routine rd.RoutineRef
	// RoutineIgnored says the caller supplied an identity that could not be
	// recorded. The check runs as usual and says so once.
	RoutineIgnored bool
	// Threads creates and finds BB threads for handoffs to candidates with
	// bb.new_thread. Swapped in tests.
	Threads func(cli string) ThreadCreator
}

// ProbeNote summarises a probe that ran during the tick.
type ProbeNote struct {
	ID   string     `json:"id"`
	Tier probe.Tier `json:"tier"`
	OK   bool       `json:"ok"`
	// Skipped says why the probe produced no result: "tick_timeout" when it
	// was not started, "cut_short" when it was stopped by the check's time
	// budget before its own timeout. Neither is recorded as a probe failure.
	Skipped    string `json:"skipped,omitempty"`
	ErrorClass string `json:"errorClass,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// HandoffNote describes a handoff attempt.
type HandoffNote struct {
	From   string   `json:"from"`
	To     string   `json:"to,omitempty"`
	Reason string   `json:"reason"`
	Tried  []string `json:"tried,omitempty"`
	// NewThread is the BB thread keepgoing created for this handoff, and
	// Continues the thread of the conversation it continues. Reconciled says
	// the creation was found by a later check, not returned by bb at the time.
	// WouldCreateThread is the dry-run preview of a creation.
	NewThread         string `json:"newThread,omitempty"`
	Continues         string `json:"continues,omitempty"`
	Reconciled        bool   `json:"reconciled,omitempty"`
	WouldCreateThread bool   `json:"wouldCreateThread,omitempty"`
	// Skipped lists fallbacks not probed this check because of rejected
	// briefs, as "id: backing_off" or "id: capped".
	Skipped []string `json:"skipped,omitempty"`
	DryRun  bool     `json:"dryRun,omitempty"`
}

// Result is what a tick did, printed as JSON by the CLI.
type Result struct {
	// RetrySignal is "expired" when a stale provider retry notice was seen
	// and not honored, so ordinary error or stall handling applied.
	RetrySignal  string     `json:"retrySignal,omitempty"`
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
	// Recovery says how a re-prompt was carried out: turn_retry re-ran the
	// failed turn by reference with no new text, prompt sent the wake text.
	Recovery string `json:"recovery,omitempty"`
	// ErroredChildren is how many direct child threads were in error.
	// ErroredChildrenPartial: child discovery was incomplete, so the count is
	// a lower bound. ChildErrorNote is set when this check's wake announces
	// them. ChildErrorNoticeStopped: the notice was given up after repeated
	// rejected sends.
	ErroredChildren         int             `json:"erroredChildren,omitempty"`
	ErroredChildrenPartial  bool            `json:"erroredChildrenPartial,omitempty"`
	ChildErrorNote          bool            `json:"childErrorNote,omitempty"`
	ChildErrorNoticeStopped bool            `json:"childErrorNoticeStopped,omitempty"`
	Handoff                 *HandoffNote    `json:"handoff,omitempty"`
	Redundancy              *health.Summary `json:"redundancy,omitempty"`
	Probes                  []ProbeNote     `json:"probes,omitempty"`
	Yielded                 bool            `json:"yielded,omitempty"`
	DryRun                  bool            `json:"dryRun,omitempty"`
	Warnings                []string        `json:"warnings,omitempty"`
	// Outcome is the recorded result of the check: succeeded, failed or
	// dry_run. It is empty when the check could not be recorded at all.
	Outcome string `json:"outcome,omitempty"`
	// MissingWorktrees counts registered Git worktrees left out of the
	// progress fingerprint because their directories are gone.
	MissingWorktrees int `json:"missingWorktrees,omitempty"`
	// Recorded: state, the event line and incident history were all written.
	Recorded bool `json:"-"`
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
	// incidentLimitWaitReport is set only by the check that detects a long limit wait.
	incidentLimitWaitReport string
	// incidentTurnRetry is set only by a check that asked the provider to retry a failed turn.
	incidentTurnRetry string
	// handoffAvailable is the last engine input's view of whether a confirmed
	// fallback could take the loop.
	handoffAvailable bool
	progress         *state.Progress
	observedTarget   string
	awaitingInput    bool
	awaitingChildren []string
	// decisionInput is the input of the latest ordinary decision. childNoticeKept
	// and childNoticeNew are the announced record and the newly errored
	// children it saw; act uses them to decide whether a wake carries the
	// child-error notice and what to record.
	// newThreadCapped: a handoff needed a new thread and the loop had created
	// its maximum. newThreadUnresolved: a creation's outcome is unknown.
	// incidentNewThread: this check recorded a created thread.
	newThreadCapped     bool
	newThreadUnresolved bool
	incidentNewThread   bool
	decisionInput       decide.Input
	childNoticeKept     []decide.CoveredChild
	childNoticeNew      []string
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
	deadline            time.Time     // wall time by which the check must end
	reserve             time.Duration // kept back for mandatory steps still to come
	probesDeferred      bool          // a needed probe was skipped for lack of time
	switchBackDeferred  bool
	budgetWarned        bool
	observeSpent        time.Duration // observation time used by a lookup for an unsettled thread creation
	lateReports         int           // reports started after the budget was spent
	lateProbed          bool
	graphFailed         bool
	graphClosed         bool
	// waiting is the child picture this check observed, for the run graph.
	waiting *graphWaiting
}

func (t *tick) warn(format string, a ...any) {
	t.res.Warnings = append(t.res.Warnings, fmt.Sprintf(format, a...))
}

func (t *tick) report(typ, text string, data map[string]any) {
	if t.opts.DryRun {
		return
	}
	ctx, cancel, ok := t.reportContext(typ, text)
	if !ok {
		return
	}
	defer cancel()
	if err := t.opts.Reporter.Report(ctx, report.Event{Type: typ, Loop: t.loop.Name, Text: text, At: t.now, Data: data}); err != nil {
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
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Threads == nil {
		opts.Threads = bbThreads
	}
	now := opts.Now.UTC()
	res := Result{At: now, Loop: cfg.Loop.Name, Action: string(decide.None), DryRun: opts.DryRun}
	t := &tick{ctx: ctx, cfg: cfg, loop: cfg.Loop, fb: cfg.FallbackSet, opts: opts, now: now, res: &res}
	t.deadline = opts.Clock().Add(tickBudget(cfg.Loop) + checkTail)
	t.reserve = reserveFingerprint + reserveSend + reserveReports
	if opts.RoutineIgnored {
		t.warn("routine identity ignored: --routine takes source/routine or source/routine/invocation; this check ran without it")
	}
	t.graphSync()
	err := t.run()
	t.graphClose()
	if journalErr := t.recordIncident(err != nil); journalErr != nil {
		t.warn("incident history write failed; diagnostic evidence is incomplete")
		err = errors.Join(err, journalErr)
		res.Recorded = false
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
	if isNewThread(c) {
		// Its thread is the one created for it at handoff.
		a, ok := t.st.CandidateAgent(c)
		if !ok {
			t.warn("active target %q has no created thread on record; returning to primary", target)
			return state.Primary, t.loop.Agent
		}
		return c.ID, a
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

	// A thread creation whose outcome was never recorded is settled first: if
	// the thread exists, the handoff it was created for is completed now.
	moved, held := t.reconcileNewThread()
	if held {
		res.State, res.Window = "not observed", "not checked"
		return t.record(nil)
	}
	if moved {
		target, agent = t.resolve(t.st.Target())
		res.Target, t.incidentTarget, t.incidentAdapter = target, target, agent.Adapter
	}
	a, err := t.newAdapter(agent, loop.StateDir, target)
	if err != nil {
		res.State, res.Window, res.Reason = "not observed", "not checked", "could not build the target's adapter; no action taken"
		return t.record(err)
	}
	t.observedTarget = target
	obs, err := t.observe(a)
	t.graphChildren(agent.Adapter, target, obs, err != nil)
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
	if obs.RetryExpired {
		res.RetrySignal = "expired"
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

	// Fallback health: scheduled probes, then the redundancy picture.
	var summary health.Summary
	handoffFirst := false
	if t.fb != nil {
		if t.reg, err = health.Load(loop.StateDir); err != nil {
			t.reg = nil
			res.Reason = "could not read fallback health; no action taken"
			return t.record(err)
		}
		// When a handoff looks due on what is already known, the budget goes
		// to the exact probes that handoff needs; the scheduled probes wait
		// until after the decision, and run then if no handoff was attempted.
		handoffFirst = obs.State == decide.Error && contains(loop.Errors.FailoverOn, string(obs.ErrorClass)) &&
			health.Evaluate(t.fb, t.reg, t.now, target, agent.Group).Meets
		if inWindow && !t.opts.DryRun && !handoffFirst {
			t.scheduledProbes(target, agent.Group)
		}
		summary = t.noteRedundancy(target, agent.Group)
	}

	unchangedFor := time.Duration(0)
	progressed := false // verified Git metadata changed since the last check
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
		if n := state.MissingWorktrees(f); n > 0 {
			t.warn("%d registered Git worktree(s) no longer exist on disk and were left out of the progress fingerprint; run `git worktree prune` in the repository", n)
			res.MissingWorktrees = n
		}
		// A first baseline is not progress.
		// A worktree that disappeared, returned or was newly registered is not
		// progress either; the newest observation is kept so later changes are.
		changed := !state.SameProgress(t.st.Fingerprint, f)
		progressed = t.st.Fingerprint != "" && !t.st.LastProgressAt.IsZero() && changed
		previousFingerprint := t.st.Fingerprint
		t.st.Fingerprint = state.Remember(previousFingerprint, f)
		if previousFingerprint == "" || changed || t.st.LastProgressAt.IsZero() {
			t.st.LastProgressAt = t.now
			t.st.Nudges = 0
		}
		if t.progress != nil {
			t.st.LastProgressAt = t.progress.ChangedAt
		}
		unchangedFor = t.now.Sub(t.st.LastProgressAt)
		res.UnchangedFor = unchangedFor.Truncate(time.Second).String()
	}
	t.reserve = reserveSend + reserveReports // the fingerprint has been taken
	unchangedFor = t.stallAfterRetry(obs, target, unchangedFor)
	sinceWake := 100 * 365 * 24 * time.Hour
	if !t.st.LastWakeAt.IsZero() {
		sinceWake = t.now.Sub(t.st.LastWakeAt)
	}
	sinceReprompt := time.Duration(0)
	if !t.st.LastRepromptAt.IsZero() {
		sinceReprompt = t.now.Sub(t.st.LastRepromptAt)
	}
	// One error episode covers the re-prompt count and the rejected handoff
	// briefs. It ends on evidence that the session recovered: a turn finished
	// without an error (idle), or verified Git progress while active. Merely
	// seeing the session active does not end it: our own re-prompt, or a
	// provider retry, makes it active, and a turn that errors again must spend
	// the same allowances, or neither cap is ever reached.
	if obs.State == decide.Idle || (obs.State == decide.Active && progressed) {
		t.st.Reprompts, t.st.LastRepromptAt = 0, time.Time{}
		t.st.HandoffBriefs = nil
		// Handoffs put off for lack of time belong to the same episode. A
		// check that merely sees the session active must not clear them, or a
		// session alternating between error and active is put off forever.
		t.st.ClearDeferrals()
	}
	if obs.State != decide.Active {
		t.st.Nudges = 0 // the turn ended; a new one starts a fresh count
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
		ProgressTracked: len(loop.Progress.Repositories) > 0, RetrySilentFor: t.retrySilentFor(obs, target),
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
	// An errored child is not running work. The session is told when a child
	// newly enters error. The record and the rule are the ones the owner
	// report uses: only growth is new, and a covered child is forgotten only
	// after it has stayed out of error for min_wake_interval, so one that
	// flaps between error and a provider retry is announced once.
	if len(t.st.ChildErrorsAnnouncedV1) > 0 {
		// A record written before recovery times existed: carry it over as
		// children last seen in error now.
		if len(t.st.ChildErrorsAnnounced) == 0 {
			t.st.ChildErrorsAnnounced = decide.CoverChildren(nil, t.st.ChildErrorsAnnouncedV1, t.now, childErrorMaxCovered)
		}
		t.st.ChildErrorsAnnouncedV1 = nil
	}
	kept, unannounced := decide.CoveredChildren(decide.ChildHashes(obs.ErroredChildren), decide.ChildHashes(obs.HealthyChildren),
		t.st.ChildErrorsAnnounced, !obs.ChildrenUnverified, t.now, loop.Progress.MinWakeInterval.Duration)
	t.childNoticeKept, t.childNoticeNew = kept, unannounced
	if !t.opts.DryRun {
		t.st.ChildErrorsAnnounced = kept
	}
	if !obs.ChildrenUnverified {
		// Rejected sends are counted per announcement: a newly errored child
		// is a new one.
		if pending := strings.Join(unannounced, ","); pending != t.st.ChildErrorNoticeFor {
			t.st.ChildErrorNoticeFor, t.st.ChildErrorNoticeAttempts = pending, 0
		}
	}
	res.ErroredChildren, res.ErroredChildrenPartial = len(obs.ErroredChildren), obs.ChildrenUnverified && len(obs.ErroredChildren) > 0
	in.UnannouncedErroredChildren = len(unannounced)
	in.ChildErrorNoticeAttempts, in.MaxChildErrorNotices = t.st.ChildErrorNoticeAttempts, loop.Errors.MaxReprompts
	res.ChildErrorNoticeStopped = len(unannounced) > 0 && in.ChildErrorNoticeAttempts >= in.MaxChildErrorNotices
	t.decisionInput = in
	d := t.decide("normal", in)
	handoffAttempted := d.Action == decide.Handoff
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
			why := " (primary failed its probe; staying on the fallback)"
			if t.switchBackDeferred {
				why = " (no time left in this check to confirm the primary; staying on the fallback)"
			}
			res.Action, res.Reason = string(d.Action), d.Reason+why
			actErr = t.act(a, d)
		}
	} else if d.Action == decide.Handoff {
		moved, err := t.handoff(target, agent, obs, summary)
		if moved && err == nil && !t.opts.DryRun && target == state.Primary {
			t.st.PrimaryResetsAt = obs.ResetsAt // switch-back waits for this
		}
		if err != nil {
			actErr = err
		} else if !moved {
			// No candidate took over: fall back to the ordinary error rules
			// for the current target. A dry run only previews that decision.
			why := " (no fallback could take over; see probes and warnings)"
			if t.probesDeferred {
				t.st.HandoffDeferrals++
			}
			switch {
			case t.probesDeferred && t.st.HandoffDeferrals <= t.deferralCap():
				// A fallback could not be confirmed because the check ran out
				// of time, not because it failed. Spend nothing on that: no
				// re-prompt and no hold. The next check retries the handoff.
				in.HandoffDeferred = true
				t.decisionInput = in
				d = t.decide("handoff_deferred", in)
				why = " (this check ran out of time)"
			case t.probesDeferred:
				// Too many such checks in this error episode: stop waiting on
				// the budget and let the ordinary error rules act on the
				// current target.
				in.Failover.Available = false
				d = t.decide("handoff_failed", in)
				why = fmt.Sprintf(" (no fallback could be confirmed within the check's time budget in %d checks of this error episode; see probes and warnings)", t.st.HandoffDeferrals)
			case t.newThreadCapped:
				// A fallback that starts a new thread was needed and the loop
				// has created its maximum: a person decides, not a re-prompt.
				d = decide.Decision{Action: decide.AwaitUser, Reason: decide.NewThreadCapReason}
				why = ""
			case t.newThreadUnresolved && t.st.NewThreadIntent != nil:
				// A created thread may exist and could not be found. Nothing
				// more is created or sent anywhere: a person is asked.
				d = decide.Decision{Action: decide.AwaitUser, Reason: unsettledReason(t.st.NewThreadIntent)}
				why = ""
			case handoffFirst && inWindow && !t.opts.DryRun && t.lateScheduledProbes(target, agent.Group) && t.st.HandoffDeferrals < t.deferralCap():
				// The fallbacks that were confirmed did not take over, and the
				// ones due for a scheduled probe had been held back for them.
				// Those have now been probed. Do not spend a re-prompt before
				// the next check has seen the result.
				t.st.HandoffDeferrals++
				in.HandoffDeferred = true
				t.decisionInput = in
				d = t.decide("handoff_deferred", in)
				why = " (the confirmed fallbacks did not take over; fallbacks due for a scheduled probe had been held back for them and are probed before a re-prompt is spent)"
			default:
				in.Failover.Available = false
				d = t.decide("handoff_failed", in)
				if res.Handoff != nil && len(res.Handoff.Tried) == 0 && len(res.Handoff.Skipped) > 0 {
					why = " (every confirmed fallback is capped or backing off after rejecting its handoff brief)"
				}
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
	if t.opts.DryRun && res.Action == string(decide.Reprompt) {
		// Preview the method only; a dry run asks the provider for nothing.
		_, canRetry := a.(adapter.TurnRetrier)
		res.Recovery = string(decide.RecoverBy(decide.Decision{Action: decide.Reprompt}, obs, canRetry))
	}
	if errors.Is(actErr, state.ErrSendLimit) || errors.Is(actErr, state.ErrSendLedger) {
		actErr = t.sendAllowanceFailure(actErr)
	}
	t.reserve = reserveReports // the action, if any, has been taken
	if handoffFirst && !handoffAttempted && inWindow && !t.opts.DryRun && t.reg != nil {
		// The handoff did not happen after all (a limit wait, a hold): the
		// scheduled probes it was put ahead of run now.
		t.lateScheduledProbes(t.st.Target(), agent.Group)
	}
	if t.reg != nil && !t.opts.DryRun {
		if err := t.reg.Save(loop.StateDir); err != nil && actErr == nil {
			actErr = err
		}
	}
	t.reportLongLimitWait(target, obs)
	t.reportChildErrors(target, obs)
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
	a, err := t.newAdapter(agent, t.loop.StateDir, target)
	if err != nil {
		res.State, res.Reason = "not observed", "stop requested; could not build the target's adapter"
		return t.record(err)
	}
	t.observedTarget = target
	obs, err := t.observe(a)
	t.graphChildren(agent.Adapter, target, obs, err != nil)
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
func (t *tick) scheduledProbes(active, activeGroup string) (ran int) {
	for _, c := range t.fb.Candidates {
		if c.ID == active || (activeGroup != "" && c.Group == activeGroup) {
			continue
		}
		due, _ := t.reg.Due(c, t.fb.Probe, t.now)
		if !due {
			continue
		}
		// Scheduled probes are optional and run before the decision. One is
		// started only if its whole timeout fits before the time kept back for
		// deciding, sending and reporting, so it is never shortened: a probe
		// that runs to its timeout is a real failure, recorded and counted,
		// and one that does not fit stays due for the next check.
		if t.left() < t.fb.Probe.Timeout.Duration {
			t.skipProbe(c.ID, probe.Scheduled, "tick_timeout")
			continue
		}
		r := t.opts.Probe(t.ctx, c, probe.Scheduled, t.fb.Probe.Timeout.Duration)
		r.At, r.Tier = t.now, probe.Scheduled
		t.reg.Record(c.ID, r)
		ran++
		t.keepFailure(c.ID, r)
		t.res.Probes = append(t.res.Probes, ProbeNote{ID: c.ID, Tier: r.Tier, OK: r.OK, ErrorClass: r.ErrorClass, Detail: r.Detail})
		t.incidentProbes.Add(c.ID, string(r.Tier), r.OK, r.ErrorClass)
		if !r.OK {
			t.report("probe_failed", fmt.Sprintf("%s: scheduled probe of %s failed (%s)", t.loop.Name, c.ID, r.ErrorClass), map[string]any{"candidate": c.ID, "probe": r})
		}
	}
	return ran
}

// handoff tries healthy candidates in order. Each must pass an exact-target
// probe right now before it receives the brief.
// childrenLeft is what the brief says about the children of the conversation
// being left. A candidate in the same BB thread continues that conversation:
// its children stay listed, so nothing is said.
func childrenLeft(obs decide.Observation, from config.Agent, to config.Candidate) handoff.ChildrenLeft {
	if from.BB != nil && to.Agent.BB != nil && !to.Agent.BB.NewThread && from.BB.Thread != "" && from.BB.Thread == to.Agent.BB.Thread {
		return handoff.ChildrenLeft{}
	}
	return handoff.ChildrenLeft{Working: obs.ActiveChildren, Unverified: obs.ChildrenUnverified}
}

func (t *tick) handoff(from string, fromAgent config.Agent, obs decide.Observation, summary health.Summary) (bool, error) {
	class := obs.ErrorClass
	note := &HandoffNote{From: from, Reason: string(class), DryRun: t.opts.DryRun}
	t.res.Handoff = note
	if t.opts.DryRun {
		for _, id := range summary.Healthy {
			if v := decide.BriefGate(t.handoffBriefs(id), t.now, t.loop.Errors.MaxReprompts, t.loop.Errors.Backoff.Duration); v != decide.BriefAllowed {
				note.Skipped = append(note.Skipped, id+": "+string(v))
				continue
			}
			if c, _ := t.fb.Find(id); isNewThread(c) {
				if why := t.newThreadGate(c); why != "" {
					note.Skipped = append(note.Skipped, id+": "+why)
					continue
				}
				note.WouldCreateThread = true // a preview: nothing is created
			}
			note.To = id
			return true, nil
		}
		// Nothing would be offered a brief: preview the fall-through instead.
		return len(summary.Healthy) == 0, nil
	}
	if t.st.NewThreadIntent != nil {
		// A thread may already exist for an earlier attempt at this handoff.
		// Handing the loop to anything now could start a second conversation.
		t.newThreadUnresolved = true
		return false, nil
	}
	// Fallbacks whose exact probe was stopped early by the budget go last, so
	// one that hangs cannot keep a working one from being tried.
	order := append([]string(nil), summary.Healthy...)
	sort.SliceStable(order, func(i, j int) bool { return t.st.ExactCut[order[i]] < t.st.ExactCut[order[j]] })
	for _, id := range order {
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
		// A candidate that starts a new thread is skipped, unprobed, when the
		// loop has created its maximum or an earlier creation is unsettled.
		if why := t.newThreadGate(c); why != "" {
			note.Skipped = append(note.Skipped, id+": "+why)
			continue
		}
		brief, err := handoff.ComposeBrief(t.loop, handoff.Facts{Loop: t.loop.Name, At: t.now, From: t.label(from), To: id + " (" + c.Describe() + ")", Reason: string(class), Children: childrenLeft(obs, fromAgent, c)})
		if err != nil {
			return false, fmt.Errorf("handoff brief: %w", err)
		}
		timeout, ok := t.probeBudget(t.fb.Probe.Timeout.Duration)
		if !ok {
			// No time for this or any later candidate.
			t.skipProbe(id, probe.Exact, "tick_timeout")
			t.probesDeferred = true
			break
		}
		r := t.opts.Probe(t.ctx, c, probe.Exact, timeout)
		if r.TimedOut && timeout < t.fb.Probe.Timeout.Duration && t.st.ExactCut[id]+1 < t.deferralCap() {
			// Stopped early by the budget: that says little about the
			// candidate. Try the next one now and this one last next time.
			if t.st.ExactCut == nil {
				t.st.ExactCut = map[string]int{}
			}
			t.st.ExactCut[id]++
			t.skipProbe(id, probe.Exact, "cut_short")
			t.probesDeferred = true
			continue
		}
		// An answer, a full timeout, or one early stop too many: a real result.
		delete(t.st.ExactCut, id)
		r.At, r.Tier = t.now, probe.Exact
		t.reg.Record(id, r)
		t.keepFailure(id, r)
		t.res.Probes = append(t.res.Probes, ProbeNote{ID: id, Tier: r.Tier, OK: r.OK, ErrorClass: r.ErrorClass, Detail: r.Detail})
		t.incidentProbes.Add(id, string(r.Tier), r.OK, r.ErrorClass)
		note.Tried = append(note.Tried, id)
		if !r.OK {
			continue
		}
		if isNewThread(c) {
			// The brief is the new thread's first prompt: there is nothing to
			// activate and nothing more to send.
			thread, err := t.createThread(from, fromAgent, c, brief, string(class))
			switch {
			case errors.Is(err, state.ErrSendLimit) || errors.Is(err, state.ErrSendLedger):
				return false, err
			case errors.Is(err, errNewThreadUnsettled):
				// A thread may exist and was not found. Creating another, or
				// handing the loop to a different fallback, could put two
				// conversations on the task: a person decides.
				t.newThreadUnresolved = true
				t.reserve = reserveReports // the creation has been attempted
				return false, nil
			case err != nil:
				t.warn("handoff to %s: new thread not created: %v", id, err)
				t.rejectHandoffBrief(id)
				continue
			}
			t.reserve = reserveReports // the brief has been sent, as the first prompt
			t.moveToCreated(&state.NewThreadIntent{Candidate: id, From: from, Continues: t.st.NewThreads[id].Continues, Reason: string(class)}, thread, note)
			return true, nil
		}
		ta, err := t.newAdapter(c.Agent, t.loop.StateDir, c.ID)
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
		t.st.ClearChildErrors()
		note.To = id
		t.st.ActiveTarget, t.st.LastHandoffAt, t.st.LastWakeAt = id, t.now, t.now
		t.st.Handoffs++
		t.st.Reprompts, t.st.LastRepromptAt = 0, time.Time{}
		t.res.Target = id
		t.reserve = reserveReports // the brief has been sent
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
	c := t.loop.PrimaryCandidate()
	configured := config.DefaultProbeTimeout
	if t.fb != nil {
		configured = t.fb.Probe.Timeout.Duration
	}
	// Out of time: leave the retry spacing alone so the next check tries.
	// A switch-back is optional: it runs only when the whole probe fits.
	if t.left() < configured {
		t.skipProbe(state.Primary, probe.Exact, "tick_timeout")
		t.switchBackDeferred = true
		return false, nil
	}
	r := t.opts.Probe(t.ctx, c, probe.Exact, configured)
	t.st.LastSwitchBackTry = t.now
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
	pa, err := t.newAdapter(t.loop.Agent, t.loop.StateDir, state.Primary)
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
	t.st.ClearChildErrors()
	t.st.Reprompts, t.st.LastRepromptAt, t.st.Nudges = 0, time.Time{}, 0
	t.reserve = reserveReports // the brief has been sent
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
		reserved := false
		if r, ok := a.(adapter.TurnRetrier); ok && decide.RecoverBy(d, t.decisionInput.Obs, true) == decide.RecoverByTurnRetry {
			done, err := t.retryTurn(a, r, d)
			if done {
				return err
			}
			// The provider reported no failed turn and the session is still in
			// error, or the provider cannot retry a turn: fall back to the
			// prompt, under the send already reserved for this recovery.
			reserved = true
		}
		text, err := prompt.ComposeWake(loop)
		if err != nil {
			return err
		}
		mode := adapter.ModeAuto
		if d.Action == decide.Nudge {
			mode = adapter.ModeQueue
		}
		childErrors := childErrorNote(t.decisionInput, d)
		if d.ChildErrorWake {
			// Notice only: the parent is waiting on other children on purpose.
			text = prompt.ChildErrorsOnly(childErrors, t.decisionInput.Obs.ActiveChildren)
		} else {
			text = prompt.WithChildErrors(text, childErrors)
		}
		if !reserved {
			if err := state.ReserveSend(loop.StateDir, loop.Limits.MaxSends); err != nil {
				return err
			}
		}
		t.res.Recovery = ""
		if d.Action == decide.Reprompt {
			t.res.Recovery = string(decide.RecoverByPrompt)
		}
		err = t.send(a, text, mode, t.res.Target, string(d.Action))
		t.reserve = reserveReports // the send has been made
		t.childNoticeSent(childErrors, err)
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

// childErrorNote is the rule for carrying the errored-children notice on a
// send. It is a variable so a test can exercise a rule under which a
// re-prompt carries the notice.
var childErrorNote = decide.ChildErrorNote

// childNoticeSent records the result of a send that carried the notice about
// errored children. childErrors is zero when the send carried none.
func (t *tick) childNoticeSent(childErrors int, err error) {
	switch {
	case childErrors == 0:
	case err == nil:
		// Announced: the newly errored children are recorded as told.
		t.st.ChildErrorsAnnounced = decide.CoverChildren(t.childNoticeKept, t.childNoticeNew, t.now, childErrorMaxCovered)
		t.st.ChildErrorNoticeFor, t.st.ChildErrorNoticeAttempts = "", 0
	default:
		// Rejected (failed or unsupported): try again on a later wake, up to
		// the cap. It may have been delivered, so it must not repeat without
		// bound.
		t.st.ChildErrorNoticeAttempts++
	}
}

// send records only a returned adapter call. It is persisted with the completed
// tick, not a durable pre-send intent or an acknowledgement of agent execution.
func (t *tick) send(a adapter.Adapter, message prompt.Message, mode adapter.Mode, target, action string) error {
	attempt := t.graphBegin(target, action, string(mode), message)
	err := a.Send(t.ctx, message.Text, mode)
	outcome := "adapter_returned"
	if err != nil {
		outcome = "adapter_error"
	}
	t.graphFinish(attempt, outcome)
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
	// Consecutive failed checks, so a loop that keeps running without
	// completing a check is visible. A dry run changes nothing.
	switch outcome {
	case "failed":
		if t.st.FailedChecks == 0 {
			t.st.FailedSince = t.now
		}
		t.st.FailedChecks++
	case "succeeded":
		t.st.FailedChecks, t.st.FailedSince = 0, time.Time{}
	}
	t.st.LastCheck = &state.Check{At: t.now, Version: t.opts.Version,
		Target: t.res.Target, ObservedTarget: t.observedTarget, ObservedState: t.res.State, AwaitingInput: t.awaitingInput, RetrySignal: t.res.RetrySignal, ErroredChildren: t.res.ErroredChildren, ErroredChildrenPartial: t.res.ErroredChildrenPartial, ChildErrorNoticeStopped: t.res.ChildErrorNoticeStopped,
		Action: t.res.Action, Reason: t.res.Reason, Outcome: outcome, DryRun: t.opts.DryRun}
	if err := state.Save(t.loop.StateDir, t.st); err != nil {
		return err
	}
	// After the state save, so graph work never delays it, and before the
	// event is built, so a graph warning reaches runs.jsonl.
	t.graphClose()
	ev := state.Event{At: t.now, Loop: t.loop.Name, Event: "tick", Action: t.res.Action, Reason: t.res.Reason, State: t.res.State, Target: t.res.Target, Outcome: outcome}
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
	t.res.Outcome, t.res.Recorded = outcome, true
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

// newAdapter builds an adapter and gives it this check's time.
func (t *tick) newAdapter(a config.Agent, stateDir, target string) (adapter.Adapter, error) {
	ad, err := t.opts.NewAdapter(a, stateDir, target)
	if c, ok := ad.(adapter.CheckTimer); ok && err == nil {
		c.CheckAt(t.now)
	}
	return ad, err
}

// Time accounting for one check.
//
// A check has a hard limit: loop.tick_timeout plus checkTail. Every step is
// either mandatory, with a fixed maximum that is reserved for it until it has
// run, or optional, and then it runs only in time that no later mandatory
// step has reserved. So whatever combination of steps is slow, the check
// ends by the hard limit, apart from the few seconds a killed command is
// given to exit.
//
//	mandatory            maximum   reserved until
//	yield command        10s       (runs first)
//	observation          90s       (runs first; includes up to 20s looking for
//	                               a thread whose creation is unsettled)
//	Git fingerprint      60s       the fingerprint has been taken
//	activate and send    60s       the action has been taken
//	  (or, for a handoff to a new thread: read the place 10s, bb thread
//	  spawn 30s, look the thread up by title 20s)
//	essential reports    30s       the end (three of 10s each)
//
// Optional steps are scheduled probes, exact probes and routine reports.
const (
	minProbeTime  = 10 * time.Second
	minReportTime = 10 * time.Second
	// observeTimeout bounds one whole observation, however many commands it
	// runs: a hung bb or an endless log cannot hold the check open.
	observeTimeout = 90 * time.Second
	// checkTail is added to loop.tick_timeout to give the hard limit.
	checkTail          = config.TickTail
	reserveFingerprint = state.FingerprintTimeout
	reserveSend        = 60 * time.Second
	reserveReports     = maxLateReports * minReportTime
)

// tickBudget is the configured time budget of a check.
func tickBudget(l config.Loop) time.Duration {
	if d := l.TickTimeout.Duration; d > 0 {
		return d
	}
	return config.DefaultTickTimeout
}

// left is the time an optional step may use now: what remains before the
// hard limit, less what the mandatory steps still to come have reserved.
func (t *tick) left() time.Duration {
	return t.deadline.Sub(t.opts.Clock()) - t.reserve
}

// probeBudget returns the timeout a probe may use now, at most configured.
// ok is false when too little remains for the probe to be worth starting.
func (t *tick) probeBudget(configured time.Duration) (time.Duration, bool) {
	left := t.left()
	if left < minProbeTime {
		return 0, false
	}
	return min(configured, left), true
}

// deferralCap is how many checks of one error episode may put a handoff off,
// and how many early stops one fallback's exact probe gets, before the result
// is treated as real. The counts end with the episode (see the re-prompt
// count), not with any check in between, so they need not be consecutive. It
// reuses the re-prompt cap.
func (t *tick) deferralCap() int {
	return max(t.loop.Errors.MaxReprompts, 1)
}

// skipProbe notes a probe that produced no result. It is not recorded in the
// health registry, the probe cap or incident history as a failure.
func (t *tick) skipProbe(id string, tier probe.Tier, why string) {
	t.res.Probes = append(t.res.Probes, ProbeNote{ID: id, Tier: tier, Skipped: why})
	if !t.budgetWarned && why == "tick_timeout" {
		t.budgetWarned = true
		t.warn("the check's time budget (loop.tick_timeout) left no room for every fallback probe; skipped probes stay due")
	}
}

// observe reads the target's state within observeTimeout. When the limit is
// reached the adapter's commands are cancelled and the observation fails or
// comes back unverified; either way nothing is sent.
func (t *tick) observe(a adapter.Adapter) (decide.Observation, error) {
	limit := observeTimeout - t.observeSpent // less a lookup for an unsettled thread creation
	ctx, cancel := context.WithTimeout(t.ctx, limit)
	defer cancel()
	obs, err := a.Observe(ctx)
	if err == nil && ctx.Err() != nil {
		err = fmt.Errorf("observation exceeded %s", limit)
	}
	return obs, err
}

// essentialReports are the events a person or a dashboard acts on. The rest
// (a failed probe, a change in redundancy, an ordinary wake, nudge or
// re-prompt) describe routine work.
var essentialReports = map[string]bool{
	"await_user": true, "error": true, "stop": true, "handoff": true, "switch_back": true,
	"complete": true, limitWaitEvent: true, childErrorEvent: true,
}

// maxLateReports bounds how many reports a check may start after its budget
// is spent, each for at most minReportTime.
const maxLateReports = 3

// canReport says whether a report of this kind would be sent now, without
// using anything up.
func (t *tick) canReport(typ string) bool {
	return t.left() >= minReportTime || (essentialReports[typ] && t.lateReports < maxLateReports)
}

// reportContext gives a report its time. A report may use the time no
// mandatory step has reserved. When less than minReportTime of that is left,
// a routine report is not sent, and an essential one gets minReportTime from
// the time reserved for essential reports, for at most maxLateReports of
// them. A report that is not sent is noted in the delivery records.
func (t *tick) reportContext(typ, text string) (context.Context, context.CancelFunc, bool) {
	if left := t.left(); left >= minReportTime {
		ctx, cancel := context.WithTimeout(t.ctx, left)
		return ctx, cancel, true
	}
	if !essentialReports[typ] || t.lateReports >= maxLateReports {
		t.warn("report %s not sent: no time left in this check (loop.tick_timeout)", typ)
		if d, ok := t.opts.Reporter.(report.Dropper); ok {
			if err := d.Dropped(report.Event{Type: typ, Loop: t.loop.Name, Text: text, At: t.now}); err != nil {
				t.warn("report %s: %v", typ, err)
			}
		}
		return nil, nil, false
	}
	t.lateReports++
	ctx, cancel := context.WithTimeout(t.ctx, minReportTime)
	return ctx, cancel, true
}

// noteRedundancy evaluates fallback health, records it in the result and
// reports a change between meeting and not meeting the redundancy rule.
func (t *tick) noteRedundancy(target, group string) health.Summary {
	summary := health.Evaluate(t.fb, t.reg, t.now, target, group)
	t.res.Redundancy = &summary
	if degraded := !summary.Meets; degraded != t.st.Degraded && !t.opts.DryRun {
		t.st.Degraded = degraded
		if degraded {
			t.report("degraded", fmt.Sprintf("%s: only %d of the required %d distinct healthy fallbacks; handoffs are refused", t.loop.Name, summary.Distinct, summary.Min), map[string]any{"redundancy": summary})
		} else {
			t.report("recovered", fmt.Sprintf("%s: %d distinct healthy fallbacks again", t.loop.Name, summary.Distinct), map[string]any{"redundancy": summary})
		}
	}
	return summary
}

// lateScheduledProbes runs, once per check, the scheduled probes that were
// held back for a handoff, then brings the redundancy picture up to date. It
// reports whether any held-back probe ran or is still due.
func (t *tick) lateScheduledProbes(target, group string) bool {
	if t.lateProbed || t.reg == nil {
		return false
	}
	t.lateProbed = true
	before := len(t.res.Probes)
	if t.scheduledProbes(target, group) > 0 {
		t.noteRedundancy(target, group)
	}
	// True when a held-back probe ran, or is still due because it did not fit.
	return len(t.res.Probes) > before
}

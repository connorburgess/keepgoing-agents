// Package supervise runs one tick: observe, probe, decide, act, record.
package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
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
	"github.com/connorburgess/keepgoing-agents/internal/netretry"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

// ProbeFunc runs one probe.
type ProbeFunc func(ctx context.Context, c config.Candidate, tier probe.Tier, timeout time.Duration) probe.Result

// Options tune a tick. Zero values use the real implementations.
type Options struct {
	Version string // build label of the caller, for historical diagnostics
	// SessionDir is the private directory of session claims shared by every
	// loop this user runs; empty means session.Dir().
	SessionDir  string
	Now         time.Time
	DryRun      bool // decide and record; send nothing and run no probes
	Fingerprint func([]string) (string, error)
	NewAdapter  factory.Func
	Probe       ProbeFunc
	Reporter    report.Reporter
	Busy        func(ctx context.Context, y config.Yield) (bool, error)
	// Online reports connectivity once. Nil uses the loop's network settings;
	// a loop with the check off is never checked.
	Online func(ctx context.Context) bool
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
	// RetrySignal says why a provider retry notice was not honoured, so that
	// ordinary error or stall handling applied: "expired" when it had gone
	// stale, "declined" when the provider's own retry service had recorded
	// that it will not retry the failed turn. Empty otherwise.
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
	// Network is "down" when this check's connectivity check failed and work
	// that needs the network was not attempted, "down_untrusted" when it
	// failed but is not believed, and "restored" when it passed after an
	// outage, with NetworkDownFor saying how long the check had failed.
	Network        string `json:"network,omitempty"`
	NetworkDownFor string `json:"networkDownFor,omitempty"`
	// MissingWorktrees counts registered Git worktrees left out of the
	// progress fingerprint because their directories are gone.
	MissingWorktrees int `json:"missingWorktrees,omitempty"`
	// Recorded: state, the event line and incident history were all written.
	Recorded bool `json:"-"`
}

type tick struct {
	// claims are the session claims this check holds, by key. A check holds
	// its active session's claim from before it first observes it until it
	// ends, and a fallback's or the primary's before it probes or sends there.
	claims              map[string]*session.Claim
	sessionDir          string
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
	// turnRetryAlreadyQueued: this check asked for a turn retry and was told
	// one was already queued.
	turnRetryAlreadyQueued bool
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
	networkChecked      bool // connectivity was checked in this check
	reserveNetwork      time.Duration
	networkOffline      bool            // the check failed
	networkGated        bool            // and is still believed, so work that needs the network waits
	progressCounted     time.Duration   // time since the last check counted toward no_progress_timeout
	probeSucceeded      bool            // a fallback probe passed in this check
	sessionRecovered    bool            // the target was observed and is not in error
	savedSends          int64           // the last intended send state.json recorded when this check began
	unsettledBriefs     []control.Brief // briefs an earlier check recorded, outcome unknown
	briefHold           []control.Brief // briefs named by a hold already raised for them
	stopRequested       bool            // a stop is requested in control.json
	stopsMade           []string        // targets this check sent a stop to
	enabled             bool            // the loop is enabled in control.json
	stBeforeRecords     state.File      // state before the records were handled, to undo them
	quietFailure        bool            // a failure already reported by an earlier check
	failure             *failureReport  // wording for the one error report of a failed check
	observeSpent        time.Duration   // observation time used by a lookup for an unsettled thread creation
	lateReports         int             // reports started after the budget was spent
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
	_ = t.reportSent(typ, text, data)
}

// reportSent sends a report and says whether it was delivered.
func (t *tick) reportSent(typ, text string, data map[string]any) bool {
	if t.opts.DryRun {
		return false
	}
	ctx, cancel, ok := t.reportContext(typ, text)
	if !ok {
		return false
	}
	defer cancel()
	if err := t.opts.Reporter.Report(ctx, report.Event{Type: typ, Loop: t.loop.Name, Text: text, At: t.now, Data: data}); err != nil {
		t.warn("report %s: %v", typ, err)
		return false
	}
	return true
}

// reportOnce sends the report of an episode, named by key, until it has been
// delivered, at most state.MaxOnceAttempts times. A new key starts again.
func (t *tick) reportOnce(slot **state.OnceReport, key, typ, text string, data map[string]any) {
	if t.opts.DryRun {
		return
	}
	if *slot == nil || (*slot).Key != key {
		*slot = &state.OnceReport{Key: key}
	}
	r := *slot
	if r.Sent || r.Attempts >= state.MaxOnceAttempts {
		return
	}
	r.Attempts++
	r.Sent = t.reportSent(typ, text, data)
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
	if timeout := cfg.Loop.NetworkCheckTimeout(); opts.Online == nil && timeout > 0 {
		n := cfg.Loop.Network
		opts.Online = netretry.Probe{URL: n.URL, Command: n.Command, Timeout: timeout}.Online
	}
	if opts.Threads == nil {
		opts.Threads = bbThreads
	}
	now := opts.Now.UTC()
	res := Result{At: now, Loop: cfg.Loop.Name, Action: string(decide.None), DryRun: opts.DryRun}
	t := &tick{ctx: ctx, cfg: cfg, loop: cfg.Loop, fb: cfg.FallbackSet, opts: opts, now: now, res: &res}
	t.deadline = opts.Clock().Add(tickBudget(cfg.Loop) + checkTail)
	t.reserve = reserveFingerprint + reserveSend + reserveReports
	if opts.Online != nil {
		// The connectivity check is mandatory when it is needed, so its
		// timeout is reserved until it has run or can no longer be needed.
		t.reserveNetwork = cfg.Loop.NetworkCheckTimeout()
		if t.reserveNetwork <= 0 {
			t.reserveNetwork = config.DefaultNetworkTimeout // an injected check on a loop built in code
		}
		t.reserve += t.reserveNetwork
	}
	if opts.RoutineIgnored {
		t.warn("routine identity ignored: --routine takes source/routine or source/routine/invocation; this check ran without it")
	}
	t.graphSync()
	err := t.run()
	t.releaseClaims()
	t.graphClose()
	if journalErr := t.recordIncident(err != nil); journalErr != nil {
		t.warn("incident history write failed; diagnostic evidence is incomplete")
		err = errors.Join(err, journalErr)
		res.Recorded = false
	}
	return res, err
}

// sessionDirectory resolves the shared claim directory once per check. A
// directory of this user's that is not exactly private is made so, and the
// check says it once.
func (t *tick) sessionDirectory() (string, error) {
	if t.sessionDir != "" {
		return t.sessionDir, nil
	}
	dir := t.opts.SessionDir
	if dir == "" {
		d, tightened, err := session.Dir(true)
		if err != nil {
			return "", err
		}
		if tightened {
			t.warn("the session claim directory %s was not private; it is now 0700", d)
		}
		dir = d
	}
	t.sessionDir = dir
	return dir, nil
}

// claim takes a session's claim for the rest of this check. It returns the
// *session.ClaimedError of another loop's claim; an agent with no fixed
// session needs none.
func (t *tick) claim(a config.Agent) error {
	id, ok := session.Of(a)
	if !ok {
		return nil
	}
	if t.claims[id.Key()] != nil {
		return nil // this check already holds it
	}
	dir, err := t.sessionDirectory()
	if err != nil {
		return err
	}
	c, err := session.Take(dir, id, session.Holder{Loop: t.loop.Name, PID: os.Getpid(), Since: t.opts.Clock().UTC(), Until: t.deadline.UTC()})
	if err != nil {
		return err
	}
	if t.claims == nil {
		t.claims = map[string]*session.Claim{}
	}
	t.claims[id.Key()] = c
	return nil
}

// claimActive claims the session this check is about to observe.
//
//   - Another loop's live claim ends the check: nothing is observed, decided,
//     sent or recorded, and the caller skips it as a busy check. A holder past
//     its own time limit is stuck: that is recorded and reported once.
//   - No user state directory at all (no HOME and no XDG_STATE_HOME): the
//     check goes ahead unprotected and says so; the start and end of such a
//     stretch are reported once each. A check that failed on every run would
//     get an external scheduler to switch itself off.
//   - A claim directory or claim file that cannot be repaired safely (another
//     user's, not a directory, a link, something planted): the check acts on
//     nothing and fails, naming the path and the fix.
func (t *tick) claimActive(a config.Agent) error {
	id, ok := session.Of(a)
	if !ok {
		return nil // no fixed session, nothing to claim
	}
	err := t.claim(a)
	var claimed *session.ClaimedError
	var undetermined *session.UndeterminedError
	var unsafe *session.UnsafeError
	switch {
	case errors.As(err, &claimed):
		t.res.State, t.res.Window, t.res.Reason = "not observed", "not checked", "another loop is acting on this session"
		if claimed.Stuck() {
			t.reportStuckClaim(claimed)
			t.res.Reason = "another loop's stuck check holds this session"
			return t.record(err)
		}
		return err
	case errors.As(err, &unsafe):
		t.res.State, t.res.Window, t.res.Reason = "not observed", "not checked", "session claim unsafe; no action taken"
		return t.record(fmt.Errorf("session claim: %v; no action taken: %s", unsafe, unsafe.Fix()))
	case errors.As(err, &undetermined):
		t.warn("session claim unavailable (%v); this check is not protected against another loop acting on the same session at the same moment", err)
	case err != nil:
		t.res.State, t.res.Window, t.res.Reason = "not observed", "not checked", "session claim unsafe; no action taken"
		return t.record(fmt.Errorf("session claim: %v; no action taken", err))
	default:
		if !t.opts.DryRun && t.cfg.Path() != "" {
			// Best effort, and never more than a moment of the check's time:
			// lets enable and status name this loop to others.
			_ = session.Note(t.sessionDir, id, session.Supervisor{Loop: t.loop.Name, Config: t.cfg.Path(), StateDir: t.loop.StateDir, SeenAt: t.now}, 100*time.Millisecond)
		}
	}
	t.noteClaimEpisode(undetermined)
	return nil
}

// noteClaimEpisode keeps the record of a stretch without claims and sends
// its start and end once each, retrying a failed delivery.
func (t *tick) noteClaimEpisode(undetermined *session.UndeterminedError) {
	if t.opts.DryRun {
		return
	}
	unavailable := undetermined != nil
	if unavailable != t.st.ClaimUnavailable {
		t.st.ClaimUnavailable = unavailable
		kind := "recovered"
		if unavailable {
			kind = "degraded"
		}
		t.st.ClaimReport = &state.OnceReport{Key: fmt.Sprintf("%s:%s", kind, t.now.UTC().Format(time.RFC3339))}
	}
	if t.st.ClaimReport == nil {
		return
	}
	if strings.HasPrefix(t.st.ClaimReport.Key, "degraded:") {
		t.reportOnce(&t.st.ClaimReport, t.st.ClaimReport.Key, "degraded", fmt.Sprintf("%s: session claims are unavailable (%s); checks are not protected against another loop acting on the same session at the same moment", t.loop.Name, claimReason(undetermined)), map[string]any{"sessionClaim": "unavailable"})
	} else {
		t.reportOnce(&t.st.ClaimReport, t.st.ClaimReport.Key, "recovered", fmt.Sprintf("%s: session claims are available again", t.loop.Name), map[string]any{"sessionClaim": "available"})
	}
}

func claimReason(e *session.UndeterminedError) string {
	if e == nil {
		return "no user state directory"
	}
	return e.Error()
}

// reportStuckClaim reports, once per stuck holder, that another loop's check
// has held this session past its own time limit, and what to do.
func (t *tick) reportStuckClaim(c *session.ClaimedError) {
	t.warn("%v", c)
	key := fmt.Sprintf("%d:%s", c.Holder.PID, c.Holder.Since.UTC().Format(time.RFC3339Nano))
	t.reportOnce(&t.st.StuckClaimReport, key, "error", fmt.Sprintf("%s: %v", t.loop.Name, c), map[string]any{"sessionClaim": "stuck", "holderLoop": c.Holder.Loop, "holderPid": c.Holder.PID})
}

// claimOther claims a session this check may move to. It returns another
// loop's claim as the first value; the second is any other failure.
func (t *tick) claimOther(a config.Agent) (*session.ClaimedError, error) {
	err := t.claim(a)
	var claimed *session.ClaimedError
	if errors.As(err, &claimed) {
		return claimed, nil
	}
	return nil, err
}

func (t *tick) releaseClaims() {
	for _, c := range t.claims {
		c.Release()
	}
	t.claims = nil
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
	t.briefHold, t.stopRequested = ctl.BriefHold, ctl.Stop
	t.enabled = ctl.Enabled
	t.incidentControl = incident.Gate(ctl.Gate())
	if t.st, err = state.Load(loop.StateDir); err != nil {
		return err
	}
	t.incidentStateLoaded = true
	t.savedSends = t.st.SendsRecorded
	t.reportCheckGap()
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
	// Holds, a stop and the deadline come first: none of them sends. Before
	// anything that may, sends an earlier check made without recording them
	// are counted.
	if err := t.countUnrecordedSends(); err != nil {
		res.State, res.Window, res.Reason = "not observed", "not checked", "send record unreadable; no automatic provider work"
		return t.record(err)
	}
	if len(t.unsettledBriefs) > 0 {
		res.State, res.Window = "not observed", "not checked"
		return t.record(t.holdForBriefs())
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
		if p.Reached(t.now, timeout) && (t.networkDown() || t.outageJustEnded()) {
			// The limit is the owner's, but it measures time in which work was
			// possible. With the network down nothing was: the time since the
			// last observation is credited now, and the limit is looked at
			// again without it. The interval in which the network came back
			// is credited too: when in it is not known.
			if credited, err := state.CreditProgress(loop.StateDir, p, t.progressCounted, t.opts.DryRun); err == nil {
				p, t.progressCounted = credited, 0
				t.progress = &p
			}
		}
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
	// No two loops act on one session at the same moment: another loop's
	// check of this session ends this one before anything is observed or
	// sent.
	if err := t.claimActive(agent); err != nil {
		return err
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
		// One failure, one report: record sends it, with the target named
		// and the cause, in place of its general "tick failed" text.
		t.failure = &failureReport{text: fmt.Sprintf("%s: could not observe %s: %v", loop.Name, target, err), data: map[string]any{"target": target}}
		return t.record(err)
	}
	res.State = string(obs.State)
	t.sessionRecovered = obs.State == decide.Active || obs.State == decide.Idle
	if obs.State == decide.Error {
		res.ErrorClass = string(obs.ErrorClass)
	}
	if obs.RetryExpired {
		res.RetrySignal = "expired"
	}
	if obs.RetryDeclined {
		res.RetrySignal = "declined"
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
	if obs.AwaitingInput && !t.st.AwaitingReported && !t.opts.DryRun && t.reserveReport("awaiting_input") {
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
	t.reserve = reserveSend + reserveReports + t.reserveNetwork // the fingerprint has been taken
	unchangedFor = t.stallAfterRetry(obs, target, unchangedFor)
	if obs.State == decide.Active && t.outageKnown() {
		// The last check found the network down. An active session sends
		// nothing, so nothing else would look again, and the stall clock
		// would run through the outage.
		t.networkDown()
	}
	unchangedFor = t.stallSinceActive(obs, unchangedFor)
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
	//
	// Git progress seen on a check that finds the session in error does not
	// end it, although the fingerprint is taken there and no later check sees
	// that change. The fingerprint covers every worktree, so lanes, other
	// loops and people change it; counting it here would re-prompt a session
	// that never leaves error at every check for as long as anyone commits,
	// past the backoff, the hold and the brief caps. Only evidence about this
	// session's own turns could tell the two apart, and Git has none.
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
	if d.Action == decide.AwaitUser && obs.State == decide.Error && in.Reprompts >= in.MaxReprompts && t.st.UnconfirmedReprompts > 0 {
		// The cap was reached by attempts some of which were recorded and
		// then could not be confirmed as sent (the check that made them
		// could not save what happened). Say so, not that they all failed.
		d.Reason = fmt.Sprintf("error persisted after re-prompts; %d of them were recorded but could not be confirmed as sent", t.st.UnconfirmedReprompts)
	}
	// A re-prompt, a handoff or a nudge needs the network, and a hold raised
	// for an errored session (a spent re-prompt cap, an auth error) or for a
	// stalled one (the nudges are spent, a retry went silent) may only be the
	// outage showing: a turn whose host slept mid-request stays active and
	// never fails. Check connectivity now, when one of those is about to
	// happen, and let the engine decide what a dead network means: holds that
	// have nothing to do with the session still win there. An idle wake or a
	// switch-back does not trigger a check, but is not sent when this check
	// has already found the network down.
	recovering := obs.State == decide.Error || obs.State == decide.Active
	needsNetwork := d.Action == decide.Reprompt || d.Action == decide.Handoff || d.Action == decide.Nudge || (recovering && d.Action == decide.AwaitUser)
	sendsAnyway := d.Action == decide.Wake || d.Action == decide.SwitchBack || obs.State == decide.Active
	if (needsNetwork && t.networkDown()) || (sendsAnyway && t.networkGated) {
		in.NetworkDown = true
		t.decisionInput = in
		d = t.decide("network_down", in)
	}
	if t.st.SkipHandoffOnce && !t.opts.DryRun {
		t.st.SkipHandoffOnce = false
		if d.Action == decide.Handoff || d.Action == decide.SwitchBack {
			d = decide.Decision{Action: decide.None, Reason: "a person resumed after a brief that may have been delivered; no route is briefed on this check"}
		}
	}
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
			if t.networkOffline {
				why = " (network down, so the primary was not probed; staying on the fallback)"
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
	if errors.Is(actErr, state.ErrSendRecord) {
		res.Action, res.Reason = string(decide.None), state.ErrSendRecord.Error()
	}
	t.reserve = reserveReports + t.reserveNetwork // the action, if any, has been taken
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
	// Stop wins over every other rule. It waits only while another loop's
	// live check holds the session, which the claim proves; if the claim
	// cannot be taken for any other reason, the stop goes ahead without it.
	claimed, err := t.claimOther(agent)
	switch {
	case claimed != nil && claimed.Stuck():
		// A holder past its own hard limit is stuck; a second stop is
		// harmless, so the stop does not wait for it.
		t.reportStuckClaim(claimed)
		t.warn("stop proceeds past a stuck session claim")
	case claimed != nil:
		res.State, res.Reason = "not observed", "another loop is acting on this session"
		return claimed
	case err != nil:
		t.warn("stop proceeds without the session claim: %v", err)
	}
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
	if err := t.countUnrecordedSends(); err != nil {
		t.warn("%v", err)
	}
	// The briefs that may have put the task on another conversation: those
	// found unsettled now, and those named by a hold already raised for them.
	briefs := append(append([]control.Brief(nil), t.briefHold...), t.unsettledBriefs...)
	if len(briefs) > 0 && !t.st.StopRequestedSent {
		// A stop is asked for: stop every conversation that may have the
		// task, the current one and each one briefed (a second stop is
		// harmless). Each stop is recorded first, like any send.
		t.st.LastState = string(obs.State)
		res.Action, res.Reason = string(decide.Stop), "stop requested; stopping every conversation that may have the task"
		if t.opts.DryRun {
			return t.record(nil)
		}
		var errs []error
		var stopped []string
		waiting := false // a briefed session's stop waits for another loop's live check
		stopOne := func(id string, a adapter.Adapter) {
			if _, err := t.reserveSend("stop", id); err != nil {
				t.warn("the stop to %s could not be recorded before it was made: %v", id, err)
			}
			if err := a.Stop(t.ctx); err != nil && !errors.As(err, new(adapter.ErrUnsupported)) {
				errs = append(errs, fmt.Errorf("stop %s: %w", id, err))
				return
			}
			stopped = append(stopped, id)
		}
		stopOne(target, a)
		seen := map[string]bool{target: true}
		for _, b := range briefs {
			if seen[b.Target] {
				continue
			}
			seen[b.Target] = true
			if b.Target != state.Primary {
				if _, ok := t.fb.Find(b.Target); t.fb == nil || !ok {
					t.warn("a brief went to %s, which is no longer configured, so keepgoing cannot stop it: stop that conversation by hand", b.Target)
					continue
				}
			}
			id, agent := t.resolve(b.Target)
			// The same claim rule as the loop's own stop: wait only while
			// another loop's live check holds that session; go ahead past a
			// stuck holder, and without the claim if it cannot be taken.
			claimed, err := t.claimOther(agent)
			switch {
			case claimed != nil && claimed.Stuck():
				t.reportStuckClaim(claimed)
				t.warn("stop of %s proceeds past a stuck session claim", b.Target)
			case claimed != nil:
				t.warn("stop of %s waits: another loop's check is acting on that session; the next check stops it", b.Target)
				waiting = true
				continue
			case err != nil:
				t.warn("stop of %s proceeds without the session claim: %v", b.Target, err)
			}
			ba, err := t.newAdapter(agent, t.loop.StateDir, id)
			if err != nil {
				errs = append(errs, fmt.Errorf("stop %s: %w", b.Target, err))
				continue
			}
			stopOne(b.Target, ba)
		}
		t.stopsMade = stopped
		if len(errs) == 0 && !waiting {
			t.st.StopRequestedSent = true // otherwise the next check stops them all again
		}
		if len(t.unsettledBriefs) > 0 {
			if err := t.holdForBriefs(); err != nil {
				errs = append(errs, err)
			}
		}
		return t.record(errors.Join(errs...))
	}
	if len(t.unsettledBriefs) > 0 {
		// Already stopped for this request: hold for the new briefs.
		t.st.LastState = string(obs.State)
		return t.record(t.holdForBriefs())
	}
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
		// A probe into a dead network proves nothing about the candidate and
		// would mark every fallback unhealthy until its next scheduled probe.
		if t.networkDown() {
			t.skipProbe(c.ID, probe.Scheduled, "network_down")
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
		t.probeSucceeded = t.probeSucceeded || r.OK
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
		t.probeSucceeded = t.probeSucceeded || r.OK
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
			case errors.Is(err, state.ErrSendLimit) || errors.Is(err, state.ErrSendLedger) || errors.Is(err, state.ErrSendRecord):
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
		// The probe asked the account, not the session. Before anything is
		// sent to the session, it is claimed: a fallback whose session
		// another loop is acting on right now is not sent a brief. A claim
		// that cannot be taken at all does not stop the handoff, as it does
		// not stop the check.
		var undetermined *session.UndeterminedError
		if claimed, err := t.claimOther(c.Agent); claimed != nil {
			note.Skipped = append(note.Skipped, id+": another loop is acting on its session")
			continue
		} else if errors.As(err, &undetermined) {
			t.warn("handoff to %s is not protected by a session claim: %v", id, err)
		} else if err != nil {
			note.Skipped = append(note.Skipped, id+": its session claim is unsafe ("+err.Error()+")")
			continue
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
		if _, err := t.reserveSend("handoff", id); err != nil {
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
		t.reserve = reserveReports + t.reserveNetwork // the brief has been sent
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
	if t.st.SwitchBackRejected == t.loop.Errors.MaxReprompts && t.reserveReport("switch_back_stopped") {
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
	// Network down or out of time: leave the retry spacing alone so the next
	// check tries.
	if t.networkDown() {
		t.skipProbe(state.Primary, probe.Exact, "network_down")
		t.switchBackDeferred = true
		return false, nil
	}
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
	// The primary's session is not sent to while another loop is acting on
	// it; the switch-back waits for a later check.
	var undetermined *session.UndeterminedError
	if claimed, err := t.claimOther(t.loop.Agent); claimed != nil {
		t.warn("switch-back waits: %v", claimed)
		t.switchBackDeferred = true
		return false, nil
	} else if errors.As(err, &undetermined) {
		t.warn("switch-back is not protected by a session claim: %v", err)
	} else if err != nil {
		t.warn("switch-back waits: its session claim is unsafe (%v)", err)
		t.switchBackDeferred = true
		return false, nil
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
	if _, err := t.reserveSend("switch_back", state.Primary); err != nil {
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
	t.reserve = reserveReports + t.reserveNetwork // the brief has been sent
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
		childErrors := decide.ChildErrorNote(t.decisionInput, d)
		if d.ChildErrorWake {
			// Notice only: the parent is waiting on other children on purpose.
			text = prompt.ChildErrorsOnly(childErrors, t.decisionInput.Obs.ActiveChildren)
		} else {
			text = prompt.WithChildErrors(text, childErrors)
		}
		if !reserved {
			if _, err := t.reserveSend(string(d.Action), t.res.Target); err != nil {
				return err
			}
		}
		t.res.Recovery = ""
		if d.Action == decide.Reprompt {
			t.res.Recovery = string(decide.RecoverByPrompt)
		}
		err = t.send(a, text, mode, t.res.Target, string(d.Action))
		t.reserve = reserveReports + t.reserveNetwork // the send has been made
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
		if _, err := control.Update(loop.StateDir, func(f *control.File) {
			f.AwaitingUser, f.Note = true, d.Reason
			// Kept if already set: a later hold for another reason does not
			// make the spent allowance any less spent.
			f.EpisodeHold = f.EpisodeHold || d.EpisodeSpent
		}); err != nil {
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
		// Recorded first, like a send, so a supervisor stopped right after it
		// does not stop the turn again. If the record cannot be saved the stop
		// is still made: stopping again is harmless, and not stopping when
		// asked is the worse failure.
		if _, err := t.reserveSend("stop", t.res.Target); err != nil {
			t.warn("the stop could not be recorded before it was made, so a later check may make it again: %v", err)
		}
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

// failureReport is the error report a failed check sends, worded by the step
// that failed.
type failureReport struct {
	text string
	data map[string]any
}

func (t *tick) record(actErr error) error {
	t.noteCheckGap()
	t.st.CheckedAt = t.now
	// The count is of consecutive checks. Any check that did not end in that
	// answer breaks the run, including one that was held or sent nothing.
	if !t.turnRetryAlreadyQueued && !t.opts.DryRun {
		t.st.TurnRetryNoOps = 0
	}
	outcome := "succeeded"
	if t.opts.DryRun {
		outcome = "dry_run"
	}
	if actErr != nil {
		outcome = "failed"
	}
	t.noteNetwork()
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
	if r := t.opts.Routine; r.Source != "" {
		t.st.StartedBy = &state.Routine{Source: r.Source, Routine: r.Routine}
	} else {
		t.st.StartedBy = nil // started without a scheduler identity
	}
	if t.st.UnconfirmedReprompts > t.st.Reprompts {
		t.st.UnconfirmedReprompts = t.st.Reprompts // counted in an episode that has ended
	}
	if err := state.Save(t.loop.StateDir, t.st); err != nil {
		// Say what the check did as well as why it could not be saved: a
		// send it refused to make is the cause, and the save its symptom.
		return errors.Join(actErr, err)
	}
	// The outcomes are recorded: settle them beside the records too, so an
	// older binary that rewrites state.json cannot unsettle them. Only a
	// check that read the records, or made sends of its own, has moved
	// SendsRecorded past the point it started from.
	if !t.opts.DryRun && t.st.SendsRecorded > t.savedSends {
		if err := state.Settle(t.loop.StateDir, t.st.SendsRecorded); err != nil {
			t.warn("the send records were recorded in state.json but could not be marked settled in sends.json: %v", err)
		}
	}
	// After the state save, so graph work never delays it, and before the
	// event is built, so a graph warning reaches runs.jsonl.
	t.graphClose()
	ev := state.Event{At: t.now, Loop: t.loop.Name, Event: "tick", Action: t.res.Action, Reason: t.res.Reason, State: t.res.State, Target: t.res.Target, Outcome: outcome}
	extra := map[string]any{}
	if actErr != nil {
		ev.Error = actErr.Error()
		// A failed check sends one error report, and none when an earlier
		// check already reported the same failure. A step that knows more
		// than the error itself (which target could not be observed) words it.
		if f := t.failure; f != nil && !t.quietFailure {
			t.report("error", f.text, f.data)
		} else if !t.quietFailure {
			t.report("error", fmt.Sprintf("%s: tick failed: %v", t.loop.Name, actErr), nil)
		}
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

// reserveSend is the one way to send: every automatic send (a wake, nudge or
// re-prompt, a turn retry, a handoff or switch-back brief, a new thread's
// first prompt, a child-error notice) is preceded by it. It saves the record
// of the intended send in sends.json, limit or no limit, and with max_sends
// set it also counts the attempt; a stop and a report are recorded the same
// way but never counted against the allowance. If the record cannot be saved it returns
// an error and the caller sends nothing.
//
// The outcome is recorded by state.json, saved at the end of the check with
// SendsRecorded set to the newest intent. If that save fails, the next check
// finds the intent unrecorded and counts the send as made; see
// countUnrecordedSends.
func (t *tick) reserveSend(action, target string, key ...string) (int64, error) {
	max := t.loop.Limits.MaxSends
	if action == "stop" || strings.HasPrefix(action, "report:") {
		max = 0 // recorded, but not a send against the allowance
	}
	intent := state.SendIntent{At: t.now, Action: action, Target: target}
	if len(key) > 0 {
		intent.Key = key[0]
	}
	seq, err := state.Reserve(t.loop.StateDir, max, t.savedSends, intent)
	if err != nil {
		return 0, err
	}
	t.st.SendsRecorded = seq
	return seq, nil
}

// holdForBriefs raises the hold for briefs to other conversations whose
// outcome is unknown. They are settled only once the hold is saved: if it
// cannot be, the records this check handled are put back as they were, and
// the next check finds them again.
func (t *tick) holdForBriefs() error {
	reason := briefsReason(t.unsettledBriefs, t.st.Target(), t.stopRequested)
	if t.res.Action == string(decide.Stop) {
		t.res.Reason += "; then held: " + reason
	} else {
		t.res.Action, t.res.Reason = string(decide.AwaitUser), reason
	}
	if t.opts.DryRun {
		return nil
	}
	briefs := t.unsettledBriefs
	if _, err := control.Update(t.loop.StateDir, func(f *control.File) {
		f.AwaitingUser, f.Note = true, reason
		f.BriefHold = append(f.BriefHold, briefs...)
	}); err != nil {
		t.st = t.stBeforeRecords
		what := "nothing was sent"
		if len(t.stopsMade) > 0 {
			what = "stops were sent to " + strings.Join(t.stopsMade, " and ")
		}
		t.res.Reason = "a brief to another conversation may have been delivered, but the hold for it could not be saved; nothing was settled, and " + what
		if len(t.stopsMade) == 0 {
			t.res.Action = string(decide.None)
		}
		return err
	}
	t.report("await_user", fmt.Sprintf("%s: needs a person: %s", t.loop.Name, reason), map[string]any{"target": t.res.Target})
	return nil
}

// briefsReason says which briefs may have been delivered and what to do.
// With a stop requested it does not send the person to a plain resume,
// which would cancel the stop.
func briefsReason(briefs []control.Brief, current string, stopRequested bool) string {
	var names []string
	for _, b := range briefs {
		kind := "a handoff brief to " + b.Target
		if b.Action == "switch_back" {
			kind = "a switch-back brief to " + b.Target
		}
		names = append(names, kind)
	}
	resume := "`keepgoing resume`"
	if stopRequested {
		resume = "`keepgoing resume --keep-stop` (a stop is requested, and a plain `keepgoing resume` would cancel it)"
	}
	return strings.Join(names, " and ") + " may have been delivered. Check that conversation: if it took the task, run `keepgoing retarget <id>` and then " + resume + ", so the loop follows it; if not, run " + resume + ", and the loop stays on " + current + " and briefs no route on the next check"
}

// copyState copies a state record deeply enough to restore it.
func copyState(f state.File) state.File {
	b, _ := json.Marshal(f)
	var c state.File
	if json.Unmarshal(b, &c) != nil {
		return f
	}
	return c
}

// reportCheckGap sends one check_gap report when this check is the first
// after the loop went overdue: no check had run for longer than the overdue
// rule allows (state.ScheduleOf), so its scheduler had probably stopped. It is
// sent once per gap, keyed on the time of the last check before it, and
// recorded before it is sent. status, doctor and the panel say the same while
// the gap lasts; this tells the owner afterwards if nobody was looking.
func (t *tick) reportCheckGap() {
	sch := state.ScheduleOf(t.st, t.loop.Daemon.Interval.Duration, t.enabled, t.now)
	if !sch.Overdue || t.opts.DryRun || t.st.CheckGapReported.Equal(sch.LastCheckAt) || !t.loop.Report.ReportsEvent(checkGapEvent) {
		return
	}
	// The gap is reported by the first check after it, or not at all: the
	// next check finds a recent check before it.
	if !t.canReport(checkGapEvent) {
		t.warn("report %s not sent: no time is left in this check", checkGapEvent)
		return
	}
	key := sch.LastCheckAt.UTC().Format(time.RFC3339Nano)
	if _, err := t.reserveSend("report:check_gap", t.st.Target(), key); err != nil {
		t.warn("report check_gap not sent, because the record of sending it could not be saved: %v", err)
		return
	}
	t.st.CheckGapReported = sch.LastCheckAt
	text := fmt.Sprintf("%s: no check ran from %s to %s (%s, where checks normally run every %s), so the loop's scheduler had probably stopped. Checks are running again.",
		t.loop.Name, sch.LastCheckAt.UTC().Format(time.RFC3339), t.now.UTC().Format(time.RFC3339), sch.For.Truncate(time.Minute), sch.Usual)
	if r := sch.StartedBy; r != nil && r.Source == state.RoutineBBAutomation {
		text += " The BB automation that runs it (" + r.Routine + ") may have disabled itself and been turned back on."
	}
	t.report(checkGapEvent, text, map[string]any{"lastCheckAt": sch.LastCheckAt, "gap": sch.For.String(), "usual": sch.Usual.String()})
}

// checkGapEvent is the report event for a gap in which no check ran.
const checkGapEvent = "check_gap"

// reserveReport records a report that is sent once, whose record of having
// been sent is otherwise only saved at the end of the check, before it is
// delivered: a supervisor stopped while it is delivered does not send it
// again. If the record cannot be saved the report is not sent this time.
func (t *tick) reserveReport(key string) bool {
	if _, err := t.reserveSend("report:"+key, t.res.Target); err != nil {
		t.warn("report not sent, because the record of sending it could not be saved: %v", err)
		return false
	}
	return true
}

// countUnrecordedSends finds sends that an earlier check made, or may have
// made, without saving what happened: it saved the intent and then could not
// save state.json. Each is counted as made, exactly as an attempt whose
// outcome is unknown is counted: it spends the per-episode cap and starts the
// spacing, so it is never repeated blindly.
func (t *tick) countUnrecordedSends() error {
	l, err := state.LoadLedger(t.loop.StateDir)
	if err != nil {
		return t.sendRecordUnreadable(err)
	}
	if !t.opts.DryRun {
		t.st.SendRecordUnreadable = false
	}
	settled := max(t.st.SendsRecorded, l.Settled)
	t.savedSends = settled
	t.stBeforeRecords = copyState(t.st)
	if !t.opts.DryRun {
		defer func() { t.st.ResumedBriefs = nil }()
	}
	for _, p := range l.Unrecorded(settled) {
		if p.Action == "handoff" || p.Action == "switch_back" {
			b := control.Brief{Action: p.Action, Target: p.Target}
			// A person has already looked at this brief and resumed: its
			// hold was saved, but the check that raised it could not save
			// that it had settled the record. It is settled now.
			if !slices.Contains(t.st.ResumedBriefs, state.Brief(b)) {
				t.unsettledBriefs = append(t.unsettledBriefs, b)
			}
		}
		if t.opts.DryRun {
			continue // a dry run previews the hold and settles nothing
		}
		t.st.SendsRecorded = max(t.st.SendsRecorded, p.Seq)
		agentSend := true
		switch decide.Action(p.Action) {
		case decide.Wake, decide.Nudge, decide.Reprompt:
			c := decide.Attempted(decide.Action(p.Action), decide.Counters{
				Nudges: t.st.Nudges, Reprompts: t.st.Reprompts, LastSendAt: t.st.LastWakeAt, LastRepromptAt: t.st.LastRepromptAt,
			}, p.At)
			t.st.Nudges, t.st.Reprompts, t.st.LastWakeAt, t.st.LastRepromptAt = c.Nudges, c.Reprompts, c.LastSendAt, c.LastRepromptAt
			if p.Action == string(decide.Reprompt) {
				t.st.UnconfirmedReprompts++
			}
		case "handoff", "switch_back":
			// Settled by the hold this check raises: a person looks first.
		case "new_thread":
			// Settled by its own record in state.json, saved before the
			// conversation was created (see createThread).
			agentSend = false
		case "child_notice":
			if t.st.ChildErrorNoticeFor != p.Key {
				t.st.ChildErrorNoticeFor, t.st.ChildErrorNoticeAttempts = p.Key, 0
			}
			t.st.ChildErrorNoticeAttempts++
		case "stop":
			// Never taken as made: a stop recorded and then not sent must be
			// sent, and a second stop is harmless. Only a confirmed stop sets
			// StopRequestedSent.
			agentSend = false
			t.warn("a stop to %s was recorded at %s but that check could not save what happened, so it may not have been made; it is made again if the stop is still requested", p.Target, p.At.Format(time.RFC3339))
		case "report:awaiting_input":
			t.st.AwaitingReported, agentSend = true, false
		case "report:check_gap":
			if at, err := time.Parse(time.RFC3339Nano, p.Key); err == nil {
				t.st.CheckGapReported = at
			}
			agentSend = false
		case "report:degraded", "report:recovered":
			t.st.Degraded, agentSend = p.Action == "report:degraded", false
		case "report:switch_back_stopped":
			t.st.SwitchBackRejected, agentSend = max(t.st.SwitchBackRejected, t.loop.Errors.MaxReprompts), false
		}
		if agentSend && p.At.After(t.st.LastWakeAt) {
			t.st.LastWakeAt = p.At
		}
		if agentSend {
			t.warn("a %s to %s was recorded at %s but that check could not save what happened, so it may have been sent; it is counted as sent", p.Action, p.Target, p.At.Format(time.RFC3339))
		}
	}
	return nil
}

// sendRecordUnreadable is a check that cannot read sends.json. Nothing is
// sent. The error report goes once, when this begins, not at every check.
func (t *tick) sendRecordUnreadable(err error) error {
	if t.st.SendRecordUnreadable {
		t.quietFailure = true
	}
	if !t.opts.DryRun {
		t.st.SendRecordUnreadable = true
	}
	if t.loop.Limits.MaxSends == 0 {
		return fmt.Errorf("%w: restore it from a copy; deleting it loses the record of sends not yet counted", errSendRecordUnreadable)
	}
	return err
}

var errSendRecordUnreadable = errors.New("the send record sends.json cannot be read, so nothing is sent")

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
	if previous != nil {
		// Time in which no check of any kind ran cannot have been used to make
		// progress under supervision: the host slept or the scheduler was
		// stopped. Of such a gap only an ordinary interval counts toward the
		// timeout. Checks that ran while the loop was paused or held are
		// checks, so that time counts as before.
		t.progressCounted = t.now.Sub(previous.ObservedAt)
		if missed := t.missedTime(); missed > 0 {
			credited := *previous
			credited.ChangedAt = credited.ChangedAt.Add(min(missed, t.progressCounted))
			if credited.ChangedAt.After(t.now) {
				credited.ChangedAt = t.now
			}
			previous = &credited
			t.progressCounted -= min(missed, t.progressCounted)
		}
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

// networkDown checks connectivity at most once per check, and only when
// something that needs the network is about to be done. It reports whether
// that work should wait. Without a configured check, in a dry run, or once a
// failing check has been shown wrong, it reports false and nothing is gated.
func (t *tick) networkDown() bool {
	if t.opts.Online == nil || t.opts.DryRun {
		return false // a dry run makes no request
	}
	if !t.networkChecked {
		t.networkChecked = true
		ctx, cancel := context.WithTimeout(t.ctx, t.reserveNetwork)
		t.networkOffline = !t.opts.Online(ctx)
		cancel()
		t.releaseNetworkReserve()
		if t.networkOffline {
			t.networkGated = !t.st.NetworkDistrusted
			if t.networkGated && t.verifyDue() {
				t.verifyOutage()
			}
			if t.networkGated {
				t.res.Network = "down"
				t.warn("network down: the connectivity check failed, so nothing that needs the network was attempted (loop.network)")
			} else {
				t.res.Network = "down_untrusted"
				t.warn("the connectivity check is failing but the provider answered a probe, so the check is not believed for this outage and the ordinary rules apply. Run `keepgoing doctor` to test loop.network")
			}
		}
	}
	return t.networkGated
}

// networkDownFor is how long the connectivity check has been failing.
func (t *tick) networkDownFor() time.Duration {
	if t.st.NetworkDownSince.IsZero() {
		return 0
	}
	return max(time.Duration(0), t.now.Sub(t.st.NetworkDownSince))
}

func (t *tick) verifyAfter() time.Duration {
	if d := t.loop.Network.VerifyAfter.Duration; d > 0 {
		return d
	}
	return config.DefaultNetworkVerifyAfter
}

// verifyMaxInterval is the longest wait between two looks at one outage.
const verifyMaxInterval = 7 * 24 * time.Hour

// verifyInterval is the wait before the next closer look at the outage:
// verify_after before the first, then twice as long each time up to a week,
// so three weeks offline cost nine probes and not five hundred.
func (t *tick) verifyInterval() time.Duration {
	d := t.verifyAfter()
	for i := 0; i < t.st.NetworkVerifications && d < verifyMaxInterval; i++ {
		d *= 2
	}
	return min(d, verifyMaxInterval)
}

// verifyDue says the outage is old enough to be looked at more closely, and
// was not looked at within the current interval.
func (t *tick) verifyDue() bool {
	if t.st.NetworkVerifiedAt.IsZero() {
		return t.networkDownFor() >= t.verifyAfter()
	}
	return t.now.Sub(t.st.NetworkVerifiedAt) >= t.verifyInterval()
}

// outageKnown says the previous check found the network down and believed it.
func (t *tick) outageKnown() bool {
	return !t.st.NetworkDownSince.IsZero() && !t.st.NetworkDistrusted
}

// outageJustEnded says this check found the network working after a check
// that found it down.
func (t *tick) outageJustEnded() bool {
	return t.networkChecked && !t.networkOffline && t.outageKnown()
}

// verifyOutage keeps a failing check from holding a loop in silence, without
// turning a real outage into re-prompts into a dead network.
//
// The check's target can be unreachable while the provider works (a DNS
// filter, a proxy, an untrusted intercepting gateway). So the provider is
// asked directly, with the one exact probe a switch-back would use. Only
// positive evidence that the provider was reached sets the check aside for
// this outage:
//
//   - the probe passes;
//   - rate_limit or overloaded: the provider's own answer to a request that
//     reached it. No CLI produces either without a response.
//
// Everything else leaves the loop waiting, however long the outage lasts:
// network and unknown say nothing was reached, and auth is not evidence
// either way, because a CLI can find its stored login expired without making
// a request. An expired login with a working network is not held up by this:
// the connectivity check passes then, and the auth hold is reached as usual.
//
// The wait between looks doubles, up to a week. A report is attempted at
// each look until one is accepted; with no probe configured, where keepgoing
// cannot tell a real outage from a blocked check target, it is repeated at
// every look so the loop never goes silent.
func (t *tick) verifyOutage() {
	_, agent := t.resolve(t.st.Target())
	c := t.loop.PrimaryCandidate()
	if t.st.Target() != state.Primary && t.fb != nil {
		if found, ok := t.fb.Find(t.st.Target()); ok {
			c = found
		}
	}
	_ = agent
	configured := config.DefaultProbeTimeout
	if t.fb != nil {
		configured = t.fb.Probe.Timeout.Duration
	}
	tested := false
	if c.Probe.Kind != "" {
		timeout, ok := t.probeBudget(configured)
		if !ok {
			return // no time in this check; the next one tries
		}
		r := t.opts.Probe(t.ctx, c, probe.Exact, timeout)
		if r.TimedOut && timeout < configured {
			return // stopped early by the time accounting: no evidence
		}
		tested = true
		t.res.Probes = append(t.res.Probes, ProbeNote{ID: c.ID, Tier: probe.Exact, OK: r.OK, ErrorClass: r.ErrorClass, Detail: "connectivity verification"})
		switch decide.ErrorClass(r.ErrorClass) {
		case decide.ErrRateLimit, decide.ErrOverloaded:
			r.OK = true // the provider answered: the network works
		}
		if r.OK {
			t.st.NetworkDistrusted, t.networkGated = true, false
			t.st.NetworkVerifiedAt = t.now
			t.st.NetworkVerifications++
			return
		}
	}
	t.st.NetworkVerifiedAt = t.now
	t.st.NetworkVerifications++
	if tested && !t.st.NetworkReportedAt.IsZero() {
		return
	}
	how := "A direct probe of the provider did not get through either, so this looks like a real outage."
	if !tested {
		how = "No probe is configured for this target, so keepgoing cannot tell a real outage from a check target this machine cannot reach."
	}
	text := fmt.Sprintf("%s: keepgoing's connectivity check has failed for %s, so re-prompts, handoffs and fallback probes are waiting. %s If the network is in fact working, run `keepgoing doctor` and review loop.network. keepgoing keeps waiting and looks again in %s.",
		t.loop.Name, t.networkDownFor().Truncate(time.Minute), how, t.verifyInterval())
	ctx, cancel, ok := t.reportContext("network_wait", text)
	if !ok {
		return
	}
	defer cancel()
	if err := t.opts.Reporter.Report(ctx, report.Event{Type: "network_wait", Loop: t.loop.Name, Text: text, At: t.now,
		Data: map[string]any{"downSince": t.st.NetworkDownSince, "providerTested": tested}}); err != nil {
		t.warn("report network_wait: %v; it is attempted again at the next look", err)
		return
	}
	t.st.NetworkReportedAt = t.now
}

// Cadence. A gap between two checks is ordinary when it is no more than
// missedGapFactor times the loop's usual interval, and the usual interval is
// what the loop itself has shown: the longest of its last cadenceWindow gaps,
// or daemon.interval if that is longer. Before the loop has shown
// cadenceMinGaps gaps there is too little to judge by, and no gap is judged. So a scheduler that
// runs every hour, or at uneven times, is not mistaken for a host that
// sleeps, whatever daemon.interval says. The longest gap and not a typical
// one, because a check run by hand between two scheduled ones must not make
// the scheduled interval look like an outage.
const (
	cadenceWindow   = 5
	cadenceMinGaps  = state.CadenceMinGaps
	missedGapFactor = 3
)

// usualInterval is the loop's usual time between checks. Zero means there is
// nothing to judge a gap by.
func (t *tick) usualInterval() time.Duration {
	if len(t.st.RecentCheckGaps) < cadenceMinGaps {
		return 0
	}
	usual := t.loop.Daemon.Interval.Duration
	for _, g := range t.st.RecentCheckGaps {
		usual = max(usual, g)
	}
	return usual
}

// missedTime is the part of the time since the previous check, of any kind,
// in which no check ran although one was expected: the host slept or the
// scheduler was stopped. It is the excess over the usual interval, for a gap
// longer than missedGapFactor usual intervals, and zero otherwise.
func (t *tick) missedTime() time.Duration {
	usual := t.usualInterval()
	if usual <= 0 || t.st.CheckedAt.IsZero() {
		return 0
	}
	gap := t.now.Sub(t.st.CheckedAt)
	if gap <= missedGapFactor*usual {
		return 0
	}
	return gap - usual
}

// noteCheckGap records the time since the previous check for usualInterval.
// A gap judged to hold missed time is recorded as the longest ordinary one,
// so an outage does not pass for the loop's cadence, while a scheduler that
// has really slowed down is learned within a few checks.
func (t *tick) noteCheckGap() {
	if t.st.CheckedAt.IsZero() || !t.now.After(t.st.CheckedAt) {
		return
	}
	gap := t.now.Sub(t.st.CheckedAt)
	if usual := t.usualInterval(); usual > 0 {
		gap = min(gap, missedGapFactor*usual)
	}
	gaps := append(t.st.RecentCheckGaps, gap)
	t.st.RecentCheckGaps = gaps[max(0, len(gaps)-cadenceWindow):]
}

// noteNetwork records the state of the outage after a check.
func (t *tick) noteNetwork() {
	if t.opts.DryRun {
		return
	}
	switch {
	case t.networkChecked && !t.networkOffline:
		// The check passed: the outage, its report and any distrust are over.
		// When in the last interval the network came back is not known, so
		// none of it counts toward no_progress_timeout.
		if t.outageKnown() && t.progress != nil {
			if p, err := state.CreditProgress(t.loop.StateDir, *t.progress, t.progressCounted, false); err == nil {
				t.progress, t.progressCounted = &p, 0
			}
		}
		if !t.st.NetworkDownSince.IsZero() {
			t.res.Network, t.res.NetworkDownFor = "restored", t.networkDownFor().Truncate(time.Second).String()
			t.warn("network back: the connectivity check passed after failing for %s", t.networkDownFor().Truncate(time.Second))
		}
		t.st.NetworkDownSince, t.st.NetworkVerifiedAt, t.st.NetworkReportedAt, t.st.NetworkDistrusted, t.st.NetworkVerifications = time.Time{}, time.Time{}, time.Time{}, false, 0
	case t.networkOffline:
		if t.st.NetworkDownSince.IsZero() {
			t.st.NetworkDownSince = t.now
		}
		// A fallback answered a probe while the check was failing: the check
		// is wrong about this outage.
		if t.probeSucceeded {
			t.st.NetworkDistrusted = true
		}
		// While the network is believed down nothing can make progress, so
		// the time since the last check does not count toward
		// no_progress_timeout.
		if t.networkGated && t.progress != nil {
			if p, err := state.CreditProgress(t.loop.StateDir, *t.progress, t.progressCounted, false); err == nil {
				t.progress = &p
			} else {
				t.warn("could not credit the outage to the no-progress allowance")
			}
		}
	case t.sessionRecovered:
		// No check was needed and the session is not in error: whatever the
		// last check said, nothing is waiting for the network now. The
		// episode in which the check was not believed is over too: without
		// this a loop with no fallback probes, which makes no connectivity
		// check while its session is healthy, would keep disbelieving the
		// check, and a real outage days later would be re-prompted into.
		t.st.NetworkDownSince, t.st.NetworkVerifiedAt, t.st.NetworkReportedAt, t.st.NetworkDistrusted, t.st.NetworkVerifications = time.Time{}, time.Time{}, time.Time{}, false, 0
	}
}

// essentialReports are the events a person or a dashboard acts on. The rest
// (a failed probe, a change in redundancy, an ordinary wake, nudge or
// re-prompt) describe routine work.
var essentialReports = map[string]bool{
	"await_user": true, "error": true, "stop": true, "handoff": true, "switch_back": true,
	"complete": true, limitWaitEvent: true, childErrorEvent: true, "network_wait": true, checkGapEvent: true,
}

// maxLateReports bounds how many reports a check may start after its budget
// is spent, each for at most minReportTime.
const maxLateReports = 3

// canReport says whether a report of this kind would be sent now, without
// using anything up.
func (t *tick) canReport(typ string) bool {
	// A report that makes a single attempt would be lost into a dead network.
	if (typ == limitWaitEvent || typ == childErrorEvent) && t.networkDown() {
		return false
	}
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
	key := "recovered"
	if !summary.Meets {
		key = "degraded"
	}
	if degraded := !summary.Meets; degraded != t.st.Degraded && !t.opts.DryRun && t.reserveReport(key) {
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

// releaseNetworkReserve gives back the time kept for the connectivity check
// once it has run.
func (t *tick) releaseNetworkReserve() {
	t.reserve -= t.reserveNetwork
	t.reserveNetwork = 0
}

// stallSinceActive keeps the stall clock from counting time in which the
// session could not have been working. On positive evidence of that, the
// clock restarts when the session is seen active again: it was in error at
// the last check, or a connectivity check found the network down at the last
// check or at this one. Where only a gap with no check is known, the clock is
// paused for the missed time and not restarted. A session woken from idle is
// timed as before, from its last Git change, with min_wake_interval spacing
// the nudge from the wake.
func (t *tick) stallSinceActive(obs decide.Observation, unchanged time.Duration) time.Duration {
	since := t.st.ActiveSince
	switch {
	case obs.State != decide.Active:
		since = time.Time{}
	case t.st.LastState == string(decide.Error) || t.networkGated || t.outageKnown():
		since = t.now
	default:
		if missed := t.missedTime(); missed > 0 {
			if changed := t.now.Add(-unchanged); since.Before(changed) {
				since = changed
			}
			if since = since.Add(missed); since.After(t.now) {
				since = t.now
			}
		}
	}
	if !t.opts.DryRun {
		t.st.ActiveSince = since
	}
	if obs.State == decide.Active && !since.IsZero() {
		unchanged = min(unchanged, max(time.Duration(0), t.now.Sub(since)))
	}
	return unchanged
}

// Package decide is the pure decision engine. It takes what the adapter
// observed plus the control and progress state and returns one action.
// It performs no I/O so every rule can be table-tested.
package decide

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// State is the supervised session's coarse status.
type State string

// Session states an adapter may report.
const (
	Active   State = "active"
	Idle     State = "idle"
	Error    State = "error"
	Stopping State = "stopping"
	Pending  State = "pending"
	Unknown  State = "unknown"
)

// ErrorClass is a structured classification of a session error. It must come
// from exit codes, JSON fields, or HTTP status, never from prose.
type ErrorClass string

// Error classes.
const (
	ErrNone       ErrorClass = "none"
	ErrRateLimit  ErrorClass = "rate_limit"
	ErrAuth       ErrorClass = "auth"
	ErrOverloaded ErrorClass = "overloaded"
	ErrNetwork    ErrorClass = "network"
	ErrUnknown    ErrorClass = "unknown"
)

// Observation is what an adapter saw.
type Observation struct {
	// Evidence is diagnostic metadata and never changes Decide policy.
	Evidence   evidence.Detail
	State      State
	ErrorClass ErrorClass
	Queued     int  // messages already waiting for the session
	Background int  // background workers still running
	Gone       bool // target deleted or archived
	// Retrying means the provider itself has scheduled a retry of the
	// failed turn, so the supervisor should leave it alone.
	Retrying bool
	// RetryExpired means the adapter saw a retry notice with no fresh
	// evidence behind it and stopped honoring it, so Retrying is false and the
	// ordinary error or stall handling applies. Its only policy effect is the
	// bound on a silent active session that has no Git progress tracking.
	RetryExpired bool
	// RetryKnown is diagnostic evidence availability; false does not assert that
	// retries are absent. It does not change decision policy.
	RetryKnown bool
	// RetryUnverified means the adapter could not refresh retry/recovery evidence.
	// It suppresses automatic prompts and moves without hiding an active stop target.
	RetryUnverified bool
	// ResetsAt, when known, is when the current usage limit lifts.
	ResetsAt time.Time
	// AwaitingInput means the session is blocked on a person: a pending
	// permission prompt, question, or form, reported by the provider.
	AwaitingInput bool
	// DeferredInteraction means saved structured evidence that a provider
	// interaction was cancelled and handed to a person, and that the supervisor
	// has not yet recorded an awaiting-user hold for it. Unlike AwaitingInput it
	// is not a still-pending provider state; it never implies approval.
	DeferredInteraction bool
	// ActiveChildren is verified direct child work. ChildrenUnverified means
	// discovery could not establish the complete picture. Neither permits a
	// parent prompt or target move; control holds and stop retain precedence.
	ActiveChildren     int
	ChildrenUnverified bool
	// AwaitingChildren contains verified child IDs with pending human input.
	AwaitingChildren []string
	// ErroredChildren contains verified direct child IDs whose status is
	// error. They are not running work. They change the action Decide picks
	// in one case only, childErrorWake; ChildErrorNote says when a wake should
	// mention them.
	ErroredChildren []string
	// ActiveChildIDs names the children counted in ActiveChildren, for the
	// run graph only. It is diagnostic and never changes Decide policy.
	ActiveChildIDs []string
	// HealthyChildren contains verified direct child IDs with a known status
	// other than error. Decide never reads it; the owner report uses it to
	// tell a child that recovered from one an incomplete listing did not show.
	HealthyChildren []string
}

// Control is the operator's gate.
type Control struct {
	Enabled      bool
	Paused       bool
	Stop         bool
	AwaitingUser bool
	Completed    bool
}

// Input is everything Decide needs.
type Input struct {
	Obs               Observation
	Control           Control
	InWindow          bool
	NoProgressReached bool // verified Git metadata unchanged for the configured duration
	SendLimitReached  bool // total reserved send attempts reached the configured cap
	DeadlineReached   bool // evaluated at the beginning of this check

	UnchangedFor time.Duration // since the progress fingerprint last changed
	// ProgressTracked: Git repositories are configured, so UnchangedFor is
	// evidence. RetrySilentFor: how long this active session has been observed
	// with an expired retry notice; zero when that does not apply.
	ProgressTracked bool
	RetrySilentFor  time.Duration
	SinceWake       time.Duration // since we last sent anything
	StallAfter      time.Duration
	MinWakeInterval time.Duration

	Failover Failover

	// SwitchBackReady: the loop is on a fallback, switch-back is enabled, and
	// the primary's limit has had time to reset. The tick still confirms the
	// primary with an exact probe before moving.
	SwitchBackReady bool

	// UnannouncedErroredChildren counts errored children the session has not
	// been told about. Children it was told about that are still in error do
	// not count, so a shrinking set announces nothing.
	UnannouncedErroredChildren int
	// ChildErrorNoticeAttempts counts sends of the current announcement that
	// the adapter did not accept. At MaxChildErrorNotices it is given up.
	ChildErrorNoticeAttempts int
	MaxChildErrorNotices     int

	Nudges    int // nudges since progress or the most recent turn end
	MaxNudges int

	Reprompts         int // re-prompts sent since the session last ran cleanly
	MaxReprompts      int
	SinceLastReprompt time.Duration
	Backoff           time.Duration

	// NetworkDown: a structured connectivity check just failed and is still
	// believed. Nothing that needs the network is sent: no re-prompt, handoff,
	// wake, nudge or switch-back, and an errored session is not held for a
	// person because of it. The session is looked at again on the next check.
	NetworkDown bool

	// HandoffDeferred: a handoff is due and a fallback is listed healthy, but
	// this check ran out of time to confirm one. Nothing is spent on that.
	HandoffDeferred bool
}

// Failover is the supervisor's view of whether a handoff is possible.
type Failover struct {
	// Eligible: the error class is one the loop fails over on.
	Eligible bool
	// Available: a confirmed, healthy fallback exists and the redundancy
	// rule is met.
	Available bool
	// ResetIn is how long until the limit lifts; zero when unknown.
	ResetIn time.Duration
	// WaitIfResetWithin: wait rather than hand off when the limit lifts
	// this soon.
	WaitIfResetWithin time.Duration
}

// Action is what the command layer should do.
type Action string

// Actions.
const (
	None       Action = "none"        // do nothing this tick
	Wake       Action = "wake"        // start a turn on an idle session
	Nudge      Action = "nudge"       // queue a progress check behind a live turn
	Reprompt   Action = "reprompt"    // start a turn after an error, counted and bounded
	AwaitUser  Action = "await_user"  // give up until the operator intervenes
	Stop       Action = "stop"        // stop the running session now
	Handoff    Action = "handoff"     // move the loop to a confirmed fallback
	SwitchBack Action = "switch_back" // return the loop to its primary
)

// LimitWait names the rule under which a known provider limit reset is waited
// out. It is diagnostic metadata: nothing reads it to choose an action.
type LimitWait string

// Limit-wait rules.
const (
	WaitResetSoon        LimitWait = "limit_wait"              // the reset is within wait_if_reset_within
	WaitNoFallback       LimitWait = "limit_no_fallback"       // failover is eligible but no fallback is confirmed
	WaitFailoverDisabled LimitWait = "limit_failover_disabled" // the error class is not configured to fail over
)

// Decision is an action and the rule that produced it. LimitWait is set only
// when the action is None because a provider limit has a known reset.
type Decision struct {
	Action    Action
	Reason    string
	LimitWait LimitWait
	// ChildErrorWake is set only when errored children caused this wake, so a
	// caller never matches on Reason text. The parent is idle on purpose while
	// other children run: it gets the notice alone, not the task prompt.
	ChildErrorWake bool
	// DeferredInteraction is set only when the deferred-interaction rule itself
	// produced an AwaitUser, so a caller never matches on Reason text.
	DeferredInteraction bool
}

// DeferredInteractionReason is fixed text; it carries no provider content.
const DeferredInteractionReason = "agent interaction was cancelled and needs a person; nothing was approved"

func limitWait(kind LimitWait, reason string) Decision {
	return Decision{Action: None, Reason: reason, LimitWait: kind}
}

func none(reason string) Decision { return Decision{Action: None, Reason: reason} }

// NetworkDownReason is recorded when recovery waits for connectivity.
const NetworkDownReason = "network down; waiting for connectivity"

// HandoffDeferredReason is recorded when a due handoff waits for the next check.
const HandoffDeferredReason = "handoff deferred: no fallback could be confirmed in this check; the next check retries"

// Decide applies the rules in priority order. Stop and pause always win.
func Decide(in Input) Decision {
	c := in.Control
	o := in.Obs
	if c.Stop {
		if o.State == Active || o.State == Pending {
			return Decision{Action: Stop, Reason: "stop requested"}
		}
		return none("stop requested; session not running")
	}
	if !c.Enabled {
		return none("disabled")
	}
	if c.Paused {
		return none("paused")
	}
	if c.AwaitingUser {
		return none("awaiting user")
	}
	if c.Completed {
		return none("completed")
	}
	if in.DeadlineReached {
		return Decision{Action: AwaitUser, Reason: "loop deadline reached; revise loop.limits.deadline before resuming"}
	}
	if in.SendLimitReached {
		return Decision{Action: AwaitUser, Reason: "loop send limit reached; increase loop.limits.max_sends before resuming"}
	}
	if in.NoProgressReached {
		return Decision{Action: AwaitUser, Reason: "no Git progress within loop.limits.no_progress_timeout; inspect work and progress tracking before resuming"}
	}
	if o.Gone {
		return Decision{Action: AwaitUser, Reason: "target gone (archived, deleted, or not found)"}
	}
	if len(o.AwaitingChildren) > 0 {
		return Decision{Action: AwaitUser, Reason: "children waiting on a person (pending interaction): " + strings.Join(o.AwaitingChildren, ", ")}
	}
	if o.DeferredInteraction {
		return Decision{Action: AwaitUser, Reason: DeferredInteractionReason, DeferredInteraction: true}
	}
	if o.AwaitingInput {
		return none("waiting on a person (pending interaction)")
	}
	if o.ActiveChildren > 0 {
		if childErrorWake(in) {
			return Decision{Action: Wake, Reason: ChildErrorWakeReason, ChildErrorWake: true}
		}
		return none("child work running")
	}
	if o.ChildrenUnverified {
		return none("child status unverified")
	}
	if !in.InWindow {
		return none("outside window")
	}
	if o.RetryUnverified {
		return none("provider retry state unverified")
	}
	if o.Retrying && !limitOutranksRetry(in) {
		return none("provider is retrying the turn")
	}
	switch o.State {
	case Stopping, Pending:
		return none("session " + string(o.State))
	case Unknown:
		return none("status unknown")
	case Error:
		return decideError(in)
	case Idle:
		if o.Background > 0 {
			return none("background work running")
		}
		if o.Queued > 0 {
			return none("messages already queued")
		}
		if in.NetworkDown {
			return none(NetworkDownReason)
		}
		// Only between turns: never pull the loop from a fallback mid-turn.
		if in.SwitchBackReady {
			return Decision{Action: SwitchBack, Reason: "primary's limit has reset"}
		}
		return Decision{Action: Wake, Reason: "idle"}
	case Active:
		// Without Git tracking there is no stall evidence and no nudge would
		// help. A session whose retry notice expired and which has stayed
		// silent for the patience a tracked loop would have shown (one stall
		// interval, then every nudge interval) needs a person.
		if o.RetryExpired && !in.ProgressTracked && in.RetrySilentFor >= SilentRetryPatience(in.StallAfter, in.MinWakeInterval, in.MaxNudges) {
			return Decision{Action: AwaitUser, Reason: "provider retry went silent on an active session for " + in.RetrySilentFor.Truncate(time.Minute).String() + "; no Git progress tracking to nudge by"}
		}
		if in.UnchangedFor >= in.StallAfter && in.SinceWake >= in.MinWakeInterval {
			if in.NetworkDown {
				// Neither a nudge nor the hold that follows the last one.
				return none(NetworkDownReason)
			}
			if in.MaxNudges > 0 && in.Nudges >= in.MaxNudges {
				return Decision{Action: AwaitUser, Reason: "no progress after " + itoa(in.Nudges) + " nudges"}
			}
			return Decision{Action: Nudge, Reason: "no progress for " + in.UnchangedFor.Truncate(time.Minute).String()}
		}
		return none("active")
	}
	return none("unrecognised state")
}

// limitOutranksRetry reports whether a retry notice must yield to the limit
// rules. A rate-limited, errored session whose structured reset is further
// away than wait_if_reset_within, with no retry actually queued, is handled
// by decideError (handoff when allowed, a limit wait otherwise), so a notice
// cannot hold the loop on a limited account past a healthy fallback.
func limitOutranksRetry(in Input) bool {
	o, f := in.Obs, in.Failover
	return o.State == Error && o.ErrorClass == ErrRateLimit && o.Queued == 0 && f.ResetIn > 0 && f.ResetIn > f.WaitIfResetWithin
}

// SilentRetryPatience is how long an untracked active session may stay silent
// after its retry notice expired before a person is asked.
func SilentRetryPatience(stallAfter, minWakeInterval time.Duration, maxNudges int) time.Duration {
	return stallAfter + time.Duration(max(maxNudges, 1))*minWakeInterval
}

// decideError bounds recovery from an errored session. A handoff happens
// only for an error class the loop is configured to fail over on, and only
// when a confirmed fallback is available. Unknown errors never hand off.
func decideError(in Input) Decision {
	o, f := in.Obs, in.Failover
	if o.Queued > 0 {
		return none("messages already queued after error")
	}
	// Ahead of every recovery rule, including the caps: a re-prompt or brief
	// sent into a dead network fails and spends its allowance for nothing,
	// and a hold raised because of it would outlast the outage.
	if in.NetworkDown {
		return none(NetworkDownReason)
	}
	if f.Eligible && o.ErrorClass != ErrUnknown && o.ErrorClass != ErrNone && o.ErrorClass != "" {
		if f.ResetIn > 0 && f.ResetIn <= f.WaitIfResetWithin {
			return limitWait(WaitResetSoon, "limit resets in "+f.ResetIn.Round(time.Minute).String()+"; waiting")
		}
		if f.Available {
			if in.HandoffDeferred {
				return none(HandoffDeferredReason)
			}
			return Decision{Action: Handoff, Reason: "error: " + string(o.ErrorClass)}
		}
		if f.ResetIn > 0 {
			return limitWait(WaitNoFallback, "limit resets in "+f.ResetIn.Round(time.Minute).String()+"; no confirmed fallback")
		}
	}
	// Disabling handoffs must not turn a known cooldown into failed recovery
	// attempts and a durable user hold. Keep observing until it lifts instead.
	if o.ErrorClass == ErrRateLimit && f.ResetIn > 0 {
		return limitWait(WaitFailoverDisabled, "limit resets in "+f.ResetIn.Round(time.Minute).String()+"; failover disabled; waiting")
	}
	// Retrying cannot fix an auth error.
	if o.ErrorClass == ErrAuth {
		return Decision{Action: AwaitUser, Reason: "auth error"}
	}
	if in.Reprompts >= in.MaxReprompts {
		return Decision{Action: AwaitUser, Reason: "error persisted after re-prompts"}
	}
	if in.Reprompts > 0 && in.SinceLastReprompt < in.Backoff {
		return none("backing off after error")
	}
	return Decision{Action: Reprompt, Reason: "error: " + string(classOrUnknown(o.ErrorClass))}
}

// Counters are the per-episode send records the command layer persists and
// feeds back into Input: Nudges, Reprompts, SinceWake and SinceLastReprompt.
type Counters struct {
	Nudges         int
	Reprompts      int
	LastSendAt     time.Time
	LastRepromptAt time.Time
}

// Recovery is how a re-prompt decision is carried out.
type Recovery string

// Recovery methods.
const (
	// RecoverByPrompt sends the wake text again.
	RecoverByPrompt Recovery = "prompt"
	// RecoverByTurnRetry asks the provider to re-run the failed turn by
	// reference, with no new text.
	RecoverByTurnRetry Recovery = "turn_retry"
)

// RecoverBy chooses the method for a re-prompt decision. A turn retry is used
// only for a session in error whose class a retry can plausibly fix: network,
// overloaded or unknown. A rate limit is not cured by running the same turn
// again and an auth error needs a person, so those keep the existing prompt
// path (auth is held before it gets here). Anything that is not a re-prompt
// of an errored session on a provider that can retry is a prompt.
func RecoverBy(d Decision, obs Observation, canRetryTurn bool) Recovery {
	if d.Action != Reprompt || !canRetryTurn || obs.State != Error {
		return RecoverByPrompt
	}
	switch obs.ErrorClass {
	case ErrNetwork, ErrOverloaded, ErrUnknown, ErrNone, "":
		return RecoverByTurnRetry
	}
	return RecoverByPrompt
}

// Attempted returns the counters after the command layer has called the
// adapter for a wake, nudge or re-prompt at the given time. It deliberately
// takes no outcome: a send that failed, was unsupported or is uncertain may
// still have been delivered, so it spends the same cap and starts the same
// spacing as one that returned cleanly. Other actions leave the counters alone.
func Attempted(a Action, c Counters, at time.Time) Counters {
	switch a {
	case Wake:
		c.LastSendAt = at
	case Nudge:
		c.LastSendAt = at
		c.Nudges++
	case Reprompt:
		c.LastSendAt = at
		c.Reprompts++
		c.LastRepromptAt = at
	}
	return c
}

// ChildErrorWakeReason is the reason of the one wake that errored children
// cause by themselves.
const ChildErrorWakeReason = "children in error while other children run"

// childErrorNoticeDue: some errored child has not been announced, and the
// announcement has not been given up after repeated rejected sends.
func childErrorNoticeDue(in Input) bool {
	return len(in.Obs.ErroredChildren) > 0 && in.UnannouncedErroredChildren > 0 && in.ChildErrorNoticeAttempts < in.MaxChildErrorNotices
}

// childErrorWake is the single exception to "a parent with an active child is
// busy". A child the parent has not been told about is in error while others
// still run: without a wake it would not hear until the last active child
// finished. It applies only where an idle wake would otherwise be allowed
// (idle, in the window, nothing queued or running in the background,
// discovery complete, no provider retry pending), only for newly errored
// children, no sooner than MinWakeInterval after the last send, and no more
// than MaxChildErrorNotices rejected sends per announcement.
func childErrorWake(in Input) bool {
	o := in.Obs
	return childErrorNoticeDue(in) &&
		o.State == Idle && in.InWindow && !o.ChildrenUnverified &&
		!o.RetryUnverified && !o.Retrying && o.Queued == 0 && o.Background == 0 &&
		in.SinceWake >= in.MinWakeInterval
}

// ChildErrorNote returns how many errored children a message should announce,
// or zero for none. A wake of an idle session and a re-prompt of an errored
// one carry the note, and only
// while a newly errored child is unannounced: the supervisor never retries,
// messages or stops a child, so the owning session is told and decides. The
// count is every child now in error, including ones announced before. Every
// hold and gate that stops the wake stops the note with it. The note rides on
// the ordinary idle wake, or is the whole message of the one wake
// childErrorWake allows.
func ChildErrorNote(in Input, d Decision) int {
	if (d.Action != Wake && d.Action != Reprompt) || !childErrorNoticeDue(in) || in.Obs.ChildrenUnverified {
		return 0
	}
	return len(in.Obs.ErroredChildren)
}

// ChildHashes returns one short hash per distinct child ID, sorted. Records of
// what was announced hold these instead of IDs.
func ChildHashes(ids []string) []string {
	seen := map[string]bool{}
	for _, id := range ids {
		sum := sha256.Sum256([]byte(id))
		seen[hex.EncodeToString(sum[:12])] = true
	}
	hashes := make([]string, 0, len(seen))
	for h := range seen {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	return hashes
}

// ChildReport says what to do about telling the owner that children are in
// error. It is separate from the notice to the parent session: the owner is
// told when a failure is first seen, the parent when it is next woken.
type ChildReport string

// Child report outcomes.
const (
	ChildReportNone          ChildReport = "none"           // no newly errored child
	ChildReportSend          ChildReport = "requested"      // send one report
	ChildReportNotSubscribed ChildReport = "not_subscribed" // report.events does not include child_error
	ChildReportCapped        ChildReport = "capped"         // this episode has sent its maximum
)

// CoveredChild is one child the parent session or the owner has been told
// about. Hash is its ChildHashes value, never the ID. RecoveredAt is zero
// while the child was in error when last seen.
type CoveredChild struct {
	Hash        string    `json:"hash"`
	ErroredAt   time.Time `json:"erroredAt"`             // when it was last seen in error
	RecoveredAt time.Time `json:"recoveredAt,omitempty"` // when it was first seen out of error since then
}

// CoveredChildren is the one rule for "which children in error are new". The
// notice to the parent session and the report to the owner keep separate
// records, because they are told at different times, but both records are
// advanced by this function, so they cannot disagree about what counts as a
// new failure. unreported lists errored children the record does not cover:
// only growth is new, never a set that merely shrank.
//
// A covered child is forgotten only after it has been seen out of error for
// forgetAfter. Until then a child that fails again is the same failure, so one
// that flaps between error and a provider retry is not told again each time.
// A child counts as out of error when the listing is complete and does not
// show it in error, or when it is in healthy. An incomplete listing that
// simply does not show a child proves nothing about it, so that child is kept
// unchanged.
func CoveredChildren(errored, healthy []string, covered []CoveredChild, complete bool, now time.Time, forgetAfter time.Duration) (kept []CoveredChild, unreported []string) {
	inError, out := map[string]bool{}, map[string]bool{}
	for _, h := range errored {
		inError[h] = true
	}
	for _, h := range healthy {
		out[h] = true
	}
	known := map[string]bool{}
	kept, unreported = []CoveredChild{}, []string{}
	for _, c := range covered {
		if known[c.Hash] {
			continue
		}
		if inError[c.Hash] && !c.RecoveredAt.IsZero() && now.Sub(c.RecoveredAt) >= forgetAfter {
			// Out of error for the floor and now failing again, with no check
			// in between to forget it: a new failure all the same.
			continue
		}
		known[c.Hash] = true
		switch {
		case inError[c.Hash]:
			c.ErroredAt, c.RecoveredAt = now, time.Time{}
		case complete || out[c.Hash]:
			if c.RecoveredAt.IsZero() {
				c.RecoveredAt = now
			}
			if now.Sub(c.RecoveredAt) >= forgetAfter {
				continue // recovered for long enough: forgotten
			}
		}
		kept = append(kept, c)
	}
	for _, h := range errored {
		if !known[h] {
			known[h] = true
			unreported = append(unreported, h)
		}
	}
	return kept, unreported
}

// CoverChildren adds newly told children to a record. The record is bounded:
// past max entries, the ones seen in error longest ago are dropped.
func CoverChildren(kept []CoveredChild, unreported []string, now time.Time, max int) []CoveredChild {
	out := append([]CoveredChild{}, kept...)
	for _, h := range unreported {
		out = append(out, CoveredChild{Hash: h, ErroredAt: now})
	}
	if len(out) > max {
		sort.SliceStable(out, func(i, j int) bool { return out[i].ErroredAt.After(out[j].ErroredAt) })
		out = out[:max]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out
}

// ChildErrorReport decides whether newly errored children are reported to the
// owner. reports counts the reports already requested in this episode, which
// lasts while any child stays in error; max bounds them, so children failing
// one after another cannot produce a stream.
func ChildErrorReport(unreported, reports, max int, subscribed bool) ChildReport {
	switch {
	case unreported <= 0:
		return ChildReportNone
	case !subscribed:
		return ChildReportNotSubscribed
	case reports >= max:
		return ChildReportCapped
	}
	return ChildReportSend
}

// NewThreadVerdict says whether a handoff may create another thread.
type NewThreadVerdict string

// New thread verdicts.
const (
	NewThreadAllowed NewThreadVerdict = "allowed"
	NewThreadCapped  NewThreadVerdict = "new_thread_cap"     // the loop has used its maximum
	NewThreadTooSoon NewThreadVerdict = "new_thread_spacing" // the last one was too recent
)

// NewThreadGate bounds the threads a loop creates. attempts counts every
// creation whose outcome was a thread or is unknown: only one proven never to
// have reached BB is left out, so a lost answer cannot make room for another
// thread. Below the cap, two creations are at least minInterval apart. With
// no cap configured no thread is created.
func NewThreadGate(attempts, max int, sinceLast, minInterval time.Duration, any bool) NewThreadVerdict {
	switch {
	case max <= 0 || attempts >= max:
		return NewThreadCapped
	case any && sinceLast < minInterval:
		return NewThreadTooSoon
	}
	return NewThreadAllowed
}

// NewThreadCapReason is the hold raised when a handoff needed a new thread
// and the loop had already used its maximum.
const NewThreadCapReason = "new thread cap reached: this loop has used its maximum of BB threads for handoffs; raise loop.limits.max_new_threads or retarget before resuming"

// BriefAttempts records one target's rejected takeover briefs within the
// current episode: handoff briefs to one fallback while an error lasts, or
// switch-back briefs to the primary while the loop stays on a fallback.
type BriefAttempts struct {
	Failed int
	LastAt time.Time
}

// BriefVerdict says whether a target may be offered another brief.
type BriefVerdict string

// Brief verdicts.
const (
	BriefAllowed    BriefVerdict = "allowed"
	BriefBackingOff BriefVerdict = "backing_off" // a rejected brief is too recent
	BriefCapped     BriefVerdict = "capped"      // no more briefs this episode
)

// BriefGate bounds briefs per target. The count and spacing belong to the one
// target, so a fallback that keeps rejecting its brief is skipped without
// spending the attempts of the others. A target at its cap is skipped for the
// rest of the episode; below it, briefs are at least backoff apart. The gate
// applies before the exact probe, so a skipped target is not probed either.
func BriefGate(a BriefAttempts, now time.Time, max int, backoff time.Duration) BriefVerdict {
	switch {
	case a.Failed >= max:
		return BriefCapped
	case a.Failed > 0 && now.Sub(a.LastAt) < backoff:
		return BriefBackingOff
	}
	return BriefAllowed
}

// BriefRejected returns the record after a brief was not accepted at the given
// time. Like Attempted it takes no reason: a failed activation, a failed send
// and an unsupported send all count, because each may repeat on every check.
func BriefRejected(a BriefAttempts, at time.Time) BriefAttempts {
	a.Failed++
	a.LastAt = at
	return a
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func classOrUnknown(c ErrorClass) ErrorClass {
	if c == "" || c == ErrNone {
		return ErrUnknown
	}
	return c
}

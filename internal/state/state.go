// Package state owns the machine-written records in a loop's state
// directory: state.json for what the last tick knew and runs.jsonl for an
// append-only event log.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// FileName of the state record.
const FileName = "state.json"

// LogName of the event log.
const LogName = "runs.jsonl"

// Primary is the active-target id of the loop's own agent.
const Primary = "primary"

// File is what one tick leaves for the next.
type File struct {
	LastPromptSend *PromptSend `json:"lastPromptSend,omitempty"`
	StallRetry     *StallRetry `json:"stallRetry,omitempty"`
	// SilentRetry records when the active target was first observed with an
	// expired retry notice, so its continued silence can be bounded.
	SilentRetry *SilentRetry `json:"silentRetry,omitempty"`
	// HandoffDeferrals counts consecutive checks in which a due handoff could
	// not be confirmed within the check's time budget. ExactCut counts, per
	// fallback, consecutive exact probes stopped early by that budget.
	// FailedChecks counts consecutive recorded checks whose outcome was failed,
	// and FailedSince is when that run began.
	// NetworkDownSince is when a connectivity check first failed in the
	// current run of failures; zero once a check did not find it down.
	// RecentCheckGaps are the times between the last few checks, newest last.
	// The longest of them is the loop's usual interval, which tells a gap in
	// which no check ran from a scheduler that is simply slow.
	RecentCheckGaps []time.Duration `json:"recentCheckGapsNs,omitempty"`
	// ActiveSince is the earliest moment the stall clock may count from: when
	// the target was seen active again after an error or an outage, or the
	// last Git change moved forward by time in which no check ran. Zero means
	// the clock counts from the last Git change.
	ActiveSince      time.Time `json:"activeSince,omitempty"`
	NetworkDownSince time.Time `json:"networkDownSince,omitempty"`
	// NetworkVerifiedAt is when the provider was last tested directly during
	// that outage, NetworkVerifications how many times, and NetworkReportedAt
	// when the outage was last reported.
	// NetworkDistrusted says the check is no longer believed, because the
	// provider answered a probe while the check was failing. It stays set
	// until the check itself passes or the session is seen active or idle.
	NetworkVerifiedAt    time.Time      `json:"networkVerifiedAt,omitempty"`
	NetworkVerifications int            `json:"networkVerifications,omitempty"`
	NetworkReportedAt    time.Time      `json:"networkReportedAt,omitempty"`
	NetworkDistrusted    bool           `json:"networkDistrusted,omitempty"`
	FailedChecks         int            `json:"failedChecks,omitempty"`
	FailedSince          time.Time      `json:"failedSince,omitempty"`
	HandoffDeferrals     int            `json:"handoffDeferrals,omitempty"`
	ExactCut             map[string]int `json:"exactCut,omitempty"`
	Fingerprint          string         `json:"fingerprint,omitempty"`
	LastProgressAt       time.Time      `json:"lastProgressAt,omitempty"`
	LastWakeAt           time.Time      `json:"lastWakeAt,omitempty"`
	CheckedAt            time.Time      `json:"checkedAt,omitempty"`
	LastState            string         `json:"lastState,omitempty"`
	LastCheck            *Check         `json:"lastCheck,omitempty"`
	Reprompts            int            `json:"reprompts,omitempty"`
	Nudges               int            `json:"nudges,omitempty"` // since progress or the most recent turn end
	LastRepromptAt       time.Time      `json:"lastRepromptAt,omitempty"`
	StopRequestedSent    bool           `json:"stopSent,omitempty"`

	ActiveTarget  string    `json:"activeTarget,omitempty"` // "" or "primary" means the loop's own agent
	LastHandoffAt time.Time `json:"lastHandoffAt,omitempty"`
	Handoffs      int       `json:"handoffs,omitempty"`
	Degraded      bool      `json:"degraded,omitempty"`
	// PrimaryResetsAt is when the primary's limit was reported to lift at the
	// handoff away from it; switch-back waits for it.
	PrimaryResetsAt   time.Time `json:"primaryResetsAt,omitempty"`
	LastSwitchBackTry time.Time `json:"lastSwitchBackTry,omitempty"`
	// HandoffBriefs counts rejected handoff briefs per fallback within the
	// current error episode. SwitchBackRejected counts rejected switch-back
	// briefs while the loop stays on its current fallback.
	HandoffBriefs      map[string]BriefRecord `json:"handoffBriefs,omitempty"`
	SwitchBackRejected int                    `json:"switchBackRejected,omitempty"`
	// ChildErrorsAnnounced covers the errored children the session has been
	// told about, by hash, never by ID. A child is dropped once it has stayed
	// out of error for min_wake_interval; the record is bounded.
	ChildErrorsAnnounced []decide.CoveredChild `json:"childErrorsNoticed,omitempty"`
	// ChildErrorsAnnouncedV1 is the record an earlier unreleased build wrote: a
	// plain list of hashes with no recovery time. It is read once, converted,
	// and never written again.
	ChildErrorsAnnouncedV1 []string `json:"childErrorsAnnounced,omitempty"`
	// ChildErrorNoticeFor identifies the pending announcement (the hashes not
	// yet announced) that ChildErrorNoticeAttempts rejected sends belong to.
	ChildErrorNoticeFor      string `json:"childErrorNoticeFor,omitempty"`
	ChildErrorNoticeAttempts int    `json:"childErrorNoticeAttempts,omitempty"`
	// AwaitingReported is set once a pending interaction has been reported,
	// so the person is told once, not on every tick.
	AwaitingReported bool `json:"awaitingReported,omitempty"`
	// LimitWaitReport is set while a long limit wait is in progress, so its
	// single report is not repeated by later checks or after a restart.
	LimitWaitReport *LimitWaitReport `json:"limitWaitReport,omitempty"`
	// LastTurnRetry is the last recovery made by retrying a failed turn.
	LastTurnRetry *TurnRetry `json:"lastTurnRetry,omitempty"`
	// Lanes is the record of lane recovery (bb.children.retry_errored). It
	// holds hashes of lane IDs, never the IDs.
	Lanes *LaneRecovery `json:"lanes,omitempty"`
	// TurnRetryNoOps counts consecutive checks in which the provider answered
	// that a retry was already queued while it reported nothing queued.
	TurnRetryNoOps int `json:"turnRetryNoOps,omitempty"`
	// NewThreads holds the thread created for each candidate with
	// bb.new_thread. NewThreadsCreated counts, for
	// loop.limits.max_new_threads, every creation that produced a thread or
	// whose outcome is unknown; only one proven never to have reached BB is
	// not counted. LastNewThreadAt is when the latest counted one was asked
	// for. NewThreadIntent is a creation whose outcome is not yet recorded.
	NewThreads        map[string]CreatedThread `json:"newThreads,omitempty"`
	NewThreadsCreated int                      `json:"newThreadsCreated,omitempty"`
	LastNewThreadAt   time.Time                `json:"lastNewThreadAt,omitempty"`
	NewThreadIntent   *NewThreadIntent         `json:"newThreadIntent,omitempty"`
	// ChildErrorReport is set while any child of the supervised thread is in
	// error, so its report is not repeated by later checks or after a restart.
	ChildErrorReport *ChildErrorReport `json:"childErrorReport,omitempty"`
}

// ChildErrorReport outcomes; the first three mean what the limit-wait ones do.
const (
	ChildReportRequested     = "requested"
	ChildReportFailed        = "delivery_failed"
	ChildReportNotSubscribed = "not_subscribed"
	ChildReportCapped        = "capped" // newly errored children were seen after the episode's last report
)

// ChildErrorReport describes the owner reports for one episode of errored
// children: from the first child seen in error until every covered child has
// been out of error long enough to be forgotten. Children holds hashes, never
// child IDs, and is bounded.
type ChildErrorReport struct {
	Children   []decide.CoveredChild `json:"children,omitempty"`
	Reports    int                   `json:"reports,omitempty"` // report attempts in this episode
	Outcome    string                `json:"outcome"`
	DetectedAt time.Time             `json:"detectedAt"`
	// What the detecting check saw. Complete is false when the child listing
	// was incomplete, so the counts are lower bounds.
	Errored  int  `json:"errored"`
	Running  int  `json:"running"`
	Complete bool `json:"complete"`
}

// LaneRecovery is what the supervisor remembers about the lanes of the
// current target, for bb.children.retry_errored. Every key is a hash of a
// lane ID; no ID, title or text is stored.
type LaneRecovery struct {
	// SeenActive is when each lane was last seen working. A lane absent from
	// it is never retried.
	SeenActive map[string]time.Time `json:"seenActive,omitempty"`
	// Retries holds the lanes that were retried or looked at and not yet forgotten.
	Retries map[string]LaneRetry `json:"retries,omitempty"`
	// EpisodeRetries counts retries since Retries was last empty.
	EpisodeRetries int `json:"episodeRetries,omitempty"`
	// Reports counts child_recovery reports in the same episode.
	Reports int `json:"reports,omitempty"`
	// Deferred lists lanes the last check did not reach for lack of time;
	// the next check takes them first.
	Deferred []string `json:"deferred,omitempty"`
	// Last summarizes the latest check that did lane work, for status.
	Last *LaneCheck `json:"last,omitempty"`
}

// LaneRetry is one lane's record.
type LaneRetry struct {
	Attempts int       `json:"attempts,omitempty"`
	LastAt   time.Time `json:"lastAt,omitempty"`
	// LookedAt is when the lane was last inspected and left alone. It spaces
	// inspections the way LastAt spaces retries.
	LookedAt time.Time `json:"lookedAt,omitempty"`
	// Outcome is the provider's answer to the last retry, or the reason the
	// lane was last not retried.
	Outcome string `json:"outcome,omitempty"`
	GivenUp bool   `json:"givenUp,omitempty"`
	// Reported: a child_recovery report has covered this lane's give-up or
	// its failure class.
	Reported bool `json:"reported,omitempty"`
	// HealthySince is when the lane was first seen out of error since its
	// last failure. The record is dropped once that has lasted
	// progress.min_wake_interval.
	HealthySince time.Time `json:"healthySince,omitempty"`
}

// LaneCheck is the lane work of one check. Counts only.
type LaneCheck struct {
	At       time.Time `json:"at"`
	Retried  int       `json:"retried,omitempty"`
	Deferred int       `json:"deferred,omitempty"`
	GivenUp  int       `json:"givenUp,omitempty"`
	// NotRetried counts lanes looked at and left alone, by reason.
	NotRetried map[string]int `json:"notRetried,omitempty"`
	// Waiting is "network" when the connectivity check failed.
	Waiting string `json:"waiting,omitempty"`
}

// TurnRetry records the last time a failed turn was retried by reference
// instead of being prompted again. Outcome is the provider's structured
// result, or "refused" when it refused or could not be read. It is saved with
// the completed check and proves neither delivery nor that the turn ran.
type TurnRetry struct {
	CheckAt time.Time `json:"checkAt"`
	Target  string    `json:"target"`
	Outcome string    `json:"outcome"`
	Attempt int       `json:"attempt,omitempty"`
}

// LimitWaitReport outcomes.
const (
	LimitWaitRequested     = "requested"       // saved before sending; the send was started or returned without error, receipt unverified
	LimitWaitFailed        = "delivery_failed" // the one attempt failed and is not retried
	LimitWaitNotSubscribed = "not_subscribed"  // report.events does not include limit_wait
)

// LimitWaitReport describes the one report attempt for a wait on a provider
// limit whose reset is further away than errors.report_wait_after. Target is
// the configured alias, as elsewhere in this private file.
type LimitWaitReport struct {
	Target     string    `json:"target"`
	ResetsAt   time.Time `json:"resetsAt"`
	DetectedAt time.Time `json:"detectedAt"`
	Outcome    string    `json:"outcome"`
	// Reports counts report attempts in this wait, including this one.
	Reports int `json:"reports,omitempty"`
}

// CreatedThread is the BB thread created for a handoff to a candidate with
// bb.new_thread. It is that candidate's thread until a later handoff to the
// same candidate creates another.
type CreatedThread struct {
	Thread    string    `json:"thread"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	// From is the target alias the loop left; Continues is that target's BB
	// thread, when it has one.
	From      string `json:"from"`
	Continues string `json:"continues,omitempty"`
}

// NewThreadIntent is saved before a thread is created and cleared once the
// outcome is known, so a crash or a restart in between can find the thread by
// its title and never create a second one for the same handoff.
type NewThreadIntent struct {
	Candidate   string    `json:"candidate"`
	Title       string    `json:"title"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	From        string    `json:"from"`
	Continues   string    `json:"continues,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	RequestedAt time.Time `json:"requestedAt"`
	// NotStarted records that the bb CLI could not be started for the spawn,
	// so the hold can say so. The outcome is still treated as unknown.
	NotStarted bool `json:"notStarted,omitempty"`
}

// CreatedThreadFor returns the thread created for a candidate, or "".
func (f File) CreatedThreadFor(candidate string) string {
	return f.NewThreads[candidate].Thread
}

// CandidateAgent returns the candidate's agent as it can be used right now.
// A candidate that starts a new thread at handoff has no thread in its
// configuration: its thread is the one created for it, and ok is false until
// there is one. Every other candidate is returned unchanged.
func (f File) CandidateAgent(c config.Candidate) (a config.Agent, ok bool) {
	a = c.Agent
	if a.Adapter != "bb" || a.BB == nil || !a.BB.NewThread {
		return a, true
	}
	thread := f.CreatedThreadFor(c.ID)
	if thread == "" {
		return a, false
	}
	target := *a.BB
	target.Thread = thread
	a.BB = &target
	return a, true
}

// BriefRecord is one target's rejected takeover briefs.
type BriefRecord struct {
	Failed int       `json:"failed"`
	LastAt time.Time `json:"lastAt"`
}

// ClearChildErrors forgets what was announced about errored children. They
// belong to one thread, so it is called whenever the loop's target changes.
func (f *File) ClearChildErrors() {
	f.ChildErrorsAnnounced, f.ChildErrorsAnnouncedV1, f.ChildErrorNoticeFor, f.ChildErrorNoticeAttempts = nil, nil, "", 0
	f.ChildErrorReport = nil
	// The lanes belong to the conversation that was left.
	f.Lanes = nil
}

// ClearBriefs starts a new episode for handoff and switch-back briefs.
func (f *File) ClearBriefs() {
	f.HandoffBriefs, f.SwitchBackRejected = nil, 0
	f.ClearDeferrals()
}

// ClearDeferrals forgets handoffs put off for lack of time.
func (f *File) ClearDeferrals() {
	f.HandoffDeferrals, f.ExactCut = 0, nil
}

// SilentRetry is target-scoped and cleared as soon as the target is not both
// active and carrying an expired retry notice.
type SilentRetry struct {
	Target string    `json:"target"`
	Since  time.Time `json:"since"`
}

// StallRetry is a target-scoped grace period for ordinary stall nudges only.
// It never replaces Git evidence or refunds loop-wide allowances.
type StallRetry struct {
	Target    string    `json:"target"`
	Waiting   bool      `json:"waiting"`
	ResumedAt time.Time `json:"resumedAt,omitempty"`
}

// Check records a completed tick's decision and outcome, not a live session
// status. It contains no prompts, provider output, or raw error text.
type Check struct {
	At             time.Time `json:"at"`
	Version        string    `json:"version,omitempty"`
	Target         string    `json:"target"`
	ObservedTarget string    `json:"observedTarget,omitempty"`
	ObservedState  string    `json:"observedState"`
	AwaitingInput  bool      `json:"awaitingInput,omitempty"`
	// RetrySignal is "expired" when the check saw a stale provider retry
	// notice and stopped honoring it, "declined" when the provider's retry
	// service had declined to retry the failure; empty otherwise.
	RetrySignal string `json:"retrySignal,omitempty"`
	// ErroredChildren is how many direct child threads were in error.
	// ErroredChildrenPartial: child discovery was incomplete, so the count is
	// a lower bound. ChildErrorNoticeStopped: the notice was given up after
	// repeated rejected sends.
	ErroredChildren         int    `json:"erroredChildren,omitempty"`
	ErroredChildrenPartial  bool   `json:"erroredChildrenPartial,omitempty"`
	ChildErrorNoticeStopped bool   `json:"childErrorNoticeStopped,omitempty"`
	Action                  string `json:"action"`
	Reason                  string `json:"reason"`
	Outcome                 string `json:"outcome"` // succeeded, failed, or dry_run
	DryRun                  bool   `json:"dryRun,omitempty"`
}

// Target returns the active target id, defaulting to primary.
func (f File) Target() string {
	if f.ActiveTarget == "" {
		return Primary
	}
	return f.ActiveTarget
}

// Load reads state.json; a missing file is an empty record.
func Load(stateDir string) (File, error) {
	var f File
	// Status readers poll this file while a daemon replaces it. Open it so a
	// reader never makes the writer's replacement fail (Windows).
	r, err := snapshot.Open(filepath.Join(stateDir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	b, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	if f.LastPromptSend != nil && !f.LastPromptSend.Valid() {
		f.LastPromptSend = nil
	}
	return f, nil
}

// Save writes state.json atomically with owner-only permissions.
func Save(stateDir string, f File) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, FileName)
	tmp, err := os.CreateTemp(stateDir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	// This record is what stops a stop or prompt being repeated. Sync it
	// before publishing, as the send ledger does. Not a power-loss guarantee.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return snapshot.Replace(name, path)
}

// Event is one line of runs.jsonl.
type Event struct {
	At     time.Time `json:"at"`
	Loop   string    `json:"loop"`
	Event  string    `json:"event"`
	Action string    `json:"action,omitempty"`
	Reason string    `json:"reason,omitempty"`
	State  string    `json:"state,omitempty"`
	Target string    `json:"target,omitempty"`
	Error  string    `json:"error,omitempty"`
	// Outcome of a tick: succeeded, failed or dry_run.
	Outcome string         `json:"outcome,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// Append adds an event to runs.jsonl.
func Append(stateDir string, e Event) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, LogName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

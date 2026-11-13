// Package incident stores bounded, typed diagnostic snapshots. It is never an
// input to supervision decisions or control/allowance accounting.
package incident

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// Record describes a tick, a saved control update or a saved manual retarget.
// Timestamps describe local operations, not provider event time. Unsupported
// source details are explicitly unavailable.
type Record struct {
	Kind            string `json:"kind"`
	LimitWaitReport string `json:"limitWaitReport"`
	// NewThread says this check recorded a BB thread keepgoing created for a
	// handoff. The thread ID is in the run graph, not here.
	NewThread            bool           `json:"newThread,omitempty"`
	TurnRetry            string         `json:"turnRetry"`
	Tier                 string         `json:"tier"`
	Collapsed            *Collapsed     `json:"collapsed"`
	TargetKeyAfter       string         `json:"targetKeyAfter"`
	Probes               Probes         `json:"probes"`
	ControlChange        *ControlChange `json:"controlChange"`
	Decisions            Decisions      `json:"decisions"`
	CountersAvailable    bool           `json:"countersAvailable"`
	Cause                string         `json:"cause"`
	Sequence             uint64         `json:"sequence"`
	CheckStartedAt       time.Time      `json:"checkStartedAt"`
	Build                string         `json:"build"`
	Adapter              string         `json:"adapter"`
	TargetKey            string         `json:"targetKey"`
	Observation          Observation    `json:"observation"`
	ControlBefore        *Controls      `json:"controlBefore"`
	ControlAfter         *Controls      `json:"controlAfter"`
	Action               string         `json:"action"`
	Outcome              string         `json:"outcome"`
	DryRun               bool           `json:"dryRun"`
	Nudges               int            `json:"nudges"`
	Reprompts            int            `json:"reprompts"`
	LastActiveRecoveryAt *time.Time     `json:"lastActiveRecoveryAt"`
}

// ControlChange describes a successful save, not report delivery or an agent
// stopping. Owner identities and private control notes are deliberately absent.
type ControlChange struct {
	Operation          string    `json:"operation"`
	SaveObservedAt     time.Time `json:"saveObservedAt"`
	OwnerPresentBefore bool      `json:"ownerPresentBefore"`
	OwnerPresentAfter  bool      `json:"ownerPresentAfter"`
	OwnershipChanged   bool      `json:"ownershipChanged"`
}

// Retention tiers. Each has reserved room that the others cannot take, so
// noise in one cannot evict another.
//   - key: who had the loop and whether it was held. A completed target move,
//     a retarget, an ownership change, a hold flag entered or cleared.
//   - condition: evidence of an ongoing problem. Limit waits, failed probes,
//     and handoff or switch-back attempts that did not move the loop.
//   - activity: sends, failed checks, control commands that changed nothing.
//   - routine: a check that observed and did nothing.
const (
	TierKey       = "key"
	TierCondition = "condition"
	TierActivity  = "activity"
	TierRoutine   = "routine"
)

// Collapsed summarizes checks folded into the record that carries it: Count
// checks identical to it, apart from their times, decision inputs and
// scheduled probes, ran between FirstAt and LastAt (the earliest and latest
// folded check times, whatever order the clock produced them in).
// PassedProbes and FailedProbes count folded scheduled-probe results, each of
// which repeated that candidate's previous result in the run's retained
// records. ProbeTargetKeys lists up to MaxProbes of the candidates they
// probed, so a summary can still be found by target.
type Collapsed struct {
	Count           uint64    `json:"count"`
	FirstAt         time.Time `json:"firstAt"`
	LastAt          time.Time `json:"lastAt"`
	PassedProbes    int       `json:"passedProbes"`
	FailedProbes    int       `json:"failedProbes"`
	ProbeTargetKeys []string  `json:"probeTargetKeys"`
}

// foldedProbeTargets adds the candidates of folded probe results, in first
// seen order, without duplicates and bounded by MaxProbes.
func foldedProbeTargets(keys []string, results []ProbeResult) []string {
	out := append([]string{}, keys...)
	for _, p := range results {
		if len(out) >= MaxProbes {
			break
		}
		seen := !keyPattern.MatchString(p.TargetKey)
		for _, k := range out {
			seen = seen || k == p.TargetKey
		}
		if !seen {
			out = append(out, p.TargetKey)
		}
	}
	return out
}

func sameControls(a, b *Controls) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func targetMoved(r Record) bool {
	return r.TargetKeyAfter != "unavailable" && r.TargetKeyAfter != r.TargetKey
}

// attempted reports a check that tried to move the loop, whether or not it
// did: an exact probe ran, or the engine was re-evaluated after a failed
// handoff or switch-back.
func attempted(r Record) bool {
	if r.Action == "handoff" || r.Action == "switch_back" {
		return true
	}
	for _, p := range r.Probes.Results {
		if p.Tier != "scheduled" {
			return true
		}
	}
	for _, e := range r.Decisions.Evaluations {
		if e.Stage == "handoff_failed" || e.Stage == "switch_back_failed" {
			return true
		}
	}
	return false
}

// repeatable reports a check that executed nothing, changed nothing and
// attempted nothing, so a run of them can be summarized without losing an
// event. A scheduled probe does not prevent it; see consistentProbes.
func repeatable(r Record) bool {
	if r.Kind != "tick" || !sameControls(r.ControlBefore, r.ControlAfter) || targetMoved(r) {
		return false
	}
	dry := r.DryRun || r.Outcome == "dry_run"
	if dry {
		return true // A dry run executes nothing, whatever it proposed.
	}
	return r.Action == "none" && !attempted(r) && r.LimitWaitReport == "none" && !r.NewThread
}

func limitWaitCause(cause string) bool {
	return cause == "limit_wait" || cause == "limit_no_fallback" || cause == "limit_failover_disabled"
}

func failedProbes(r Record) int {
	n := 0
	for _, p := range r.Probes.Results {
		if !p.OK {
			n++
		}
	}
	return n
}

// tierOf classifies a cleaned record for retention.
func tierOf(r Record) string {
	switch r.Kind {
	case "retarget":
		return TierKey
	case "control":
		c := r.ControlChange
		if !sameControls(r.ControlBefore, r.ControlAfter) || (c != nil && (c.OwnershipChanged || c.Operation == "daemon_takeover" || c.Operation == "transfer_begin" || c.Operation == "transfer_finish")) {
			return TierKey
		}
		return TierActivity
	case "tick":
		dry := r.DryRun || r.Outcome == "dry_run"
		if dry {
			return TierRoutine
		}
		if !sameControls(r.ControlBefore, r.ControlAfter) || targetMoved(r) || r.Action == "await_user" {
			return TierKey
		}
		// A record without a second target can only offer its outcome as evidence of a move.
		if (r.Action == "handoff" || r.Action == "switch_back") && r.TargetKeyAfter == "unavailable" && r.Outcome == "succeeded" {
			return TierKey
		}
		if attempted(r) || failedProbes(r) > 0 || limitWaitCause(r.Cause) || r.LimitWaitReport != "none" {
			return TierCondition
		}
		if repeatable(r) && r.Outcome != "failed" {
			return TierRoutine
		}
	}
	return TierActivity
}

// sameBase reports two repeatable checks that differ only in when they ran,
// their decision inputs and their scheduled probes.
func sameBase(a, b Record) bool {
	if !repeatable(a) || !repeatable(b) {
		return false
	}
	for _, r := range []*Record{&a, &b} {
		r.Sequence, r.CheckStartedAt, r.Collapsed, r.LastActiveRecoveryAt, r.Tier = 0, time.Time{}, nil, nil, ""
		r.Decisions, r.Probes = Decisions{}, Probes{}
	}
	return reflect.DeepEqual(a, b)
}

// probeStatus is the latest scheduled-probe result per candidate across a
// run's retained records: an error class when failing, "" when passing.
type probeStatus map[string]string

func (s probeStatus) apply(r Record) {
	for _, p := range r.Probes.Results {
		if p.OK {
			s[p.TargetKey] = ""
		} else {
			s[p.TargetKey] = p.ErrorClass
		}
	}
}

// repeats reports whether every probe result in r equals that candidate's
// last result in the run's retained records. A candidate's first result in a
// run, a first failure, a changed error class and a recovery are all events.
// A result is never assumed: if the record that showed a candidate failing
// has been pruned, its next result is retained whatever it is.
func (s probeStatus) repeats(r Record) bool {
	for _, p := range r.Probes.Results {
		class, known := s[p.TargetKey]
		if !known || (p.OK && class != "") || (!p.OK && class != p.ErrorClass) {
			return false
		}
	}
	return true
}

// foldsLatest reports whether next continues the run that ends the retained
// records, such that the current latest record can be folded into next. The
// run is the trailing records with the same base as next. Its latest record
// may be folded only if an earlier record of the run remains and the latest
// itself added no probe news, so every record that reported something new
// stays retained.
func foldsLatest(records []Record, next Record) bool {
	n := len(records)
	if n < 2 || !sameBase(records[n-1], next) {
		return false
	}
	start := n - 1
	for start > 0 && sameBase(records[start-1], next) {
		start--
	}
	if start == n-1 {
		return false
	}
	status := probeStatus{}
	for _, r := range records[start : n-1] {
		status.apply(r)
	}
	if !status.repeats(records[n-1]) {
		return false
	}
	status.apply(records[n-1])
	return status.repeats(next)
}

// MaxProbes bounds the probe results kept for one tick.
const MaxProbes = 8

// Probes lists the candidate probes a tick ran, in order. Captured with no
// results means the tick ran none; legacy records are unavailable. Probe
// output, models, latency and reset times stay in the private health records.
type Probes struct {
	Availability string        `json:"availability"`
	Results      []ProbeResult `json:"results"`
	Dropped      int           `json:"dropped"`
}

// ProbeResult is a hashed candidate alias and the probe's typed result.
type ProbeResult struct {
	TargetKey  string `json:"targetKey"`
	Tier       string `json:"tier"`
	OK         bool   `json:"ok"`
	ErrorClass string `json:"errorClass"`
}

// Add appends one result. A passing probe has error class none.
func (p *Probes) Add(alias, tier string, ok bool, errorClass string) {
	if ok {
		errorClass = "none"
	}
	p.Availability = "captured"
	p.Results = append(p.Results, ProbeResult{TargetKey: TargetKey(alias), Tier: tier, OK: ok, ErrorClass: errorClass})
	*p = cleanProbes(*p)
}

func cleanProbes(p Probes) Probes {
	if p.Availability != "captured" {
		return Probes{Availability: "unavailable", Results: []ProbeResult{}}
	}
	p.Dropped = max(0, p.Dropped)
	if n := len(p.Results) - MaxProbes; n > 0 {
		p.Dropped += n
		p.Results = p.Results[n:]
	}
	// Copy so cleaning caller-owned records does not mutate their slices.
	results := make([]ProbeResult, len(p.Results))
	copy(results, p.Results)
	p.Results = results
	for i := range p.Results {
		r := &p.Results[i]
		if !keyPattern.MatchString(r.TargetKey) {
			r.TargetKey = "unavailable"
		}
		r.Tier = oneOf(r.Tier, "unavailable", "scheduled", "exact")
		r.ErrorClass = oneOf(r.ErrorClass, "unavailable", "none", "rate_limit", "auth", "restricted", "refused", "overloaded", "network", "unknown")
	}
	return p
}

type Controls struct {
	Enabled      bool `json:"enabled"`
	Paused       bool `json:"paused"`
	Stop         bool `json:"stop"`
	AwaitingUser bool `json:"awaitingUser"`
	Completed    bool `json:"completed"`
}

func Gate(c decide.Control) *Controls {
	return &Controls{c.Enabled, c.Paused, c.Stop, c.AwaitingUser, c.Completed}
}

type Observation struct {
	Source             string `json:"source"`
	Category           string `json:"category"`
	HTTPStatus         *int   `json:"httpStatus"`
	ExitCode           *int   `json:"exitCode"`
	Availability       string `json:"availability"`
	State              string `json:"state"`
	ErrorClass         string `json:"errorClass"`
	Retry              string `json:"retry"`
	SourceTime         string `json:"sourceTime"`
	ProviderCode       string `json:"providerCode"`
	Queued             int    `json:"queued"`
	Background         int    `json:"background"`
	AwaitingInput      bool   `json:"awaitingInput"`
	ActiveChildren     int    `json:"activeChildren"`
	AwaitingChildren   int    `json:"awaitingChildren"`
	ChildrenUnverified bool   `json:"childrenUnverified"`
	// ErroredChildren counts direct children in error; omitted when zero.
	ErroredChildren int  `json:"erroredChildren,omitempty"`
	Gone            bool `json:"gone"`
}

// Observed copies normalized decision evidence, never raw provider output.
func Observed(o decide.Observation, adapter string, failed bool) Observation {
	if failed {
		return Observation{Availability: "unavailable"}
	}
	d := evidence.Clean(o.Evidence)
	r := Observation{Source: d.Source, Category: d.Category, ProviderCode: d.Code, HTTPStatus: d.HTTPStatus, ExitCode: d.ExitCode, Availability: "observed", State: string(o.State), ErrorClass: string(o.ErrorClass),
		Retry: "unavailable", Queued: o.Queued, Background: o.Background, AwaitingInput: o.AwaitingInput,
		ActiveChildren: o.ActiveChildren, AwaitingChildren: len(o.AwaitingChildren), ChildrenUnverified: o.ChildrenUnverified, ErroredChildren: min(len(o.ErroredChildren), 200), Gone: o.Gone}
	if adapter == "bb" && len(o.AwaitingChildren) > 0 {
		r.ErrorClass = "unavailable" // Child-input escalation skips the parent retry scan.
	}
	if adapter == "bb" && len(o.AwaitingChildren) == 0 {
		switch {
		case o.RetryUnverified:
			r.Retry = "unverified"
			r.ErrorClass = "unavailable"
		case o.Retrying:
			r.Retry = "retrying"
		case o.RetryDeclined:
			// The provider's retry service declined to retry this failure and
			// had nothing pending, so the notice was dropped before it aged out.
			r.Retry = "declined"
		case o.RetryExpired:
			// A stale retry notice on a session that is not running; the adapter
			// stopped honoring it, so ordinary error handling applied.
			r.Retry = "expired"
		case o.RetryKnown:
			r.Retry = "not_retrying"
		}
	}
	return r
}

// Target keys correlate configured aliases without publishing their text.
func TargetKey(alias string) string {
	if alias == "" {
		return "unavailable"
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(alias)))
}

// ResolveTargetKey returns a well-formed key unchanged and hashes anything else
// as a configured alias, so a reader can select by either without storing aliases.
func ResolveTargetKey(aliasOrKey string) string {
	if keyPattern.MatchString(aliasOrKey) {
		return aliasOrKey
	}
	return TargetKey(aliasOrKey)
}

var buildPattern = regexp.MustCompile(`^(dev|v?[0-9]+\.[0-9]+\.[0-9]+(?:-dev\.[a-f0-9]{7,40}|-rc\.[0-9]+)?|(?:smoke-)?[a-f0-9]{7,40}(?:-dirty)?)$`)
var keyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func oneOf(value, fallback string, allowed ...string) string {
	for _, v := range allowed {
		if value == v {
			return value
		}
	}
	return fallback
}

// clean is the storage boundary: no arbitrary strings or provider-supplied maps
// cross it. Unknown fields are unavailable rather than truncated raw text.
func clean(r Record) Record {
	r.Kind = oneOf(r.Kind, "unavailable", "tick", "control", "retarget")
	if r.Kind == "retarget" {
		// A retarget saves the active target without observing, deciding, probing
		// or sending. Its note and the configured aliases never enter the journal.
		r = Record{Kind: r.Kind, Sequence: r.Sequence, CheckStartedAt: r.CheckStartedAt, Build: r.Build,
			TargetKey: r.TargetKey, TargetKeyAfter: r.TargetKeyAfter, Action: "none", Cause: "other", Outcome: "target_saved"}
	}
	if r.Kind == "control" {
		// Control updates do not observe an adapter or run a decision. Never attach
		// a stale tick snapshot to a new control save.
		r = Record{Kind: r.Kind, Sequence: r.Sequence, CheckStartedAt: r.CheckStartedAt, Build: r.Build,
			ControlBefore: r.ControlBefore, ControlAfter: r.ControlAfter, ControlChange: r.ControlChange,
			Action: "none", Cause: "other", Outcome: "control_saved"}
		if r.ControlChange != nil {
			c := *r.ControlChange
			c.Operation = oneOf(c.Operation, "unavailable", "enable", "disable", "pause", "resume", "stop", "complete", "await-user", "start", "daemon_takeover", "transfer_begin", "transfer_finish")
			r.ControlChange = &c
		}
	} else {
		r.ControlChange = nil
	}

	if !buildPattern.MatchString(r.Build) {
		r.Build = "unavailable"
	}
	r.Adapter = oneOf(r.Adapter, "unavailable", "bb", "process", "claude", "codex", "cursor")
	if !keyPattern.MatchString(r.TargetKey) {
		r.TargetKey = "unavailable"
	}
	if !keyPattern.MatchString(r.TargetKeyAfter) {
		r.TargetKeyAfter = "unavailable"
	}
	r.Probes = cleanProbes(r.Probes)
	r.Cause = cleanCause(r.Cause)
	r.Action = cleanAction(r.Action)
	r.Decisions = cleanDecisions(r.Decisions)
	r.Outcome = oneOf(r.Outcome, "unavailable", "succeeded", "failed", "dry_run", "control_saved", "target_saved")
	// Whether this check detected a long limit wait and what it did about the one report.
	r.LimitWaitReport = oneOf(r.LimitWaitReport, "none", "requested", "delivery_failed", "not_subscribed")
	// What a re-prompt that retried the failed turn by reference was told.
	r.TurnRetry = oneOf(r.TurnRetry, "none", "retried", "queued", "nothing_to_retry", "not_in_error", "already_queued", "queue_unconfirmed", "refused", "unsupported")
	if r.Kind != "tick" {
		r.LimitWaitReport, r.TurnRetry = "none", "none"
	}
	o := &r.Observation
	o.Availability = oneOf(o.Availability, "not_observed", "observed", "unavailable", "not_observed")
	if o.Availability != "observed" {
		*o = Observation{Availability: o.Availability}
	}
	o.State = oneOf(o.State, "unavailable", "active", "idle", "error", "stopping", "pending", "unknown")
	o.ErrorClass = oneOf(o.ErrorClass, "unavailable", "none", "rate_limit", "auth", "restricted", "refused", "overloaded", "network", "unknown")
	o.Retry = oneOf(o.Retry, "unavailable", "retrying", "not_retrying", "unverified", "expired", "declined")
	o.SourceTime = "unavailable"
	d := evidence.Clean(evidence.Detail{Source: o.Source, Category: o.Category, Code: o.ProviderCode, HTTPStatus: o.HTTPStatus, ExitCode: o.ExitCode})
	o.Source, o.Category, o.ProviderCode, o.HTTPStatus, o.ExitCode = d.Source, d.Category, d.Code, d.HTTPStatus, d.ExitCode
	o.Queued = max(0, o.Queued)
	o.Background = max(0, o.Background)
	o.ActiveChildren = max(0, o.ActiveChildren)
	o.AwaitingChildren = max(0, o.AwaitingChildren)
	o.ErroredChildren = min(max(0, o.ErroredChildren), 200)
	if !r.CountersAvailable {
		r.Nudges = 0
		r.Reprompts = 0
	}
	r.Nudges = max(0, r.Nudges)
	r.Reprompts = max(0, r.Reprompts)
	if r.Kind != "tick" {
		r.Collapsed = nil
	}
	if r.Collapsed != nil {
		// Copy so cleaning a caller-owned record does not alias its summary.
		c := *r.Collapsed
		c.ProbeTargetKeys = append([]string{}, c.ProbeTargetKeys...)
		r.Collapsed = &c
	}
	r.Tier = tierOf(r) // Derived, never trusted from storage.
	return r
}

// Cause converts only known supervisor reasons to a fixed vocabulary.
func Cause(reason string) string {
	switch reason {
	case "disabled", "paused", "completed", "idle", "active":
		return reason
	case "awaiting user":
		return "awaiting_user"
	case "provider is retrying the turn":
		return "retrying"
	case "provider retry state unverified":
		return "retry_unverified"
	case "child work running":
		return "child_busy"
	case decide.ChildErrorWakeReason:
		return "child_error"
	case "child status unverified":
		return "child_unverified"
	case "waiting on a person (pending interaction)":
		return "input"
	case decide.RestrictedReason:
		return "restricted"
	case decide.RefusedReason:
		return "refused"
	case decide.DeclineAcknowledgedReason:
		return "decline_acknowledged"
	}
	for _, c := range []struct{ prefix, code string }{
		{"loop deadline reached;", "deadline"}, {"loop send limit reached;", "send_limit"},
		{"no Git progress within ", "git_limit"}, {"no progress after ", "stall_limit"},
		{"no progress for ", "stall"}, {"children waiting on a person ", "child_input"},
		{"provider retry went silent ", "retry_silent"}, {"handoff deferred:", "handoff_deferred"}, {"network down;", "network_down"}, {"stop requested", "stop"}, {"stop already requested", "stop"},
	} {
		if strings.HasPrefix(reason, c.prefix) {
			return c.code
		}
	}
	return "other"
}

// DecisionCause prefers the engine's typed limit-wait rule, which survives any
// text the supervisor appends to a reason, over matching the reason itself.
func DecisionCause(d decide.Decision) string {
	if d.LimitWait != "" {
		return cleanCause(string(d.LimitWait))
	}
	return Cause(d.Reason)
}

func cleanCause(value string) string {
	return oneOf(value, "unavailable", "disabled", "paused", "awaiting_user", "completed", "retrying", "retry_unverified", "retry_silent", "handoff_deferred", "network_down", "deadline", "send_limit", "git_limit", "stall_limit", "stall", "idle", "active", "child_input", "child_busy", "child_error", "child_unverified", "input", "stop", "limit_wait", "limit_no_fallback", "limit_failover_disabled", "restricted", "refused", "decline_acknowledged", "other")
}
func cleanAction(value string) string {
	return oneOf(value, "unavailable", "none", "wake", "nudge", "reprompt", "await_user", "stop", "handoff", "switch_back")
}

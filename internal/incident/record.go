// Package incident stores bounded, typed diagnostic snapshots. It is never an
// input to supervision decisions or control/allowance accounting.
package incident

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// Record describes a tick or a saved control update. Timestamps describe local operations,
// not provider event time. Unsupported source details are explicitly unavailable.
type Record struct {
	Kind                 string         `json:"kind"`
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
	Gone               bool   `json:"gone"`
}

// Observed copies normalized decision evidence, never raw provider output.
func Observed(o decide.Observation, adapter string, failed bool) Observation {
	if failed {
		return Observation{Availability: "unavailable"}
	}
	d := evidence.Clean(o.Evidence)
	r := Observation{Source: d.Source, Category: d.Category, ProviderCode: d.Code, HTTPStatus: d.HTTPStatus, ExitCode: d.ExitCode, Availability: "observed", State: string(o.State), ErrorClass: string(o.ErrorClass),
		Retry: "unavailable", Queued: o.Queued, Background: o.Background, AwaitingInput: o.AwaitingInput,
		ActiveChildren: o.ActiveChildren, AwaitingChildren: len(o.AwaitingChildren), ChildrenUnverified: o.ChildrenUnverified, Gone: o.Gone}
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
	r.Kind = oneOf(r.Kind, "unavailable", "tick", "control")
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
	r.Adapter = oneOf(r.Adapter, "unavailable", "bb", "process", "claude", "codex")
	if !keyPattern.MatchString(r.TargetKey) {
		r.TargetKey = "unavailable"
	}
	r.Cause = cleanCause(r.Cause)
	r.Action = cleanAction(r.Action)
	r.Decisions = cleanDecisions(r.Decisions)
	r.Outcome = oneOf(r.Outcome, "unavailable", "succeeded", "failed", "dry_run", "control_saved")
	o := &r.Observation
	o.Availability = oneOf(o.Availability, "not_observed", "observed", "unavailable", "not_observed")
	if o.Availability != "observed" {
		*o = Observation{Availability: o.Availability}
	}
	o.State = oneOf(o.State, "unavailable", "active", "idle", "error", "stopping", "pending", "unknown")
	o.ErrorClass = oneOf(o.ErrorClass, "unavailable", "none", "rate_limit", "auth", "overloaded", "network", "unknown")
	o.Retry = oneOf(o.Retry, "unavailable", "retrying", "not_retrying", "unverified")
	o.SourceTime = "unavailable"
	d := evidence.Clean(evidence.Detail{Source: o.Source, Category: o.Category, Code: o.ProviderCode, HTTPStatus: o.HTTPStatus, ExitCode: o.ExitCode})
	o.Source, o.Category, o.ProviderCode, o.HTTPStatus, o.ExitCode = d.Source, d.Category, d.Code, d.HTTPStatus, d.ExitCode
	o.Queued = max(0, o.Queued)
	o.Background = max(0, o.Background)
	o.ActiveChildren = max(0, o.ActiveChildren)
	o.AwaitingChildren = max(0, o.AwaitingChildren)
	if !r.CountersAvailable {
		r.Nudges = 0
		r.Reprompts = 0
	}
	r.Nudges = max(0, r.Nudges)
	r.Reprompts = max(0, r.Reprompts)
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
	case "child status unverified":
		return "child_unverified"
	case "waiting on a person (pending interaction)":
		return "input"
	}
	for _, c := range []struct{ prefix, code string }{
		{"loop deadline reached;", "deadline"}, {"loop send limit reached;", "send_limit"},
		{"no Git progress within ", "git_limit"}, {"no progress after ", "stall_limit"},
		{"no progress for ", "stall"}, {"children waiting on a person ", "child_input"},
		{"stop requested", "stop"}, {"stop already requested", "stop"},
	} {
		if strings.HasPrefix(reason, c.prefix) {
			return c.code
		}
	}
	return "other"
}

func cleanCause(value string) string {
	return oneOf(value, "unavailable", "disabled", "paused", "awaiting_user", "completed", "retrying", "retry_unverified", "deadline", "send_limit", "git_limit", "stall_limit", "stall", "idle", "active", "child_input", "child_busy", "child_unverified", "input", "stop", "other")
}
func cleanAction(value string) string {
	return oneOf(value, "unavailable", "none", "wake", "nudge", "reprompt", "await_user", "stop", "handoff", "switch_back")
}

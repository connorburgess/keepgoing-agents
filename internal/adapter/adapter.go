// Package adapter defines how the supervisor talks to a provider. Every
// adapter returns structured observations only; none of them read
// transcript text.
package adapter

import (
	"time"

	"context"
	"strconv"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Mode says how a prompt should be delivered.
type Mode string

// Delivery modes.
const (
	ModeAuto  Mode = "auto"  // steer a live turn, otherwise start one
	ModeQueue Mode = "queue" // queue behind the live turn
)

// Adapter is one supervised session.
type Adapter interface {
	// Name identifies the adapter kind for logs.
	Name() string
	// Observe returns the session's structured status.
	Observe(ctx context.Context) (decide.Observation, error)
	// Send delivers a prompt to the session.
	Send(ctx context.Context, prompt string, mode Mode) error
	// Stop halts the running session. Adapters that cannot stop return
	// ErrUnsupported.
	Stop(ctx context.Context) error
}

// WakePoller consumes structured notifications that can justify an early idle
// check. It never sends a prompt. The caller holds the tick lock, honors control
// holds, and re-observes the target before using the signal.
type WakePoller interface {
	PollWake(ctx context.Context) bool
}

// Activator is implemented by adapters that must prepare the session before
// the first message after a handoff, for example switching its model.
type Activator interface {
	Activate(ctx context.Context) error
}

// InteractionAdopter is implemented by adapters that report
// decide.Observation.DeferredInteraction from saved evidence. The supervisor
// calls it only after the awaiting-user hold for that evidence is saved, so
// the adapter stops reporting the same interaction. Until then the evidence
// keeps raising the hold, including after a restart. It must not send, approve
// or resume anything.
type InteractionAdopter interface {
	AdoptInteraction(ctx context.Context) error
}

// TurnRetrier is implemented by adapters whose provider can re-run a failed
// turn by reference, without a new prompt. The supervisor uses it for a
// re-prompt decision on an errored session, so recovery does not add another
// copy of the task to the conversation.
type TurnRetrier interface {
	// RetryFailedTurn asks the provider to retry the session's failed turn. It
	// sends no text. ErrUnsupported means this provider version cannot retry
	// a turn, and the supervisor prompts as it would without this interface.
	// Any other error means the provider refused or the result could not be
	// read; the supervisor then sends nothing else this check.
	RetryFailedTurn(ctx context.Context) (TurnRetry, error)
	// StillFailed re-reads the session's status after the provider answered
	// that there is no failed turn. It reports true only when the session is
	// still in error with nothing queued, the one case where a prompt is the
	// right recovery. It reads status only: no log, no children, no send.
	StillFailed(ctx context.Context) (bool, error)
}

// CLIPather is implemented by adapters that run a provider executable: the
// absolute path the system resolved for it on the last command started, or
// "" before one was. A check records it, so a caller with a shorter PATH (the
// panel, the menu) can find the same executable for a stop.
type CLIPather interface {
	CLIPath() string
}

// LaneStopper is implemented by adapters whose conversations have lanes:
// child conversations the supervised one started, and their own children.
// It is used only for a stop a person asked to cover the lanes
// (keepgoing stop --lanes), and it can do nothing to a lane but read its
// status and stop it.
type LaneStopper interface {
	// LaneRoot is the supervised conversation the lanes hang from.
	LaneRoot() string
	// LaneChildren lists the direct children of parent and re-reads each on
	// its own, at most max of them. notChecked counts those listed but not
	// read because of max. An error means the listing itself failed.
	LaneChildren(ctx context.Context, parent string, max int) (kids []LaneInfo, notChecked int, err error)
	// StopLane stops one lane's work. It sends nothing else.
	StopLane(ctx context.Context, id string) error
	// CancelLaneRetry cancels a provider retry waiting in the lane's queue.
	// cancelled is false when there was none to cancel. ErrUnsupported means
	// the provider has no structured way to do it.
	CancelLaneRetry(ctx context.Context, id string) (cancelled bool, err error)
	// DeleteLaneQueue deletes the messages queued in a lane that has not
	// started, or is idle, so that it does not start. deleted counts them.
	// It reads no message: only each queued row's ID and thread.
	DeleteLaneQueue(ctx context.Context, id string) (deleted int, err error)
}

// LaneInfo is one listed child, as re-read on its own.
type LaneInfo struct {
	ID string
	// Verified: the thread names itself and the expected parent and is
	// neither archived nor deleted.
	Verified bool
	Status   string
	Queued   int
}

// LaneStopError is a lane stop the provider refused or that did not finish,
// with a short class from a structured source: an error code, "timeout" for
// a command stopped at its time limit, or "failed".
type LaneStopError struct {
	Class string
	Err   error
}

func (e *LaneStopError) Error() string { return "lane stop " + e.Class + ": " + e.Err.Error() }
func (e *LaneStopError) Unwrap() error { return e.Err }

// TurnRetryOutcome is what the provider reported for a retry request.
type TurnRetryOutcome string

// Retry outcomes. Only TurnRetryNothing permits a re-prompt instead.
const (
	TurnRetried            TurnRetryOutcome = "retried"          // the failed turn was dispatched again
	TurnRetryQueued        TurnRetryOutcome = "queued"           // accepted, waiting behind a busy thread or a schedule
	TurnRetryNothing       TurnRetryOutcome = "nothing_to_retry" // the session has no failed turn
	TurnRetryAlreadyQueued TurnRetryOutcome = "already_queued"   // a retry of that turn is already waiting
)

// TurnRetry is a structured retry result. Attempt is the provider's attempt
// number for the turn (2 is the first retry), or 0 when it did not say.
type TurnRetry struct {
	Outcome TurnRetryOutcome
	Attempt int
}

// ErrUnsupported marks an operation the adapter cannot perform.
type ErrUnsupported struct{ Op string }

func (e ErrUnsupported) Error() string { return e.Op + " is not supported by this adapter" }

// ClassifyCode maps a structured error code (a string token or an HTTP or
// exit status) to an error class. It is deliberately strict: only well-known
// codes classify, anything else is unknown. Callers must pass codes, not
// messages.
func ClassifyCode(code string) decide.ErrorClass {
	c := strings.ToLower(strings.TrimSpace(code))
	if c == "" {
		return decide.ErrUnknown
	}
	if n, err := strconv.Atoi(c); err == nil {
		switch n {
		case 429:
			return decide.ErrRateLimit
		case 401, 403:
			return decide.ErrAuth
		case 503, 529:
			return decide.ErrOverloaded
		}
		return decide.ErrUnknown
	}
	switch c {
	case "rate_limit", "rate-limit", "rate_limited", "ratelimit", "rate_limit_event", "rate_limit_error", "usage_limit", "usage_limit_reached", "quota_exceeded", "insufficient_quota", "too_many_requests", "subscription_window_blocked":
		return decide.ErrRateLimit
	case "auth", "unauthorized", "unauthenticated", "authentication", "authentication_error", "authentication_failed", "permission_denied", "forbidden", "invalid_api_key", "oauth_token_expired":
		return decide.ErrAuth
	case "overloaded", "overloaded_error", "service_unavailable", "capacity", "server_overloaded":
		return decide.ErrOverloaded
	case "network", "econnreset", "econnrefused", "etimedout", "enotfound", "timeout", "connection_error", "stream-disconnected", "stream_disconnected", "responsestreamdisconnected":
		return decide.ErrNetwork
	}
	return decide.ErrUnknown
}

// ParseState maps a provider's status string onto the engine's states.
func ParseState(s string) decide.State {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "active", "running", "busy", "working":
		return decide.Active
	case "idle", "completed", "done", "waiting":
		return decide.Idle
	case "error", "failed", "errored":
		return decide.Error
	case "stopping", "cancelling", "canceling", "interrupting":
		return decide.Stopping
	case "pending", "queued", "starting":
		return decide.Pending
	}
	return decide.Unknown
}

// CheckTimer is implemented by adapters that compare provider times (event
// or reset times) against the check time. The supervisor passes its own
// check time so the observation and the decision share one clock.
type CheckTimer interface {
	CheckAt(time.Time)
}

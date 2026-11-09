// Package adapter defines how the supervisor talks to a provider. Every
// adapter returns structured observations only; none of them read
// transcript text.
package adapter

import (
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
	case "rate_limit", "rate_limited", "ratelimit", "usage_limit", "quota_exceeded", "insufficient_quota", "too_many_requests":
		return decide.ErrRateLimit
	case "auth", "unauthorized", "unauthenticated", "authentication_error", "permission_denied", "forbidden", "invalid_api_key":
		return decide.ErrAuth
	case "overloaded", "overloaded_error", "service_unavailable", "capacity":
		return decide.ErrOverloaded
	case "network", "econnreset", "econnrefused", "etimedout", "enotfound", "timeout", "connection_error":
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

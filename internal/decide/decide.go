// Package decide is the pure decision engine. It takes what the adapter
// observed plus the control and progress state and returns one action.
// It performs no I/O so every rule can be table-tested.
package decide

import "time"

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
	State      State
	ErrorClass ErrorClass
	Queued     int  // messages already waiting for the session
	Background int  // background workers still running
	Gone       bool // target deleted or archived
}

// Control is the operator's gate.
type Control struct {
	Enabled      bool
	Paused       bool
	Stop         bool
	AwaitingUser bool
}

// Input is everything Decide needs.
type Input struct {
	Obs      Observation
	Control  Control
	InWindow bool

	UnchangedFor    time.Duration // since the progress fingerprint last changed
	SinceWake       time.Duration // since we last sent anything
	StallAfter      time.Duration
	MinWakeInterval time.Duration

	Reprompts         int // re-prompts sent since the session last ran cleanly
	MaxReprompts      int
	SinceLastReprompt time.Duration
	Backoff           time.Duration
}

// Action is what the command layer should do.
type Action string

// Actions.
const (
	None      Action = "none"       // do nothing this tick
	Wake      Action = "wake"       // start a turn on an idle session
	Nudge     Action = "nudge"      // queue a progress check behind a live turn
	Reprompt  Action = "reprompt"   // start a turn after an error, counted and bounded
	AwaitUser Action = "await_user" // give up until the operator intervenes
	Stop      Action = "stop"       // stop the running session now
)

// Decision is an action and the rule that produced it.
type Decision struct {
	Action Action
	Reason string
}

func none(reason string) Decision { return Decision{None, reason} }

// Decide applies the rules in priority order. Stop and pause always win.
func Decide(in Input) Decision {
	c := in.Control
	o := in.Obs
	if c.Stop {
		if o.State == Active || o.State == Pending {
			return Decision{Stop, "stop requested"}
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
	if o.Gone {
		return none("target gone")
	}
	if !in.InWindow {
		return none("outside window")
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
		return Decision{Wake, "idle"}
	case Active:
		if in.UnchangedFor >= in.StallAfter && in.SinceWake >= in.MinWakeInterval {
			return Decision{Nudge, "no progress for " + in.UnchangedFor.Truncate(time.Minute).String()}
		}
		return none("active")
	}
	return none("unrecognised state")
}

// decideError bounds recovery from an errored session. Nothing here fails
// over; that is a separate, confirmed-fallback step. Auth errors cannot be
// fixed by retrying, so they go straight to the operator.
func decideError(in Input) Decision {
	if in.Obs.ErrorClass == ErrAuth {
		return Decision{AwaitUser, "auth error"}
	}
	if in.Obs.Queued > 0 {
		return none("messages already queued after error")
	}
	if in.Reprompts >= in.MaxReprompts {
		return Decision{AwaitUser, "error persisted after re-prompts"}
	}
	if in.Reprompts > 0 && in.SinceLastReprompt < in.Backoff {
		return none("backing off after error")
	}
	return Decision{Reprompt, "error: " + string(classOrUnknown(in.Obs.ErrorClass))}
}

func classOrUnknown(c ErrorClass) ErrorClass {
	if c == "" || c == ErrNone {
		return ErrUnknown
	}
	return c
}

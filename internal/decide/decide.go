// Package decide is the pure decision engine. It takes what the adapter
// observed plus the control and progress state and returns one action.
// It performs no I/O so every rule can be table-tested.
package decide

import (
	"strings"
	"time"
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
	State      State
	ErrorClass ErrorClass
	Queued     int  // messages already waiting for the session
	Background int  // background workers still running
	Gone       bool // target deleted or archived
	// Retrying means the provider itself has scheduled a retry of the
	// failed turn, so the supervisor should leave it alone.
	Retrying bool
	// RetryUnverified means the adapter could not refresh retry/recovery evidence.
	// It suppresses automatic prompts and moves without hiding an active stop target.
	RetryUnverified bool
	// ResetsAt, when known, is when the current usage limit lifts.
	ResetsAt time.Time
	// AwaitingInput means the session is blocked on a person: a pending
	// permission prompt, question, or form, reported by the provider.
	AwaitingInput bool
	// ActiveChildren is verified direct child work. ChildrenUnverified means
	// discovery could not establish the complete picture. Neither permits a
	// parent prompt or target move; control holds and stop retain precedence.
	ActiveChildren     int
	ChildrenUnverified bool
	// AwaitingChildren contains verified child IDs with pending human input.
	AwaitingChildren []string
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

	UnchangedFor    time.Duration // since the progress fingerprint last changed
	SinceWake       time.Duration // since we last sent anything
	StallAfter      time.Duration
	MinWakeInterval time.Duration

	Failover Failover

	// SwitchBackReady: the loop is on a fallback, switch-back is enabled, and
	// the primary's limit has had time to reset. The tick still confirms the
	// primary with an exact probe before moving.
	SwitchBackReady bool

	Nudges    int // nudges since progress or the most recent turn end
	MaxNudges int

	Reprompts         int // re-prompts sent since the session last ran cleanly
	MaxReprompts      int
	SinceLastReprompt time.Duration
	Backoff           time.Duration
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
	if c.Completed {
		return none("completed")
	}
	if in.DeadlineReached {
		return Decision{AwaitUser, "loop deadline reached; revise loop.limits.deadline before resuming"}
	}
	if in.SendLimitReached {
		return Decision{AwaitUser, "loop send limit reached; increase loop.limits.max_sends before resuming"}
	}
	if in.NoProgressReached {
		return Decision{AwaitUser, "no Git progress within loop.limits.no_progress_timeout; inspect work and progress tracking before resuming"}
	}
	if o.Gone {
		return Decision{AwaitUser, "target gone (archived, deleted, or not found)"}
	}
	if len(o.AwaitingChildren) > 0 {
		return Decision{AwaitUser, "children waiting on a person (pending interaction): " + strings.Join(o.AwaitingChildren, ", ")}
	}
	if o.AwaitingInput {
		return none("waiting on a person (pending interaction)")
	}
	if o.ActiveChildren > 0 {
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
	if o.Retrying {
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
		// Only between turns: never pull the loop from a fallback mid-turn.
		if in.SwitchBackReady {
			return Decision{SwitchBack, "primary's limit has reset"}
		}
		return Decision{Wake, "idle"}
	case Active:
		if in.UnchangedFor >= in.StallAfter && in.SinceWake >= in.MinWakeInterval {
			if in.MaxNudges > 0 && in.Nudges >= in.MaxNudges {
				return Decision{AwaitUser, "no progress after " + itoa(in.Nudges) + " nudges"}
			}
			return Decision{Nudge, "no progress for " + in.UnchangedFor.Truncate(time.Minute).String()}
		}
		return none("active")
	}
	return none("unrecognised state")
}

// decideError bounds recovery from an errored session. A handoff happens
// only for an error class the loop is configured to fail over on, and only
// when a confirmed fallback is available. Unknown errors never hand off.
func decideError(in Input) Decision {
	o, f := in.Obs, in.Failover
	if o.Queued > 0 {
		return none("messages already queued after error")
	}
	if f.Eligible && o.ErrorClass != ErrUnknown && o.ErrorClass != ErrNone && o.ErrorClass != "" {
		if f.ResetIn > 0 && f.ResetIn <= f.WaitIfResetWithin {
			return none("limit resets in " + f.ResetIn.Round(time.Minute).String() + "; waiting")
		}
		if f.Available {
			return Decision{Handoff, "error: " + string(o.ErrorClass)}
		}
		if f.ResetIn > 0 {
			return none("limit resets in " + f.ResetIn.Round(time.Minute).String() + "; no confirmed fallback")
		}
	}
	// Retrying cannot fix an auth error.
	if o.ErrorClass == ErrAuth {
		return Decision{AwaitUser, "auth error"}
	}
	if in.Reprompts >= in.MaxReprompts {
		return Decision{AwaitUser, "error persisted after re-prompts"}
	}
	if in.Reprompts > 0 && in.SinceLastReprompt < in.Backoff {
		return none("backing off after error")
	}
	return Decision{Reprompt, "error: " + string(classOrUnknown(o.ErrorClass))}
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

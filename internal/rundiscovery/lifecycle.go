// Package rundiscovery defines an internal, pure run-identity contract: which
// identities exist, what opens or closes an execution run, and which recorded
// relationships a run may have. It does not persist records, control
// supervision, discover providers or grant permission to send. Callers must
// validate authorization and serialize/persist transitions before using their
// results. No production caller uses it yet.
package rundiscovery

import (
	"errors"
	"regexp"
	"time"
)

var ErrInvalid = errors.New("invalid run discovery transition")

var (
	loopID    = regexp.MustCompile(`^supervision_[0-9a-f]{32}$`)
	runID     = regexp.MustCompile(`^run_[0-9a-f]{32}$`)
	attemptID = regexp.MustCompile(`^attempt_[0-9a-f]{32}$`)
	goalID    = regexp.MustCompile(`^goal_[0-9a-f]{32}$`)
)

// State is lifecycle metadata, not controls or evidence of live activity.
// Its zero value is unenrolled. Values contain no pointers or mutable collections.
type State struct {
	LoopID  string
	Current Episode
	// Completed also represents completion declared before any episode exists.
	Completed bool
}

// Episode is a supervision episode, not a daemon lifetime or provider turn.
// RecordedAt is when identity was recorded. It is not a historical start time
// for Adopted episodes. ClosedAt means declared completion, not verified success.
type Episode struct {
	ID         string
	Origin     Origin
	RecordedAt time.Time
	ClosedAt   time.Time
	Reopens    string
	// Routine is the externally supplied invocation carried by the operation
	// that opened this episode, if any. It is a reference, never this run's ID.
	Routine RoutineRef
}

type Origin string

const (
	Explicit Origin = "explicit"
	Adopted  Origin = "adopted_existing"
	Reopened Origin = "reopened"
)

func (s State) Valid() bool {
	if s.LoopID == "" {
		return s.Current == (Episode{}) && !s.Completed
	}
	if !loopID.MatchString(s.LoopID) {
		return false
	}
	e := s.Current
	if e.ID == "" {
		return e == (Episode{})
	}
	if !runID.MatchString(e.ID) || e.RecordedAt.IsZero() {
		return false
	}
	if s.Completed != !e.ClosedAt.IsZero() || !e.Routine.validOrZero() {
		return false
	}
	switch e.Origin {
	case Explicit:
		return e.Reopens == ""
	case Adopted:
		return e.Reopens == "" && e.Routine == (RoutineRef{})
	case Reopened:
		return e.Reopens == "" || (runID.MatchString(e.Reopens) && e.Reopens != e.ID)
	default:
		return false
	}
}

type Kind string

const (
	Enroll             Kind = "enroll"
	Enable             Kind = "enable"
	Start              Kind = "start"
	Adopt              Kind = "adopt"
	Complete           Kind = "complete"
	Resume             Kind = "resume"
	Pause              Kind = "pause"
	Stop               Kind = "stop"
	Disable            Kind = "disable"
	AwaitUser          Kind = "await_user"
	Check              Kind = "check"
	Retry              Kind = "retry"
	Restart            Kind = "restart"
	GoalRevision       Kind = "goal_revision"
	Transfer           Kind = "transfer"
	ExternalInvocation Kind = "external_invocation"
)

// Event describes an already-authorized operation. NewID is supplied only for
// enrollment or a potentially opening operation; the reducer never generates it.
// AdoptionEligible means the caller verified a legacy loop is enabled and has no
// hold, pending transfer or other prerequisite preventing adoption. DryRun validates the prospective
// transition and keeps state unchanged; neither mode authorizes the operation.
// Routine is required for ExternalInvocation and optional for Enable, Start and
// Resume. It is recorded only when that operation opens an episode; a trigger
// never opens, reopens or replaces one by itself.
type Event struct {
	Kind             Kind
	At               time.Time
	NewID            string
	Routine          RoutineRef
	AdoptionEligible bool
	DryRun           bool
}

// Apply returns the original state on rejection. Duplicate opening operations
// keep the original ID/time; unused candidate IDs are not records. The caller
// must guarantee IDs have never been used in retained history: this bounded
// value checks only the current episode, not global uniqueness or provenance.
// Clock order does not establish identity; backwards timestamps are permitted.
func Apply(s State, e Event) (State, error) {
	if !s.Valid() || e.At.IsZero() || (e.AdoptionEligible && e.Kind != Adopt) {
		return s, ErrInvalid
	}
	switch e.Kind {
	case Enable, Start, Resume:
		if !e.Routine.validOrZero() {
			return s, ErrInvalid
		}
	case ExternalInvocation:
		if !e.Routine.valid() {
			return s, ErrInvalid
		}
	default:
		if e.Routine != (RoutineRef{}) {
			return s, ErrInvalid
		}
	}
	switch e.Kind {
	case Enroll:
		if !loopID.MatchString(e.NewID) {
			return s, ErrInvalid
		}
		if s.LoopID != "" && s.LoopID != e.NewID {
			return s, ErrInvalid
		}
	case Enable, Start, Resume, Adopt:
		if e.NewID != "" && !runID.MatchString(e.NewID) {
			return s, ErrInvalid
		}
	case Complete, Pause, Stop, Disable, AwaitUser, Check, Retry, Restart, GoalRevision, Transfer, ExternalInvocation:
		if e.NewID != "" {
			return s, ErrInvalid
		}
	default:
		return s, ErrInvalid
	}
	if e.DryRun {
		e.DryRun = false
		_, err := Apply(s, e)
		return s, err
	}
	if e.Kind == Enroll {
		s.LoopID = e.NewID
		return s, nil
	}
	if s.LoopID == "" {
		return s, ErrInvalid
	}
	out := s
	switch e.Kind {
	case Complete:
		out.Completed = true
		if out.Current.ID != "" && out.Current.ClosedAt.IsZero() {
			out.Current.ClosedAt = e.At
		}
	case Resume:
		if s.Completed {
			if !runID.MatchString(e.NewID) || e.NewID == s.Current.ID {
				return s, ErrInvalid
			}
			out.Completed = false
			out.Current = Episode{ID: e.NewID, Origin: Reopened, RecordedAt: e.At, Reopens: s.Current.ID, Routine: e.Routine}
		}
	case Enable, Start, Adopt:
		if e.Kind == Adopt && !e.AdoptionEligible {
			return s, ErrInvalid
		}
		if !s.Completed && s.Current.ID == "" {
			if !runID.MatchString(e.NewID) {
				return s, ErrInvalid
			}
			origin := Explicit
			if e.Kind == Adopt {
				origin = Adopted
			}
			out.Current = Episode{ID: e.NewID, Origin: origin, RecordedAt: e.At, Routine: e.Routine}
		}
	}
	return out, nil
}

// GoalRef describes the exact goal revision shared by an attempt, if any.
// A configured goal that was not shared is not attempt provenance.
type GoalRef struct {
	ID       string
	Revision uint64
}

func (g GoalRef) valid() bool {
	return (g.ID == "" && g.Revision == 0) || (goalID.MatchString(g.ID) && g.Revision > 0 && g.Revision <= 128)
}

type Outcome string

const (
	Intent          Outcome = "intent"
	AdapterReturned Outcome = "adapter_returned"
	AdapterError    Outcome = "adapter_error"
)

// Attempt is one send intent, possibly a nudge of an existing turn. It contains
// no process or cancellation authority. Even AdapterReturned is not delivery.
type Attempt struct {
	ID         string
	RunID      string
	Action     string
	Mode       string
	Goal       GoalRef
	RecordedAt time.Time
	Outcome    Outcome
	ReturnedAt time.Time
}

func (a Attempt) Valid() bool {
	if !attemptID.MatchString(a.ID) || !runID.MatchString(a.RunID) || a.RecordedAt.IsZero() || !a.Goal.valid() {
		return false
	}
	switch a.Action {
	case "nudge":
		if a.Mode != "queue" {
			return false
		}
	case "wake", "reprompt", "handoff", "switch_back":
		if a.Mode != "auto" {
			return false
		}
	default:
		return false
	}
	if a.Outcome == Intent {
		return a.ReturnedAt.IsZero()
	}
	return (a.Outcome == AdapterReturned || a.Outcome == AdapterError) && !a.ReturnedAt.IsZero()
}

// BeginAttempt validates a supplied intent against the expected open run and
// the attempts the caller retains. Every send, including a retry of an intent
// that never returned and the first send after a daemon restart, is a new
// attempt with its own ID in the same run; reusing a retained ID is refused.
// Only the supplied attempts are checked: uniqueness against discarded history
// is the caller's responsibility. It does not inspect holds, perform IO or
// authorize Send. Callers must check current controls, bind the target
// separately and persist intent before calling an adapter; a stale closed-run
// intent is refused.
func BeginAttempt(s State, a Attempt, retained ...Attempt) (Attempt, error) {
	if !s.Valid() || s.LoopID == "" || s.Completed || s.Current.ID == "" || a.RunID != s.Current.ID || !a.Valid() || a.Outcome != Intent {
		return Attempt{}, ErrInvalid
	}
	for _, prior := range retained {
		if prior.ID == a.ID {
			return Attempt{}, ErrInvalid
		}
	}
	return a, nil
}

// FinishAttempt records a returned call, including for a run since completed.
// Missing return evidence remains Intent; it never authorizes replay. Repeating
// the same result preserves its first timestamp; conflicting results reject.
func FinishAttempt(a Attempt, outcome Outcome, at time.Time) (Attempt, error) {
	if !a.Valid() || at.IsZero() || (outcome != AdapterReturned && outcome != AdapterError) {
		return a, ErrInvalid
	}
	if a.Outcome != Intent {
		if a.Outcome != outcome {
			return a, ErrInvalid
		}
		return a, nil
	}
	a.Outcome, a.ReturnedAt = outcome, at
	return a, nil
}

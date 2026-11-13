package rundiscovery

import (
	"regexp"
	"strings"
	"time"
)

var (
	registrationID = regexp.MustCompile(`^loop_[0-9a-f]{32}$`)
	// token bounds externally supplied identifiers. Anything else is treated as
	// unavailable by the caller, never repaired or truncated here.
	token = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$`)
)

// RoutineRef identifies an optional external routine and one of its
// invocations, as supplied by a structured integration. Source names that
// integration. Invocation may be empty when the integration supplies none;
// such triggers cannot be told apart. It is never derived from a timestamp,
// and an invocation ID is not a keepgoing run ID.
type RoutineRef struct {
	Source     string
	Routine    string
	Invocation string
}

func (r RoutineRef) valid() bool {
	return token.MatchString(r.Source) && token.MatchString(r.Routine) && (r.Invocation == "" || token.MatchString(r.Invocation))
}

func (r RoutineRef) validOrZero() bool { return r == (RoutineRef{}) || r.valid() }

// SessionRef identifies a provider chat or session by its recorded opaque ID.
// It has no parent, owner or URL: it does not say the conversation exists, can
// be resumed or opened, or belongs to any other thread.
type SessionRef struct {
	Provider string
	ID       string
}

func (r SessionRef) valid() bool { return token.MatchString(r.Provider) && token.MatchString(r.ID) }

type LinkKind string

const (
	// SessionLink: the run used or was given this provider chat/session.
	SessionLink LinkKind = "session"
	// GoalLink: this goal revision was in effect for the run.
	GoalLink LinkKind = "goal"
	// InvocationLink: this routine trigger was observed while the run was open.
	InvocationLink LinkKind = "invocation"
)

// Link is one recorded relationship from a run to another identity. Exactly
// the field named by Kind is set. Links are append-only history: none claims
// to be the current target, and none connects a session to another session or
// a run to a parent run. AttemptID optionally names the attempt whose send
// targeted or returned a session.
type Link struct {
	RunID      string
	Kind       LinkKind
	Session    SessionRef
	Goal       GoalRef
	Routine    RoutineRef
	AttemptID  string
	RecordedAt time.Time
}

func (l Link) Valid() bool {
	if !runID.MatchString(l.RunID) || l.RecordedAt.IsZero() {
		return false
	}
	var want Link
	want.RunID, want.Kind, want.RecordedAt = l.RunID, l.Kind, l.RecordedAt
	switch l.Kind {
	case SessionLink:
		if !l.Session.valid() || (l.AttemptID != "" && !attemptID.MatchString(l.AttemptID)) {
			return false
		}
		want.Session, want.AttemptID = l.Session, l.AttemptID
	case GoalLink:
		if l.Goal.ID == "" || !l.Goal.valid() {
			return false
		}
		want.Goal = l.Goal
	case InvocationLink:
		if !l.Routine.valid() {
			return false
		}
		want.Routine = l.Routine
	default:
		return false
	}
	return l == want
}

// Relate validates a relationship for the loop's current episode. Session and
// goal links may also be recorded for a current episode that has since been
// declared complete, as late evidence that never reopens it. An invocation
// link needs an open episode: a trigger that arrives with no episode or after
// completion is related to no run. A link naming any other run is refused,
// including a late receipt for a superseded run, which needs retained history
// this value does not have. Relate never changes lifecycle state, validates
// provider existence or binds a target.
func Relate(s State, l Link) (Link, error) {
	if !s.Valid() || s.LoopID == "" || s.Current.ID == "" || l.RunID != s.Current.ID || !l.Valid() {
		return Link{}, ErrInvalid
	}
	if l.Kind == InvocationLink && s.Completed {
		return Link{}, ErrInvalid
	}
	return l, nil
}

// Sessions lists the distinct provider chats/sessions recorded for a run, in
// first-recorded slice order. One run may list several, and the same session
// may be listed for several runs; neither implies thread parentage. Invalid
// links are skipped. This is a projection of supplied records, not a lookup.
func Sessions(run string, links []Link) []SessionRef {
	var out []SessionRef
	seen := map[SessionRef]bool{}
	for _, l := range links {
		if l.Kind != SessionLink || l.RunID != run || !l.Valid() || seen[l.Session] {
			continue
		}
		seen[l.Session] = true
		out = append(out, l.Session)
	}
	return out
}

// Registration records that an existing registry entry currently refers to a
// durable loop identity. The registry ID is a removable reference to a
// config/runtime binding; re-registering creates a new one. Nothing here maps
// a new registration to earlier run history: that takes an explicit Register
// call by a caller that has verified the binding.
type Registration struct {
	LoopID         string
	RegistrationID string
	RecordedAt     time.Time
}

// Register validates an explicit registration reference for an enrolled loop.
func Register(s State, registration string, at time.Time) (Registration, error) {
	if !s.Valid() || s.LoopID == "" || !registrationID.MatchString(registration) || at.IsZero() {
		return Registration{}, ErrInvalid
	}
	return Registration{LoopID: s.LoopID, RegistrationID: registration, RecordedAt: at}, nil
}

// ParseRoutine reads "source/routine" or "source/routine/invocation". Tokens
// cannot contain "/", so the form is unambiguous. Anything else is refused,
// not repaired.
func ParseRoutine(s string) (RoutineRef, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return RoutineRef{}, ErrInvalid
	}
	r := RoutineRef{Source: parts[0], Routine: parts[1]}
	if len(parts) == 3 {
		if parts[2] == "" {
			return RoutineRef{}, ErrInvalid
		}
		r.Invocation = parts[2]
	}
	if !r.valid() {
		return RoutineRef{}, ErrInvalid
	}
	return r, nil
}

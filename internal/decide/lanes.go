package decide

import (
	"sort"
	"time"
)

// A lane is a direct child conversation of the supervised session. The rules
// here decide the one thing the supervisor may ever do to a lane, when the
// loop has opted in: ask the provider to retry its failed turn by reference.
// They never authorize a message, a stop or a creation.

// LaneFacts is what one fresh inspection of a lane established. Every field
// is false or zero when it could not be established.
type LaneFacts struct {
	// Verified: the lane is still a direct child of the supervised session,
	// by its own status record, and is neither archived nor deleted.
	Verified bool
	// Errored: its status is error.
	Errored bool
	// Queued is how many messages it has queued, including a retry the
	// provider has scheduled itself.
	Queued int
	// InputKnown: its pending interactions were read. WaitingOnPerson: one is pending.
	InputKnown      bool
	WaitingOnPerson bool
	// ClassKnown: its failure was read from structured error events.
	ClassKnown bool
	Class      ErrorClass
	// Retrying: the provider still announces a retry of its own that nothing
	// shows to be dead.
	Retrying bool
}

// LaneVerdict is what the facts allow.
type LaneVerdict string

// Lane verdicts. Only LaneRetry permits the retry.
const (
	LaneRetry         LaneVerdict = "retry"
	LaneUnverified    LaneVerdict = "unverified"        // not confirmed as a direct child
	LaneNotInError    LaneVerdict = "not_in_error"      // recovered or working again
	LaneQueued        LaneVerdict = "queued"            // something is already waiting to run
	LaneWaitingPerson LaneVerdict = "waiting_on_person" // a prompt or question is pending, or could not be ruled out
	LaneRateLimited   LaneVerdict = "rate_limit"        // a retry runs into the same limit
	LaneAuth          LaneVerdict = "auth"              // needs a person
	LaneRetrying      LaneVerdict = "provider_retrying" // the provider is retrying it
	LaneUnknown       LaneVerdict = "unknown"           // the failure could not be read
)

// LaneVerdictFor is conservative: anything not established withholds the
// retry. A lane is retried only for the classes a retry can plausibly fix,
// the same three as for the supervised session itself.
func LaneVerdictFor(f LaneFacts) LaneVerdict {
	switch {
	case !f.Verified:
		return LaneUnverified
	case !f.Errored:
		return LaneNotInError
	case f.Queued > 0:
		return LaneQueued
	case !f.InputKnown || f.WaitingOnPerson:
		return LaneWaitingPerson
	case !f.ClassKnown:
		return LaneUnknown
	case f.Class == ErrRateLimit:
		return LaneRateLimited
	case f.Class == ErrAuth:
		return LaneAuth
	case f.Retrying:
		return LaneRetrying
	}
	switch f.Class {
	case ErrNetwork, ErrOverloaded, ErrUnknown, ErrNone, "":
		return LaneRetry
	}
	return LaneUnknown
}

// Lane is what the supervisor remembers about one errored lane, under a hash
// of its ID.
type Lane struct {
	Hash string
	// SeenWorking: an earlier check of this loop saw the lane active. A lane
	// that was already in error when first seen is never retried, so opting
	// in cannot restart work the session had abandoned.
	SeenWorking bool
	Attempts    int
	LastAt      time.Time
	// LookedAt: when it was last inspected and left alone. Inspections are
	// spaced like retries, so a lane that cannot be retried (rate limited,
	// waiting on a person) is not read again on every check.
	LookedAt time.Time
	GivenUp  bool
	// Deferred: a previous check ran out of time before reaching it.
	Deferred bool
}

// LaneLimits bound lane work.
type LaneLimits struct {
	MaxPerCheck int           // lanes one check may work on
	MaxAttempts int           // retries per lane
	Backoff     time.Duration // spacing between retries of one lane
	EpisodeLeft int           // retries the episode still allows
}

// LanePlan is one check's lane work.
type LanePlan struct {
	// Try lists the lanes to inspect and, if the facts allow, retry, in order:
	// lanes a previous check put off first, then by hash.
	Try []string
	// GiveUp lists lanes that will not be retried again and have not been
	// given up before: every attempt is used and the spacing after the last
	// has passed, or the episode has no retries left.
	GiveUp []string
	// Waiting counts lanes that are eligible but not tried on this check:
	// inside their spacing, beyond the per-check limit, or beyond the episode's.
	Waiting int
}

// PlanLanes chooses this check's lanes. It never returns a lane that was not
// seen working, or one already given up.
func PlanLanes(lanes []Lane, now time.Time, lim LaneLimits) LanePlan {
	sorted := append([]Lane{}, lanes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Deferred != sorted[j].Deferred {
			return sorted[i].Deferred
		}
		return sorted[i].Hash < sorted[j].Hash
	})
	var p LanePlan
	for _, l := range sorted {
		if !l.SeenWorking || l.GivenUp {
			continue
		}
		spaced := l.Attempts == 0 || now.Sub(l.LastAt) >= lim.Backoff
		looked := !l.LookedAt.IsZero() && now.Sub(l.LookedAt) < lim.Backoff
		switch {
		case l.Attempts >= lim.MaxAttempts:
			// The last retry has had its spacing to take effect.
			if spaced {
				p.GiveUp = append(p.GiveUp, l.Hash)
			}
		case !spaced || looked:
			p.Waiting++
		case lim.EpisodeLeft <= 0:
			// The episode's retries are spent, so this lane will not be
			// retried again. It is given up now, and so reported, instead of
			// staying in error unmentioned.
			p.GiveUp = append(p.GiveUp, l.Hash)
		case len(p.Try) >= lim.MaxPerCheck || len(p.Try) >= lim.EpisodeLeft:
			p.Waiting++
		default:
			p.Try = append(p.Try, l.Hash)
		}
	}
	return p
}

// LanesAllowed reports whether a check that reached this decision may act on
// lanes at all. A check that raises a hold, stops the session or moves the
// loop to another conversation leaves every lane alone, and so does a check
// outside the loop's window or one that finds the session itself waiting on a
// person, whose pending question may be about those lanes.
func LanesAllowed(a Action, inWindow, sessionWaitingOnPerson bool) bool {
	if !inWindow || sessionWaitingOnPerson {
		return false
	}
	switch a {
	case None, Wake, Nudge, Reprompt:
		return true
	}
	return false
}

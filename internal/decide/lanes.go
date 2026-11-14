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
	LaneDeclined      LaneVerdict = "declined"          // the provider restricted it: needs a person, never retried
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
	case f.Class == ErrRestricted:
		return LaneDeclined
	case f.Retrying:
		return LaneRetrying
	}
	switch f.Class {
	// A refused lane is retried like these, within the same per-lane and
	// per-day caps: a refusal is often a false flag that another attempt
	// clears. It never moves to another route.
	case ErrNetwork, ErrOverloaded, ErrUnknown, ErrNone, "", ErrRefused:
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
	// NoOps counts consecutive retries the provider answered without doing
	// anything: no failed turn, or a retry already queued.
	NoOps int
	// DayRetries counts this lane's retries in its current 24-hour window,
	// across recoveries, so a lane that keeps failing an hour after each
	// retry is bounded too.
	DayRetries int
	// Deferred: a previous check ran out of time before reaching it.
	Deferred bool
}

// LaneLimits bound lane work.
type LaneLimits struct {
	MaxPerCheck int           // lanes one check may work on
	MaxAttempts int           // retries per lane
	Backoff     time.Duration // spacing between retries of one lane
	EpisodeLeft int           // retries the episode still allows
	MaxNoOps    int           // consecutive no-op answers before a lane is given up
	MaxPerDay   int           // retries per lane in 24 hours, across recoveries
}

// LanePlan is one check's lane work.
type LanePlan struct {
	// Try lists the lanes to inspect and, if the facts allow, retry, in order:
	// lanes a previous check put off first, then by hash.
	Try []string
	// GiveUp lists lanes that will not be retried again and have not been
	// given up before: every attempt (of this failure, or of the lane's day)
	// is used and the spacing after the last has passed, the provider keeps
	// answering that it will do nothing, or the episode has no retries left.
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
		case lim.MaxNoOps > 0 && l.NoOps >= lim.MaxNoOps:
			// The provider keeps answering that there is nothing it will do.
			p.GiveUp = append(p.GiveUp, l.Hash)
		case l.Attempts >= lim.MaxAttempts || (lim.MaxPerDay > 0 && l.DayRetries >= lim.MaxPerDay):
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
// outside the loop's window, one that finds the session itself waiting on a
// person, whose pending question may be about those lanes, and one that finds
// the session stopping or in a state it could not read.
func LanesAllowed(a Action, session State, inWindow, sessionWaitingOnPerson bool) bool {
	if !inWindow || sessionWaitingOnPerson {
		return false
	}
	// A session that is stopping, or whose state could not be read, is not
	// one to start work under.
	if session != Idle && session != Active && session != Error {
		return false
	}
	switch a {
	case None, Wake, Nudge, Reprompt:
		return true
	}
	return false
}

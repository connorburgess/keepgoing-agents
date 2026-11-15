// Package contextwindow decides, from recorded facts only, whether a loop
// would reset its conversation because the context window is nearly full:
// the context-threshold reset of the roadmap. It is a pure function: it
// reads its input and nothing else, changes nothing and keeps no state.
//
// This build only previews the decision. Nothing resets, compacts, sends or
// asks for a checkpoint because of it; status shows what it would do at the
// next turn boundary. It is a package of its own, not part of timing or
// decide: timing moves a loop between routes on one usage allowance, while
// this is about one conversation's context, and decide must not read a
// context reading at all.
package contextwindow

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// Fixed limits of the rule. They are stated in every decision.
const (
	// MaxReadingAge is how old the latest reading may be. A check records
	// one when it sends and at a turn's end, so the reading behind a reset
	// at the next turn boundary is minutes old; one older than this may
	// predate the last turn and say nothing about the context now.
	MaxReadingAge = 15 * time.Minute
	// MinInterval is the least time between resets. Each reset costs sends
	// at the largest context; a conversation that refills its window in
	// under this needs a person, not more resets.
	MinInterval = 6 * time.Hour
	// DailyCap is the most resets in one UTC day.
	DailyCap = 2
)

// Rules, in the order they are tried. The first that applies decides. These
// names are stable.
const (
	RuleConfiguration = "configuration" // no threshold is set
	RuleControls      = "controls"      // stop, pause and holds win
	RuleReading       = "reading"       // the reading must be there, sound and fresh
	RuleSession       = "session"       // idle after a turn that ended normally
	RuleLanes         = "lanes"         // no lane may be working or unverified
	RuleThreshold     = "threshold"     // at or above the threshold
	RuleSpacing       = "spacing"       // spaced and capped
	RuleWouldReset    = "would_reset"   // every rule above passed
)

// Reason codes. These are stable.
const (
	// RuleConfiguration.
	NotConfigured = "not_configured"
	// RuleControls.
	Stopped            = "stopped"
	Paused             = "paused"
	Held               = "held"
	ControlsUnreadable = "controls_unreadable"
	// RuleReading.
	NotSupported        = "not_supported"
	NoReading           = "no_reading"
	MalformedReading    = "malformed_reading"
	EstimateFlagMissing = "estimate_flag_missing"
	ReadingTooOld       = "reading_too_old"
	// RuleSession.
	NoObservation = "no_observation"
	InError       = "in_error"
	LimitWait     = "limit_wait"
	Retrying      = "retrying"
	Refusal       = "refusal"
	AwaitingInput = "awaiting_input"
	NotIdle       = "not_idle"
	// RuleLanes.
	LanesActive     = "lanes_active"
	LanesUnverified = "lanes_unverified"
	// RuleThreshold.
	BelowThreshold = "below_threshold"
	// RuleSpacing.
	TooSoon      = "too_soon"
	DailyCapUsed = "daily_cap_used"
	// RuleWouldReset.
	AtOrAboveThreshold = "at_or_above_threshold"
)

// Reading is the latest recorded context window reading of the active
// target's conversation. Estimated is nil when the record does not say
// whether the provider marked the figures as an estimate.
type Reading struct {
	UsedTokens   int64
	WindowTokens int64
	Estimated    *bool
	At           time.Time
	// Malformed: the record's layout is not one this build reads.
	Malformed bool
}

// Controls are the person's controls of the loop.
type Controls struct {
	Stop, Paused bool
	// Held: disabled, awaiting a person or completed.
	Held       bool
	Unreadable bool
}

// Session is the last observation of the active target.
type Session struct {
	// Observed: a check observed the active target. State is its observed
	// state: idle, active, error, pending, stopping or unknown.
	Observed bool
	State    string
	// AwaitingInput: the session asked a person something.
	AwaitingInput bool
	// LimitWait: a provider limit is being waited out. Retrying: a failed
	// turn is being retried, or a provider retry was seen. Refusal: the last
	// turn was refused or a refusal hold is in place.
	LimitWait, Retrying, Refusal bool
}

// Lanes are the lanes the last check saw.
type Lanes struct {
	// Working counts lanes active or awaiting input. Unverified: the
	// listing was incomplete, missing, or older than the last check.
	Working    int
	Unverified bool
}

// Input is everything the decision reads.
type Input struct {
	Now time.Time
	// ResetAt is the threshold, a share of the window; nil: not configured.
	ResetAt *float64
	// Supported: the active target's agent reports a context window reading.
	Supported bool
	// Reading is nil when none is recorded for the active target.
	Reading  *Reading
	Controls Controls
	Session  Session
	Lanes    Lanes
	// LastReset is the last reset-related action and ResetsToday how many
	// were made this UTC day. This build makes none, so status passes nil
	// and 0.
	LastReset   *time.Time
	ResetsToday int
}

// Decision is what the rule would do, and why.
type Decision struct {
	WouldReset bool
	Rule       string
	Reason     string
	// Facts the sentence is built from.
	Threshold  float64
	Share      float64 // used of the window, when there is a sound reading
	UsedTokens int64
	Window     int64
	Estimated  *bool
	ReadingAge time.Duration
	SinceReset *time.Duration
	Lanes      int
	State      string
	// The rule's fixed limits.
	MaxReadingAge time.Duration
	MinInterval   time.Duration
	DailyCap      int
	ResetsToday   int
}

// Decide returns what the rule would do at the next turn boundary.
func Decide(in Input) Decision {
	d := Decision{MaxReadingAge: MaxReadingAge, MinInterval: MinInterval, DailyCap: DailyCap, ResetsToday: in.ResetsToday, State: in.Session.State, Lanes: in.Lanes.Working}
	no := func(rule, reason string) Decision { d.Rule, d.Reason = rule, reason; return d }
	if in.ResetAt == nil {
		return no(RuleConfiguration, NotConfigured)
	}
	d.Threshold = *in.ResetAt
	if in.LastReset != nil {
		since := in.Now.Sub(*in.LastReset)
		d.SinceReset = &since
	}
	c := in.Controls
	switch {
	case c.Unreadable:
		return no(RuleControls, ControlsUnreadable)
	case c.Stop:
		return no(RuleControls, Stopped)
	case c.Paused:
		return no(RuleControls, Paused)
	case c.Held:
		return no(RuleControls, Held)
	}
	r := in.Reading
	switch {
	case !in.Supported:
		return no(RuleReading, NotSupported)
	case r == nil:
		return no(RuleReading, NoReading)
	case r.Malformed || r.WindowTokens <= 0 || r.UsedTokens < 0 || r.At.IsZero() || r.At.After(in.Now):
		return no(RuleReading, MalformedReading)
	}
	d.UsedTokens, d.Window, d.Estimated = r.UsedTokens, r.WindowTokens, r.Estimated
	d.Share = float64(r.UsedTokens) / float64(r.WindowTokens)
	d.ReadingAge = in.Now.Sub(r.At)
	switch {
	case r.Estimated == nil:
		return no(RuleReading, EstimateFlagMissing)
	case d.ReadingAge > MaxReadingAge:
		return no(RuleReading, ReadingTooOld)
	}
	s := in.Session
	switch {
	case !s.Observed:
		return no(RuleSession, NoObservation)
	case s.State == "error":
		return no(RuleSession, InError)
	case s.LimitWait:
		return no(RuleSession, LimitWait)
	case s.Retrying:
		return no(RuleSession, Retrying)
	case s.Refusal:
		return no(RuleSession, Refusal)
	case s.AwaitingInput:
		return no(RuleSession, AwaitingInput)
	case s.State != "idle":
		return no(RuleSession, NotIdle)
	}
	switch {
	case in.Lanes.Unverified:
		return no(RuleLanes, LanesUnverified)
	case in.Lanes.Working > 0:
		return no(RuleLanes, LanesActive)
	}
	if d.Share < d.Threshold {
		return no(RuleThreshold, BelowThreshold)
	}
	switch {
	case d.SinceReset != nil && *d.SinceReset < MinInterval:
		return no(RuleSpacing, TooSoon)
	case in.ResetsToday >= DailyCap:
		return no(RuleSpacing, DailyCapUsed)
	}
	d.WouldReset = true
	return no(RuleWouldReset, AtOrAboveThreshold)
}

// Text is the decision in words, without the preview marker the caller
// adds. duration words a duration ("12m").
func (d Decision) Text(duration func(time.Duration) string) string {
	pct := func(f float64) int { return int(math.Floor(f*100 + 1e-9)) }
	figures := func() string {
		return fmt.Sprintf("%d%% of the window used (%s of %s tokens, %s)", pct(d.Share), digits(d.UsedTokens), digits(d.Window), d.source())
	}
	switch d.Reason {
	case NotConfigured:
		return "not configured"
	case AtOrAboveThreshold:
		return fmt.Sprintf("would reset: %s, at or above the %d%% threshold, the session idle after a turn that ended normally and no lane working", figures(), pct(d.Threshold))
	case ControlsUnreadable:
		return "would not reset: control.json could not be read"
	case Stopped:
		return "would not reset: the loop is stopped"
	case Paused:
		return "would not reset: the loop is paused"
	case Held:
		return "would not reset: the loop is held for a person"
	case NotSupported:
		return "would not reset: this target's agent reports no context window size"
	case NoReading:
		return "would not reset: no context window reading is recorded for this thread"
	case MalformedReading:
		return "would not reset: the recorded context window reading is not one this build can read"
	case EstimateFlagMissing:
		return "would not reset: the recorded reading does not say whether BB estimated it"
	case ReadingTooOld:
		return fmt.Sprintf("would not reset: the latest reading is %s old, over the %s a reading may be", duration(d.ReadingAge), duration(d.MaxReadingAge))
	case NoObservation:
		return "would not reset: no check has observed this thread yet"
	case InError:
		return "would not reset: the session is in error; the error rules decide"
	case LimitWait:
		return "would not reset: the loop is waiting out a provider limit"
	case Retrying:
		return "would not reset: a failed turn is being retried"
	case Refusal:
		return "would not reset: the last turn was refused"
	case AwaitingInput:
		return "would not reset: the session is waiting for a person's answer"
	case NotIdle:
		return fmt.Sprintf("would not reset: the session was last seen %s, not idle after a turn", d.State)
	case LanesUnverified:
		return "would not reset: the last check could not verify the lanes"
	case LanesActive:
		if d.Lanes == 1 {
			return "would not reset: 1 lane is working"
		}
		return fmt.Sprintf("would not reset: %d lanes are working", d.Lanes)
	case BelowThreshold:
		return fmt.Sprintf("would not reset: %s, below the %d%% threshold", figures(), pct(d.Threshold))
	case TooSoon:
		return fmt.Sprintf("would not reset: the last reset was %s ago, under the %s between resets", duration(*d.SinceReset), duration(d.MinInterval))
	case DailyCapUsed:
		return fmt.Sprintf("would not reset: %d of %d resets made today (UTC)", d.ResetsToday, d.DailyCap)
	}
	return "would not reset: " + d.Reason
}

// source keeps BB's word: an estimate is never shown as a measurement.
func (d Decision) source() string {
	switch {
	case d.Estimated == nil:
		return "not marked as estimated or reported"
	case *d.Estimated:
		return "estimated by BB"
	}
	return "reported by BB"
}

// digits writes 48000 as "48,000".
func digits(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

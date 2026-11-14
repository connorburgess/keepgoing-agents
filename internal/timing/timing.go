// Package timing decides, from recorded facts only, whether a loop would move
// between the steps a person listed on its first route's allowance: up to the
// ceiling near a reset that would otherwise leave much unused, down a step
// when the readings project the allowance running out before its reset, and
// back to the first route when the reason passes. It is a pure function: it
// reads its input and nothing else, changes nothing and keeps no state.
package timing

import (
	"fmt"
	"math"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

// First is the loop's first route, the step timing returns to.
const First = "primary"

// FreshForStepUp is how old the reading behind a step up may be.
const FreshForStepUp = time.Hour

// Actions.
const (
	Stay = "stay"
	Up   = "up"
	Down = "down"
	Back = "back"
)

// Rules, in the order they are tried. The first that applies decides, even
// when it decides to stay. These names are stable.
const (
	RuleGate          = "gate"            // the check's outcome rules timing out
	RuleReturnAtReset = "return_at_reset" // the reset a step was for has passed
	RuleStepBackEarly = "step_back_early" // stepped up, and now projected to run out
	RuleStepDown      = "step_down"       // projected to run out before the reset
	RuleStepUp        = "step_up"         // near a reset with much unused
	RuleNothingDue    = "nothing_due"     // no rule applies
)

// Reason codes. These are stable.
const (
	// RuleGate.
	NoSteps              = "no_steps"
	Held                 = "held"
	InError              = "in_error"
	OtherMove            = "other_move"
	TurnNotEnded         = "turn_not_ended"
	RefusalOrUnknown     = "refusal_or_unknown"
	OnFallbackByFailover = "on_fallback_by_failover"
	Retargeted           = "retargeted"
	NotOnStep            = "not_on_step"
	// RuleReturnAtReset.
	ResetPassed = "reset_passed"
	// RuleStepBackEarly.
	RunsOutSinceStepUp = "runs_out_since_step_up"
	// RuleStepDown.
	ProjectedOverDownAt = "projected_over_down_at"
	AtFloor             = "at_floor"
	// RuleStepUp.
	NearResetUnused  = "near_reset_unused"
	AtCeiling        = "at_ceiling"
	AlreadySteppedUp = "already_stepped_up"
	OtherWindowHigh  = "other_window_high"
	// Several rules.
	TooSoon = "too_soon" // less than min_on_step since the last timing move
	// RuleNothingDue.
	NoReading      = "no_reading"
	ReadingTooOld  = "reading_too_old"
	ProjectedUnder = "projected_under_down_at"
	CannotProject  = "cannot_project"
	NoNamedWindow  = "no_named_window"
)

// Move is the last move timing made, as U5 will record it.
type Move struct {
	At       time.Time
	From, To string
	Action   string // Up or Down
	Window   string
	ResetsAt time.Time
}

// Retarget is a person's move of the loop since the last timing move. Timing
// waits until ResetsAt, the reset of the named window current when it
// happened; with none known it waits until a person changes it again.
type Retarget struct {
	At       time.Time
	ResetsAt *time.Time
}

// Input is everything the decision reads.
type Input struct {
	Now    time.Time
	Timing config.Timing
	// ProbeTTL is how old the newest reading behind a step down may be.
	ProbeTTL time.Duration
	// Current is the route the loop is on.
	Current string
	// MovedByFailover: a handoff on a usage limit or another error put the
	// loop on Current. Timing never moves a loop off such a route.
	MovedByFailover bool
	Retarget        *Retarget
	LastMove        *Move
	// Usage and History are the latest reading and the recorded readings of
	// the first route's account, nil when none is recorded for it.
	Usage   *probe.Usage
	History *allowance.History
	// The check's outcome.
	Held             bool
	InError          bool
	OtherMove        bool
	TurnEnded        bool
	RefusalOrUnknown bool
}

// Decision is what timing would do, and why.
type Decision struct {
	Action string
	To     string // the step it moves to, for a move
	Rule   string
	Reason string
	// Window and ResetsAt are the window and its reset the decision is about.
	Window   string
	ResetsAt *time.Time
	// Facts the sentence is built from.
	Current    string
	ResetsIn   time.Duration
	Unused     float64
	ReadingAge time.Duration
	Projection *allowance.Projection
	SinceMove  time.Duration
	MinOnStep  time.Duration
}

// Decide returns what timing would do now.
func Decide(in Input) Decision {
	t := in.Timing
	d := Decision{Action: Stay, Current: in.Current, MinOnStep: t.MinOnStep.Duration}
	gate := func(reason string) Decision { d.Rule, d.Reason = RuleGate, reason; return d }
	switch {
	case len(t.Steps) == 0:
		return gate(NoSteps)
	case in.Held:
		return gate(Held)
	case in.InError:
		return gate(InError)
	case in.OtherMove:
		return gate(OtherMove)
	case in.RefusalOrUnknown:
		return gate(RefusalOrUnknown)
	case !in.TurnEnded:
		return gate(TurnNotEnded)
	case in.MovedByFailover:
		return gate(OnFallbackByFailover)
	case in.Retarget != nil && (in.Retarget.ResetsAt == nil || in.Now.Before(*in.Retarget.ResetsAt)):
		d.ResetsAt = in.Retarget.ResetsAt
		return gate(Retargeted)
	case t.StepIndex(in.Current) < 0:
		return gate(NotOnStep)
	}
	cur := t.StepIndex(in.Current)
	m := in.LastMove
	if m != nil && m.To != in.Current {
		m = nil // the loop has left the step timing moved it to
	}
	if m != nil {
		d.SinceMove = in.Now.Sub(m.At)
	}
	tooSoon := m != nil && d.SinceMove < t.MinOnStep.Duration

	// 2. The reset a step was for has passed: back to the first route.
	if m != nil && in.Current != First && !in.Now.Before(m.ResetsAt) {
		at := m.ResetsAt
		d.Rule, d.Reason, d.Action, d.To, d.Window, d.ResetsAt = RuleReturnAtReset, ResetPassed, Back, First, m.Window, &at
		return d
	}
	// 3. Stepped up, and the readings since then say it runs out first.
	if m != nil && m.Action == Up {
		if p := in.project(m.Window, m.At); p != nil && (p.RunsOutAt != nil || p.UsedUpSince != nil) {
			at := m.ResetsAt
			d.Rule, d.Window, d.ResetsAt, d.Projection = RuleStepBackEarly, m.Window, &at, p
			if tooSoon {
				d.Reason = TooSoon
				return d
			}
			d.Reason, d.Action, d.To = RunsOutSinceStepUp, Back, First
			return d
		}
	}
	// 4. Projected to reach down_at with more than near_reset left.
	for _, key := range t.Windows {
		w := in.window(key)
		if w == nil || w.ResetsAt == nil || w.ResetsAt.Sub(in.Now) <= t.NearReset.Duration {
			continue
		}
		since := time.Time{}
		if m != nil {
			since = m.At
		}
		p := in.project(key, since)
		if p == nil || p.UsedAtReset < t.DownAt {
			continue
		}
		at := *w.ResetsAt
		d.Rule, d.Window, d.ResetsAt, d.Projection, d.ResetsIn = RuleStepDown, key, &at, p, at.Sub(in.Now)
		switch {
		case cur == 0:
			d.Reason = AtFloor
		case tooSoon:
			d.Reason = TooSoon
		default:
			d.Reason, d.Action, d.To = ProjectedOverDownAt, Down, t.Steps[cur-1]
		}
		return d
	}
	// 5. Near a reset with at least unused_at_least unused, on a fresh reading.
	for _, key := range t.Windows {
		w := in.window(key)
		if w == nil || w.ResetsAt == nil || !w.ResetsAt.After(in.Now) || w.ResetsAt.Sub(in.Now) > t.NearReset.Duration {
			continue
		}
		age := in.Now.Sub(in.Usage.At)
		if age < 0 || age > FreshForStepUp || 1-w.Used < t.UnusedAtLeast {
			continue
		}
		at := *w.ResetsAt
		d.Rule, d.Window, d.ResetsAt, d.ResetsIn, d.Unused, d.ReadingAge = RuleStepUp, key, &at, at.Sub(in.Now), 1-w.Used, age
		ceiling := t.StepIndex(t.Ceiling)
		switch {
		case in.LastMove != nil && in.LastMove.Action == Up && in.LastMove.Window == key && sameReset(in.LastMove.ResetsAt, at):
			d.Reason = AlreadySteppedUp
		case cur >= ceiling:
			d.Reason = AtCeiling
		case in.otherWindowHigh(key):
			d.Reason = OtherWindowHigh
		case tooSoon:
			d.Reason = TooSoon
		default:
			d.Reason, d.Action, d.To = NearResetUnused, Up, t.Ceiling
		}
		return d
	}
	// 6. Nothing applies; say what the readings show for the first named window.
	d.Rule = RuleNothingDue
	if in.Usage == nil {
		d.Reason = NoReading
		return d
	}
	for _, key := range t.Windows {
		w := in.window(key)
		if w == nil {
			continue
		}
		d.Window, d.ReadingAge = key, in.Now.Sub(in.Usage.At)
		if w.ResetsAt != nil {
			at := *w.ResetsAt
			d.ResetsAt, d.ResetsIn = &at, at.Sub(in.Now)
			if at.After(in.Now) && at.Sub(in.Now) <= t.NearReset.Duration && d.ReadingAge > FreshForStepUp {
				d.Reason = ReadingTooOld
				return d
			}
		}
		since := time.Time{}
		if m != nil {
			since = m.At
		}
		if p := in.project(key, since); p != nil {
			d.Reason, d.Projection = ProjectedUnder, p
		} else {
			d.Reason = CannotProject
			pr := in.projection(key, since)
			d.Projection = &pr
		}
		return d
	}
	d.Reason = NoNamedWindow
	return d
}

// window is the named window in the latest reading.
func (in Input) window(key string) *probe.UsageWindow {
	if in.Usage == nil {
		return nil
	}
	for i := range in.Usage.Windows {
		if in.Usage.Windows[i].Key == key {
			return &in.Usage.Windows[i]
		}
	}
	return nil
}

// projection projects the window's current instance from the readings taken
// at or after since, as the display does, with the newest no older than the
// probe TTL.
func (in Input) projection(key string, since time.Time) allowance.Projection {
	w := in.window(key)
	if w == nil || w.ResetsAt == nil {
		return allowance.Projection{Reason: allowance.NoResetTime}
	}
	s := in.History.Find(key)
	if s == nil || !sameReset(s.ResetsAt, *w.ResetsAt) {
		return allowance.Projection{Reason: allowance.NoHistory}
	}
	kept := allowance.Series{Key: s.Key, ResetsAt: s.ResetsAt}
	for _, x := range s.Samples {
		if !x.At.Before(since) {
			kept.Samples = append(kept.Samples, x)
		}
	}
	return allowance.Project(&kept, in.Now, allowance.DisplayRules(in.ProbeTTL))
}

// project is projection when one is made, otherwise nil.
func (in Input) project(key string, since time.Time) *allowance.Projection {
	p := in.projection(key, since)
	if !p.Projected {
		return nil
	}
	return &p
}

// otherWindowHigh: another named window is at or projected at down_at.
func (in Input) otherWindowHigh(key string) bool {
	for _, k := range in.Timing.Windows {
		if k == key {
			continue
		}
		w := in.window(k)
		if w == nil {
			continue
		}
		if w.Used >= in.Timing.DownAt {
			return true
		}
		if p := in.project(k, time.Time{}); p != nil && p.UsedAtReset >= in.Timing.DownAt {
			return true
		}
	}
	return false
}

func sameReset(a, b time.Time) bool {
	d := a.Sub(b)
	return d <= allowance.SameReset && d >= -allowance.SameReset
}

// Text is the decision in words, with lengths of time as duration gives them.
func (d Decision) Text(duration func(time.Duration) string) string {
	pct := func(f float64) int { return int(math.Floor(f*100 + 1e-9)) }
	switch d.Reason {
	case NearResetUnused:
		return fmt.Sprintf("step up to %s: the %s allowance resets in %s with %d%% unused (reading %s old)", d.To, d.Window, duration(d.ResetsIn), pct(d.Unused), duration(d.ReadingAge))
	case ProjectedOverDownAt:
		return fmt.Sprintf("step down to %s: %s", d.To, d.projectionWords(duration))
	case ResetPassed:
		return fmt.Sprintf("return to %s: the %s window the step was for has reset", d.To, d.Window)
	case RunsOutSinceStepUp:
		return fmt.Sprintf("return to %s: the readings since stepping up show %s running out before it resets", d.To, d.Window)
	case NoSteps:
		return "stay: no steps are configured, so timing only collects readings"
	case Held:
		return "stay: the loop is held"
	case InError:
		return "stay: the session is in error; the error rules decide"
	case OtherMove:
		return "stay: another move was decided in this check"
	case RefusalOrUnknown:
		return "stay: the last turn ended in a refusal or an unknown state"
	case TurnNotEnded:
		return "stay: a turn is running; timing acts only at a turn boundary"
	case OnFallbackByFailover:
		return fmt.Sprintf("stay: the loop is on %s because the failover rules moved it there; timing does not move it off", d.Current)
	case Retargeted:
		return fmt.Sprintf("stay: a person moved the loop to %s; timing waits until the window resets", d.Current)
	case NotOnStep:
		return fmt.Sprintf("stay: the loop is on %s, which is not one of the steps", d.Current)
	case AtFloor:
		return fmt.Sprintf("stay at %s, the cheapest step, although %s", d.Current, d.projectionWords(duration))
	case AtCeiling:
		return fmt.Sprintf("stay at %s, the ceiling, although the %s allowance resets in %s with %d%% unused", d.Current, d.Window, duration(d.ResetsIn), pct(d.Unused))
	case AlreadySteppedUp:
		return fmt.Sprintf("stay: timing already stepped up for this %s window", d.Window)
	case OtherWindowHigh:
		return fmt.Sprintf("stay: the %s allowance resets in %s with %d%% unused, but another named window is at or projected near its limit", d.Window, duration(d.ResetsIn), pct(d.Unused))
	case TooSoon:
		return fmt.Sprintf("stay: the last timing move was %s ago, under the %s each step is kept", duration(d.SinceMove), duration(d.MinOnStep))
	case NoReading:
		return "stay: no usage reading of the first route's account is recorded"
	case ReadingTooOld:
		return fmt.Sprintf("stay: the %s allowance resets in %s, but the newest reading is %s old, older than %s", d.Window, duration(d.ResetsIn), duration(d.ReadingAge), duration(FreshForStepUp))
	case ProjectedUnder:
		return fmt.Sprintf("stay: %s", d.projectionWords(duration))
	case CannotProject:
		reason := "the readings cannot say"
		if d.Projection != nil {
			if w, ok := projectionWhy[d.Projection.Reason]; ok {
				reason = w
			}
		}
		return fmt.Sprintf("stay: %s cannot be projected: %s", d.Window, reason)
	case NoNamedWindow:
		return "stay: the latest reading reports none of the named windows"
	}
	return "stay"
}

// projectionWhy says why a window cannot be projected, briefly.
var projectionWhy = map[string]string{
	allowance.NoHistory:      "no earlier reading of it is recorded",
	allowance.NoResetTime:    "no reset time was reported",
	allowance.WindowReset:    "the window has reset",
	allowance.TooFewReadings: "fewer than three readings",
	allowance.NewestTooOld:   "the newest reading is too old",
	allowance.SpanTooShort:   "the readings cover too short a time",
	allowance.TooEarly:       "too early in the window",
	allowance.UsedWentDown:   "a reading is lower than an earlier one",
}

func (d Decision) projectionWords(duration func(time.Duration) string) string {
	p := d.Projection
	if p == nil {
		return d.Window + " has no projection"
	}
	over := fmt.Sprintf("%d readings over %s", p.Readings, duration(p.Span))
	switch {
	case p.UsedUpSince != nil:
		return fmt.Sprintf("%s is used up", d.Window)
	case p.RunsOutAt != nil && d.ResetsAt != nil:
		return fmt.Sprintf("at the recent rate %s runs out about %s before it resets (%s)", d.Window, duration(d.ResetsAt.Sub(*p.RunsOutAt)), over)
	}
	return fmt.Sprintf("%s is projected at %d%% at reset (%s)", d.Window, int(math.Floor(math.Min(p.UsedAtReset, 9.99)*100+1e-9)), over)
}

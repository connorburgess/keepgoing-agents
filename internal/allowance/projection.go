package allowance

import (
	"math"
	"time"
)

// DefaultMinSpan is the shortest time between the oldest and newest reading
// of a window instance from which a rate is taken.
const DefaultMinSpan = 6 * time.Hour

// Rules bound when a projection is made.
type Rules struct {
	// MinSpan is the shortest time from the oldest to the newest reading.
	MinSpan time.Duration
	// MaxAge is how old the newest reading may be. Zero means no limit.
	MaxAge time.Duration
}

// Why a projection could not be made.
const (
	NoHistory    = "no_history"     // no recorded readings of this window instance
	NoResetTime  = "no_reset_time"  // the window was reported without a reset time
	WindowReset  = "window_reset"   // the instance's reset time has passed
	OneReading   = "one_reading"    // fewer than two readings in this instance
	ClockBehind  = "clock_behind"   // the newest reading is later than now
	NewestTooOld = "newest_too_old" // the newest reading is older than MaxAge
	SpanTooShort = "span_too_short" // the readings cover less than MinSpan
	UsedWentDown = "used_went_down" // a reading is lower than one before it
)

// Projection is what the recorded readings say about a window at its reset,
// or why they cannot say.
type Projection struct {
	// Projected is false when Reason says why there is no projection.
	Projected bool
	Reason    string
	Readings  int
	// Span is from the oldest to the newest reading.
	Span     time.Duration
	NewestAt time.Time
	ResetsAt time.Time
	// UsedAtReset is the newest used fraction plus the recorded rate over the
	// time from the newest reading to the reset. It may exceed 1.
	UsedAtReset float64
	// RunsOutAt is set when the window is projected to be fully used before
	// it resets: when the recorded rate reaches 1, or the newest reading if it
	// already did.
	RunsOutAt *time.Time
}

// Project makes a straight-line projection from the oldest and newest
// readings of one window instance. It reads only the series and now; it
// never estimates a reading that was not recorded. The rate covers all use of
// the account, by any loop or person, between the two readings.
func Project(s *Series, now time.Time, r Rules) Projection {
	if s == nil || len(s.Samples) == 0 {
		return Projection{Reason: NoHistory}
	}
	oldest, newest := s.Samples[0], s.Samples[len(s.Samples)-1]
	p := Projection{Readings: len(s.Samples), Span: newest.At.Sub(oldest.At), NewestAt: newest.At, ResetsAt: s.ResetsAt}
	switch {
	case !s.ResetsAt.After(now):
		p.Reason = WindowReset
	case len(s.Samples) < 2:
		p.Reason = OneReading
	case newest.At.After(now):
		p.Reason = ClockBehind
	case r.MaxAge > 0 && now.Sub(newest.At) > r.MaxAge:
		p.Reason = NewestTooOld
	case p.Span < r.MinSpan:
		p.Reason = SpanTooShort
	case wentDown(s.Samples):
		p.Reason = UsedWentDown
	}
	if p.Reason != "" {
		return p
	}
	rate := (newest.Used - oldest.Used) / p.Span.Seconds()
	p.Projected = true
	p.UsedAtReset = newest.Used + rate*s.ResetsAt.Sub(newest.At).Seconds()
	switch {
	case newest.Used >= 1:
		t := newest.At
		p.RunsOutAt = &t
	case p.UsedAtReset >= 1 && rate > 0:
		t := newest.At.Add(time.Duration(math.Round((1-newest.Used)/rate)) * time.Second)
		if t.Before(s.ResetsAt) {
			p.RunsOutAt = &t
		}
	}
	return p
}

func wentDown(samples []Sample) bool {
	for i := 1; i < len(samples); i++ {
		if samples[i].Used < samples[i-1].Used-1e-9 {
			return true
		}
	}
	return false
}

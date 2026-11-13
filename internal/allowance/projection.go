package allowance

import (
	"math"
	"time"
)

// Defaults for the display rules.
const (
	// DefaultMinSpan is the shortest time between the oldest and newest
	// reading of a window instance from which a rate is taken.
	DefaultMinSpan = 6 * time.Hour
	// DefaultMinReadings is the fewest readings a rate is fitted through.
	DefaultMinReadings = 3
	// DefaultMaxAhead bounds the extrapolation: the time from the newest
	// reading to the reset may be at most this many times the span the
	// readings cover.
	DefaultMaxAhead = 4
	// DefaultRounding is how far a reported fraction may be from the true one.
	// Some providers report whole percents.
	DefaultRounding = 0.01
)

// Rules bound when a projection is made.
type Rules struct {
	// MinSpan is the shortest time from the oldest to the newest reading.
	MinSpan time.Duration
	// MinReadings is the fewest readings, at least 2.
	MinReadings int
	// MaxAge is how old the newest reading may be. Zero means no limit.
	MaxAge time.Duration
	// MaxAhead is how many times the readings' span the projection may reach
	// ahead of the newest reading. Zero means no limit.
	MaxAhead float64
	// Rounding is how far each reported fraction may be from the true one.
	// "Runs out" is said only when it holds for every rate consistent with
	// readings off by that much.
	Rounding float64
}

// DisplayRules are the rules for showing a projection, with the newest
// reading no older than maxAge.
func DisplayRules(maxAge time.Duration) Rules {
	return Rules{MinSpan: DefaultMinSpan, MinReadings: DefaultMinReadings, MaxAge: maxAge, MaxAhead: DefaultMaxAhead, Rounding: DefaultRounding}
}

// Why a projection could not be made.
const (
	NoHistory      = "no_history"       // no recorded readings of this window instance
	NoResetTime    = "no_reset_time"    // the window was reported without a reset time
	WindowReset    = "window_reset"     // the instance's reset time has passed
	TooFewReadings = "too_few_readings" // fewer than MinReadings readings in this instance
	NewestTooOld   = "newest_too_old"   // the newest reading is older than MaxAge
	SpanTooShort   = "span_too_short"   // the readings cover less than MinSpan
	TooEarly       = "too_early"        // the reset is more than MaxAhead spans ahead
	UsedWentDown   = "used_went_down"   // a reading is lower than one before it
)

// Projection is what the recorded readings say about a window at its reset,
// or why they cannot say.
type Projection struct {
	// Projected is false when Reason says why there is no projection.
	Projected bool
	Reason    string
	// Readings counts the readings used; Ignored those stamped after now,
	// which a clock set back leaves behind.
	Readings int
	Ignored  int
	// Span is from the oldest to the newest reading used.
	Span     time.Duration
	NewestAt time.Time
	ResetsAt time.Time
	// UsedUpSince is the first of the latest run of readings at 100%: the
	// window was used up by then. No rate is needed for that.
	UsedUpSince *time.Time
	// UsedAtReset is the newest used fraction plus the fitted rate over the
	// time from the newest reading to the reset. It may exceed 1.
	UsedAtReset float64
	// RunsOutAt is when the fitted rate reaches 100%, set only when it does
	// so before the reset for every rate within the readings' rounding. It
	// may be before now: the readings say it has already run out.
	RunsOutAt *time.Time
	// WithinRounding is set when the fitted rate reaches 100% before the
	// reset but a rate consistent with the readings' rounding does not.
	WithinRounding bool
}

// Project fits a rate through the recorded readings of one window instance
// and extends it from the newest reading to the reset. The rate is the least
// squares slope through every reading used, so one early jump or a single
// rounding step does not decide it alone. It reads only the series and now;
// it never estimates a reading that was not recorded. The rate covers all use
// of the account, by any loop or person, over the readings' span.
func Project(s *Series, now time.Time, r Rules) Projection {
	if s == nil || len(s.Samples) == 0 {
		return Projection{Reason: NoHistory}
	}
	samples := make([]Sample, 0, len(s.Samples))
	for _, x := range s.Samples {
		if !x.At.After(now) {
			samples = append(samples, x)
		}
	}
	p := Projection{Readings: len(samples), Ignored: len(s.Samples) - len(samples), ResetsAt: s.ResetsAt}
	if !s.ResetsAt.After(now) {
		p.Reason = WindowReset
		return p
	}
	if len(samples) == 0 {
		p.Reason = TooFewReadings
		return p
	}
	oldest, newest := samples[0], samples[len(samples)-1]
	p.Span, p.NewestAt = newest.At.Sub(oldest.At), newest.At
	if newest.Used >= 1 {
		since := newest.At
		for i := len(samples) - 1; i >= 0 && samples[i].Used >= 1; i-- {
			since = samples[i].At
		}
		p.Projected, p.UsedUpSince, p.UsedAtReset = true, &since, 1
		return p
	}
	ahead := s.ResetsAt.Sub(newest.At)
	switch {
	case len(samples) < max(2, r.MinReadings):
		p.Reason = TooFewReadings
	case r.MaxAge > 0 && now.Sub(newest.At) > r.MaxAge:
		p.Reason = NewestTooOld
	case p.Span < r.MinSpan || p.Span <= 0:
		p.Reason = SpanTooShort
	case wentDown(samples):
		p.Reason = UsedWentDown
	case r.MaxAhead > 0 && ahead.Seconds() > r.MaxAhead*p.Span.Seconds():
		p.Reason = TooEarly
	}
	if p.Reason != "" {
		return p
	}
	slope, slack := fit(samples)
	p.Projected = true
	p.UsedAtReset = newest.Used + slope*ahead.Seconds()
	if p.UsedAtReset < 1 {
		return p
	}
	// The slowest rate the readings allow when each may be off by Rounding,
	// from a newest reading that may be that much high.
	low := math.Max(0, slope-r.Rounding*slack)
	if newest.Used-r.Rounding+low*ahead.Seconds() < 1 {
		p.WithinRounding = true
		return p
	}
	t := newest.At.Add(time.Duration(math.Round((1-newest.Used)/slope)) * time.Second)
	p.RunsOutAt = &t
	return p
}

// fit returns the least squares slope per second through the samples, and
// how far that slope can move per unit of error in every reading: the bound
// sum|t-mean| / sum (t-mean)^2.
func fit(samples []Sample) (slope, slack float64) {
	t0 := samples[0].At
	var mt, mu float64
	for _, x := range samples {
		mt += x.At.Sub(t0).Seconds()
		mu += x.Used
	}
	n := float64(len(samples))
	mt, mu = mt/n, mu/n
	var sxy, sxx, sabs float64
	for _, x := range samples {
		dt := x.At.Sub(t0).Seconds() - mt
		sxy += dt * (x.Used - mu)
		sxx += dt * dt
		sabs += math.Abs(dt)
	}
	return math.Max(0, sxy/sxx), sabs / sxx
}

func wentDown(samples []Sample) bool {
	for i := 1; i < len(samples); i++ {
		if samples[i].Used < samples[i-1].Used-1e-9 {
			return true
		}
	}
	return false
}

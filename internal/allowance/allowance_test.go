package allowance

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/probe"
)

var (
	t0    = time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	reset = t0.Add(4 * 24 * time.Hour)
)

func at(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }

func reading(identity string, hour float64, windows ...probe.UsageWindow) *probe.Usage {
	return &probe.Usage{At: at(hour), Source: probe.UsageSourceClaude, Identity: identity, Windows: windows}
}

func win(key string, used float64, resetsAt *time.Time) probe.UsageWindow {
	return probe.UsageWindow{Key: key, Used: used, ResetsAt: resetsAt}
}

func ptr(t time.Time) *time.Time { return &t }

func TestAddBuildsOneSeriesPerWindowInstance(t *testing.T) {
	var h *History
	h = Add(h, nil)
	if h != nil {
		t.Fatal("no reading made a history")
	}
	h = Add(h, reading("account-a", 0, win("seven_day", 0.1, &reset), win("no_reset", 0.3, nil)))
	h = Add(h, reading("account-a", 6, win("seven_day", 0.2, ptr(reset.Add(time.Minute)))))
	// The same reading again adds nothing.
	h = Add(h, reading("account-a", 6, win("seven_day", 0.2, &reset)))
	s := h.Find("seven_day")
	if !h.For("account-a") || h.For("account-b") || s == nil || len(s.Samples) != 2 || !s.ResetsAt.Equal(reset.Add(time.Minute)) {
		t.Fatalf("history: %+v", h)
	}
	if h.Find("no_reset") != nil {
		t.Fatal("a window without a reset time has no instance and is not recorded")
	}
	// A later probe without a reading leaves the history as it is.
	if Add(h, &probe.Usage{}) != h {
		t.Fatal("an absent reading changed the history")
	}

	// A reset time that moved by more than SameReset is a new instance.
	next := reset.Add(7 * 24 * time.Hour)
	h2 := Add(h, reading("account-a", 7, win("seven_day", 0.01, &next)))
	if s := h2.Find("seven_day"); len(s.Samples) != 1 || !s.ResetsAt.Equal(next) {
		t.Fatalf("new instance: %+v", s)
	}
	if len(h.Find("seven_day").Samples) != 2 {
		t.Fatal("Add changed the history it was given")
	}
	// After a clock that was ahead is set back, readings stamped later than
	// the new one are dropped and the new one is kept: the current clock wins.
	set := Add(h, reading("account-a", 3, win("seven_day", 0.15, &reset)))
	if s := set.Find("seven_day"); len(s.Samples) != 2 || !s.Samples[1].At.Equal(at(3)) || s.Samples[1].Used != 0.15 {
		t.Fatalf("after the clock was set back: %+v", s)
	}

	// Another identity starts over.
	h3 := Add(h, reading("account-b", 7, win("seven_day", 0.5, &reset)))
	if !h3.For("account-b") || len(h3.Find("seven_day").Samples) != 1 || h3.Find("seven_day").Samples[0].Used != 0.5 {
		t.Fatalf("other account: %+v", h3)
	}
}

func TestAddDropsWindowsThatHaveReset(t *testing.T) {
	short := at(5)
	h := Add(nil, reading("account-a", 0, win("five_hour", 0.4, &short), win("seven_day", 0.1, &reset)))
	// A reading after the short window's reset drops its series, even though
	// this reading does not mention that window.
	h = Add(h, reading("account-a", 6, win("seven_day", 0.2, &reset)))
	if h.Find("five_hour") != nil || len(h.Find("seven_day").Samples) != 2 {
		t.Fatalf("history: %+v", h)
	}
	// A reading that reports a window whose reset is at or before its own
	// time comes from a clock that may be ahead: nothing is recorded and
	// nothing dropped, so a clock a year ahead cannot wipe the history.
	if got := Add(h, reading("account-a", 24*365, win("seven_day", 0.3, &reset))); got.Find("seven_day") == nil || len(got.Find("seven_day").Samples) != 2 {
		t.Fatalf("a clock far ahead changed the history: %+v", got)
	}
	// A reading after the reset that reports the next instance replaces it.
	next := reset.Add(7 * 24 * time.Hour)
	if s := Add(h, reading("account-a", 24*5, win("seven_day", 0.01, &next))).Find("seven_day"); len(s.Samples) != 1 || !s.ResetsAt.Equal(next) {
		t.Fatalf("next instance: %+v", s)
	}
	// When every series has reset there is no history.
	if got := Add(h, reading("account-a", 24*5, win("other", 0.3, nil))); got != nil {
		t.Fatalf("history kept after every window reset: %+v", got)
	}
}

func TestAddIsBounded(t *testing.T) {
	var h *History
	for i := 0; i < MaxSamples+10; i++ {
		h = Add(h, reading("account-a", float64(i), win("seven_day", float64(i)/100, &reset)))
	}
	s := h.Find("seven_day")
	if len(s.Samples) != MaxSamples || !s.Samples[0].At.Equal(at(10)) || !s.Samples[MaxSamples-1].At.Equal(at(MaxSamples+9)) {
		t.Fatalf("samples: %d, first %s", len(s.Samples), s.Samples[0].At)
	}
	// Windows are capped too, keeping those read most recently.
	h = nil
	for i := 0; i < MaxWindows+3; i++ {
		h = Add(h, reading("account-a", float64(i), win(string(rune('a'+i)), 0.1, &reset)))
	}
	if len(h.Windows) != MaxWindows || h.Find("a") != nil || h.Find("k") == nil {
		t.Fatalf("windows: %+v", h.Windows)
	}
	for i := 1; i < len(h.Windows); i++ {
		if h.Windows[i-1].Key >= h.Windows[i].Key {
			t.Fatal("windows are not sorted by key")
		}
	}
}

func TestStoredHistoryLoadsDefensively(t *testing.T) {
	good := `{"key":"seven_day","resetsAt":"2026-11-13T00:00:00Z","samples":[{"at":"2026-11-09T00:00:00Z","used":0.1},{"at":"2026-11-09T06:00:00Z","used":0.2}]}`
	for name, tc := range map[string]struct {
		in      string
		windows int
	}{
		"good":                  {`{"identity":"account-a","windows":[` + good + `]}`, 1},
		"not an object":         {`"seven_day 20%"`, 0},
		"no identity":           {`{"windows":[` + good + `]}`, 0},
		"identity with spaces":  {`{"identity":"account a","windows":[` + good + `]}`, 0},
		"out of range":          {`{"identity":"account-a","windows":[{"key":"seven_day","resetsAt":"2026-11-13T00:00:00Z","samples":[{"at":"2026-11-09T00:00:00Z","used":22}]}]}`, 0},
		"no reset time":         {`{"identity":"account-a","windows":[{"key":"seven_day","samples":[{"at":"2026-11-09T00:00:00Z","used":0.1}]}]}`, 0},
		"out of order":          {`{"identity":"account-a","windows":[{"key":"seven_day","resetsAt":"2026-11-13T00:00:00Z","samples":[{"at":"2026-11-09T06:00:00Z","used":0.2},{"at":"2026-11-09T00:00:00Z","used":0.1}]}]}`, 0},
		"repeated key":          {`{"identity":"account-a","windows":[` + good + `,` + good + `]}`, 1},
		"one bad, one good":     {`{"identity":"account-a","windows":[{"key":"bad key","resetsAt":"2026-11-13T00:00:00Z","samples":[{"at":"2026-11-09T00:00:00Z","used":0.1}]},` + good + `]}`, 1},
		"no samples":            {`{"identity":"account-a","windows":[{"key":"seven_day","resetsAt":"2026-11-13T00:00:00Z","samples":[]}]}`, 0},
		"wrong field types":     {`{"identity":5,"windows":"x"}`, 0},
		"sample without a time": {`{"identity":"account-a","windows":[{"key":"seven_day","resetsAt":"2026-11-13T00:00:00Z","samples":[{"used":0.1}]}]}`, 0},
	} {
		var h History
		if err := json.Unmarshal([]byte(tc.in), &h); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(h.Windows) != tc.windows {
			t.Errorf("%s: %+v", name, h)
		}
	}
}

func series(reset time.Time, samples ...Sample) *Series {
	return &Series{Key: "seven_day", ResetsAt: reset, Samples: samples}
}

func s(hour, used float64) Sample { return Sample{At: at(hour), Used: used} }

// fitted is the least squares slope per hour through the given points.
func fitted(points ...Sample) float64 {
	slope, _ := fit(points)
	return slope * 3600
}

func TestProjectTable(t *testing.T) {
	rules := DisplayRules(6 * time.Hour)
	r60, r96 := at(60), at(96)
	for name, tc := range map[string]struct {
		s        *Series
		now      float64
		reason   string
		atReset  float64
		runsOut  *time.Time
		rounding bool
		usedUp   *time.Time
		ignored  int
	}{
		"no history":   {s: nil, now: 10, reason: NoHistory},
		"window reset": {s: series(r60, s(0, 0.1), s(6, 0.15), s(12, 0.2)), now: 60, reason: WindowReset},
		"one reading":  {s: series(r60, s(0, 0.1)), now: 1, reason: TooFewReadings},
		// Reviewer case 1: one pair at the minimum span, 75% to 77%, would be
		// extrapolated about 27 times its span; a pair is too few readings.
		"a pair is too few": {s: series(at(168), s(0, 0.75), s(6, 0.77)), now: 6, reason: TooFewReadings},
		// The same with a third reading is still too early: 162 h ahead of 6 h of readings.
		"too early to project": {s: series(at(168), s(0, 0.75), s(3, 0.76), s(6, 0.77)), now: 6, reason: TooEarly},
		"newest too old":       {s: series(r60, s(0, 0.1), s(6, 0.15), s(12, 0.2)), now: 18.5, reason: NewestTooOld},
		"span too short":       {s: series(r60, s(0, 0.1), s(2, 0.15), s(5.5, 0.2)), now: 6, reason: SpanTooShort},
		"used went down":       {s: series(r60, s(0, 0.3), s(3, 0.2), s(12, 0.4)), now: 12, reason: UsedWentDown},
		"four spans ahead":     {s: series(r96, s(0, 0.1), s(6, 0.15), s(12, 0.2)), now: 12, reason: TooEarly},
		// 10% in 12 h, 48 h left after the newest reading: 0.2 + 0.4 = 0.6.
		"stays inside":           {s: series(r60, s(0, 0.1), s(6, 0.15), s(12, 0.2)), now: 13, atReset: 0.6},
		"newest just within age": {s: series(r60, s(0, 0.1), s(6, 0.15), s(12, 0.2)), now: 18, atReset: 0.6},
		// Reviewer case 2: a jump in the first hour, then flat for 23 h. The
		// first and last readings alone said it runs out at the reset; the fit
		// through all four does not.
		"jump then flat": {s: series(at(72), s(0, 0.1), s(1, 0.4), s(12, 0.4), s(24, 0.4)), now: 24,
			atReset: 0.4 + 48*fitted(s(0, 0.1), s(1, 0.4), s(12, 0.4), s(24, 0.4))},
		// 2.5% an hour: full 24 h after the newest reading, 24 h before the reset.
		"runs out before the reset": {s: series(r60, s(0, 0.1), s(6, 0.25), s(12, 0.4)), now: 12, atReset: 0.4 + 0.025*48, runsOut: ptr(at(36))},
		// Item 3: the fitted rate reached 100% two hours ago, after the newest reading.
		"ran out after the newest reading": {s: series(at(30), s(0, 0.4), s(3, 0.6), s(6, 0.8)), now: 11, atReset: 0.8 + 0.2/3*24, runsOut: ptr(at(9))},
		// The fit reaches 105% at the reset, but readings off by one percent allow 96%.
		"within rounding": {s: series(r60, s(0, 0.8), s(6, 0.825), s(12, 0.85)), now: 12, atReset: 0.85 + 0.025/6*48, rounding: true},
		// Reviewer case 3: used up is dated at the first of the latest readings at 100%.
		"used up":              {s: series(r60, s(0, 0.6), s(6, 1), s(12, 1)), now: 12, atReset: 1, usedUp: ptr(at(6))},
		"used up, one reading": {s: series(r60, s(5, 1)), now: 12, atReset: 1, usedUp: ptr(at(5))},
		"flat":                 {s: series(r60, s(0, 0.5), s(6, 0.5), s(12, 0.5)), now: 12, atReset: 0.5},
		// Readings stamped after now, left by a clock set back, are ignored.
		"later-stamped readings ignored": {s: series(r60, s(0, 0.1), s(6, 0.15), s(12, 0.2), s(30, 0.9)), now: 13, atReset: 0.6, ignored: 1},
	} {
		p := Project(tc.s, at(tc.now), rules)
		if p.Reason != tc.reason || p.Projected != (tc.reason == "") || p.Ignored != tc.ignored {
			t.Errorf("%s: reason %q projected %v ignored %d, want %q", name, p.Reason, p.Projected, p.Ignored, tc.reason)
			continue
		}
		if !p.Projected {
			continue
		}
		if math.Abs(p.UsedAtReset-tc.atReset) > 1e-9 || p.WithinRounding != tc.rounding {
			t.Errorf("%s: at reset %v within rounding %v, want %v %v", name, p.UsedAtReset, p.WithinRounding, tc.atReset, tc.rounding)
		}
		if (p.RunsOutAt == nil) != (tc.runsOut == nil) || (p.RunsOutAt != nil && !p.RunsOutAt.Equal(*tc.runsOut)) {
			t.Errorf("%s: runs out %v, want %v", name, p.RunsOutAt, tc.runsOut)
		}
		if (p.UsedUpSince == nil) != (tc.usedUp == nil) || (p.UsedUpSince != nil && !p.UsedUpSince.Equal(*tc.usedUp)) {
			t.Errorf("%s: used up since %v, want %v", name, p.UsedUpSince, tc.usedUp)
		}
		if p.Readings != len(tc.s.Samples)-tc.ignored || !p.ResetsAt.Equal(tc.s.ResetsAt) {
			t.Errorf("%s: %+v", name, p)
		}
	}
	// No age limit when MaxAge is zero, and no limit ahead when MaxAhead is zero.
	open := Rules{MinSpan: DefaultMinSpan, MinReadings: 3}
	if p := Project(series(r96, s(0, 0.1), s(6, 0.15), s(12, 0.2)), at(80), open); !p.Projected {
		t.Fatalf("no limits: %+v", p)
	}
}

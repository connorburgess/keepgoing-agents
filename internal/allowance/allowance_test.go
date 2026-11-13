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
	// The same reading again, and one from a clock set back, add nothing.
	h = Add(h, reading("account-a", 6, win("seven_day", 0.2, &reset)))
	h = Add(h, reading("account-a", 3, win("seven_day", 0.15, &reset)))
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
	// An earlier reading of another instance does not replace a later one.
	if s := Add(h2, reading("account-a", 6.5, win("seven_day", 0.9, &reset))).Find("seven_day"); len(s.Samples) != 1 || !s.ResetsAt.Equal(next) {
		t.Fatalf("older instance replaced the newer: %+v", s)
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
	// When every series has reset there is no history.
	if h = Add(h, reading("account-a", 24*5, win("seven_day", 0.3, ptr(at(24*5))))); h != nil {
		t.Fatalf("history kept after every window reset: %+v", h)
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

func series(samples ...Sample) *Series {
	return &Series{Key: "seven_day", ResetsAt: reset, Samples: samples}
}

func s(hour, used float64) Sample { return Sample{At: at(hour), Used: used} }

func TestProjectTable(t *testing.T) {
	rules := Rules{MinSpan: DefaultMinSpan, MaxAge: 6 * time.Hour}
	for name, tc := range map[string]struct {
		s       *Series
		now     float64
		reason  string
		atReset float64
		runsOut *time.Time
	}{
		"no history":                 {nil, 10, NoHistory, 0, nil},
		"window reset":               {series(s(0, 0.1), s(12, 0.2)), 24 * 4, WindowReset, 0, nil},
		"one reading":                {series(s(0, 0.1)), 1, OneReading, 0, nil},
		"clock behind":               {series(s(0, 0.1), s(12, 0.2)), 11, ClockBehind, 0, nil},
		"newest too old":             {series(s(0, 0.1), s(12, 0.2)), 18.5, NewestTooOld, 0, nil},
		"newest just within the age": {series(s(0, 0.1), s(12, 0.2)), 18, "", 0.2 + 0.1/12*84, nil},
		"span too short":             {series(s(0, 0.1), s(5.5, 0.2)), 6, SpanTooShort, 0, nil},
		"used went down":             {series(s(0, 0.3), s(3, 0.2), s(12, 0.4)), 12, UsedWentDown, 0, nil},
		// 10% in 12 h, 84 h left after the newest reading: 0.2 + 0.7 = 0.9.
		"stays inside": {series(s(0, 0.1), s(6, 0.12), s(12, 0.2)), 13, "", 0.9, nil},
		// 30% in 12 h: full 12 h after the newest reading, long before the reset.
		"runs out before the reset": {series(s(0, 0.1), s(12, 0.4)), 12, "", 0.4 + 0.3/12*84, ptr(at(36))},
		"already used up":           {series(s(0, 0.6), s(12, 1)), 12, "", 1 + 0.4/12*84, ptr(at(12))},
		"flat":                      {series(s(0, 0.5), s(12, 0.5)), 12, "", 0.5, nil},
	} {
		p := Project(tc.s, at(tc.now), rules)
		if p.Reason != tc.reason || p.Projected != (tc.reason == "") {
			t.Errorf("%s: reason %q projected %v, want %q", name, p.Reason, p.Projected, tc.reason)
			continue
		}
		if !p.Projected {
			continue
		}
		if math.Abs(p.UsedAtReset-tc.atReset) > 1e-9 {
			t.Errorf("%s: at reset %v, want %v", name, p.UsedAtReset, tc.atReset)
		}
		if (p.RunsOutAt == nil) != (tc.runsOut == nil) || (p.RunsOutAt != nil && !p.RunsOutAt.Equal(*tc.runsOut)) {
			t.Errorf("%s: runs out %v, want %v", name, p.RunsOutAt, tc.runsOut)
		}
		if p.Readings != len(tc.s.Samples) || p.Span != tc.s.Samples[len(tc.s.Samples)-1].At.Sub(tc.s.Samples[0].At) || !p.ResetsAt.Equal(reset) {
			t.Errorf("%s: %+v", name, p)
		}
	}
	// No age limit when MaxAge is zero.
	if p := Project(series(s(0, 0.1), s(12, 0.2)), at(80), Rules{MinSpan: DefaultMinSpan}); !p.Projected {
		t.Fatalf("no age limit: %+v", p)
	}
}

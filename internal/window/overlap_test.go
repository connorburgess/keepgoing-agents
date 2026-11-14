package window

import (
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func TestOverlapCountsOnlyTimeInsideTheWindow(t *testing.T) {
	weekdays := &config.Window{TZ: "UTC", Days: map[string][]string{"Mon": {"09:00", "17:00"}, "Tue": {"09:00", "17:00"}, "Wed": {"22:00", "02:00"}}}
	monday := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC) // a Monday
	for _, tc := range []struct {
		name     string
		w        *config.Window
		from, to time.Time
		want     time.Duration
	}{
		{"no window: all of it", nil, monday, monday.Add(5 * time.Hour), 5 * time.Hour},
		{"inside one span", weekdays, monday.Add(10 * time.Hour), monday.Add(12 * time.Hour), 2 * time.Hour},
		{"overnight between two spans", weekdays, monday.Add(16 * time.Hour), monday.Add(24*time.Hour + 10*time.Hour), 2 * time.Hour},
		{"a span past midnight", weekdays, monday.Add(2*24*time.Hour + 21*time.Hour), monday.Add(3*24*time.Hour + 3*time.Hour), 4 * time.Hour},
		{"a weekend with no span", weekdays, monday.Add(-2 * 24 * time.Hour), monday.Add(9 * time.Hour), 0},
		{"backwards", weekdays, monday.Add(time.Hour), monday, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Overlap(tc.w, tc.from, tc.to)
			if err != nil || got != tc.want {
				t.Fatalf("%s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

// Overlapping spans (one past midnight running into the next day's) are
// counted once, and Overlap agrees with Inside minute by minute, across
// midnight and a daylight-saving change.
func TestOverlapAgreesWithInside(t *testing.T) {
	for _, w := range []*config.Window{
		{TZ: "UTC", Days: map[string][]string{"Mon": {"22:00", "10:00"}, "Tue": {"09:00", "17:00"}}},
		{TZ: "Europe/London", Days: map[string][]string{"Sat": {"23:00", "03:00"}, "Sun": {"01:00", "02:30"}, "Mon": {"09:00", "17:00"}}},
	} {
		// From a Monday, and across the March 2030 clock change in London.
		for _, from := range []time.Time{time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC), time.Date(2030, 3, 30, 12, 0, 0, 0, time.UTC)} {
			to := from.Add(3 * 24 * time.Hour)
			got, err := Overlap(w, from, to)
			if err != nil {
				t.Fatal(err)
			}
			var want time.Duration
			for at := from; at.Before(to); at = at.Add(time.Minute) {
				if in, _, _ := Inside(w, at); in {
					want += time.Minute
				}
			}
			if got != want {
				t.Fatalf("%s from %s: %s, minute by minute %s", w.TZ, from, got, want)
			}
		}
	}
}

// inWallClock is a separate reading of a window, minute by minute: an
// instant is inside when the local clock shows a time inside the day's
// span, or inside the part of yesterday's span that runs past midnight.
// It agrees with edge wherever no span starts or ends in a repeated hour.
func inWallClock(t *testing.T, w *config.Window, at time.Time) bool {
	t.Helper()
	loc, err := time.LoadLocation(w.TZ)
	if err != nil {
		t.Fatal(err)
	}
	lt := at.In(loc)
	hm := lt.Hour()*60 + lt.Minute()
	minutes := func(s string) int {
		h, m, err := clock(s)
		if err != nil {
			t.Fatal(err)
		}
		return h*60 + m
	}
	y, mo, d := lt.Date()
	if span, ok := w.Days[lt.Format("Mon")]; ok {
		start, end := minutes(span[0]), minutes(span[1])
		if (start < end && hm >= start && hm < end) || (end <= start && hm >= start) {
			return true
		}
	}
	yesterday := time.Date(y, mo, d-1, 12, 0, 0, 0, loc)
	if span, ok := w.Days[yesterday.Format("Mon")]; ok {
		start, end := minutes(span[0]), minutes(span[1])
		if end <= start && hm < end {
			return true
		}
	}
	return false
}

// Across the 2030 clock changes in New York and London, Overlap counts
// exactly the minutes at which the local clock shows a time inside the
// window, and so does Inside.
func TestOverlapFollowsTheWallClockAcrossClockChanges(t *testing.T) {
	every := func(tz, from, to string) *config.Window {
		days := map[string][]string{}
		for _, d := range []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"} {
			days[d] = []string{from, to}
		}
		return &config.Window{TZ: tz, Days: days}
	}
	for _, tc := range []struct {
		w       *config.Window
		changes []time.Time
	}{
		{every("America/New_York", "22:00", "06:00"), []time.Time{time.Date(2030, 3, 10, 7, 0, 0, 0, time.UTC), time.Date(2030, 11, 3, 6, 0, 0, 0, time.UTC)}},
		{every("Europe/London", "09:00", "17:00"), []time.Time{time.Date(2030, 3, 31, 1, 0, 0, 0, time.UTC), time.Date(2030, 10, 27, 1, 0, 0, 0, time.UTC)}},
	} {
		for _, change := range tc.changes {
			// Hour by hour, so a span moved by an hour, the same length,
			// does not pass for the right one.
			for from := change.Add(-48 * time.Hour); from.Before(change.Add(48 * time.Hour)); from = from.Add(time.Hour) {
				to := from.Add(time.Hour)
				got, err := Overlap(tc.w, from, to)
				if err != nil {
					t.Fatal(err)
				}
				var wall, inside time.Duration
				for at := from; at.Before(to); at = at.Add(time.Minute) {
					if inWallClock(t, tc.w, at) {
						wall += time.Minute
					}
					if in, _, _ := Inside(tc.w, at); in {
						inside += time.Minute
					}
				}
				if got != wall || inside != wall {
					t.Fatalf("%s, the hour from %s: Overlap %s, Inside %s, wall clock %s", tc.w.TZ, from, got, inside, wall)
				}
			}
		}
	}
}

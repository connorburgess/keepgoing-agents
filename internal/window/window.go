// Package window answers "may the supervisor act right now?" from a weekly
// schedule of per-day time spans.
package window

import (
	"fmt"
	"time"
	// Windows has no system zoneinfo; embed it so configured windows load everywhere.
	_ "time/tzdata"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// Inside reports whether now falls in the configured window. A nil window
// means always. A span whose end time is earlier than its start time (or
// equal to it) runs past midnight; the day key is the day the span starts, so
// yesterday's span is also checked.
//
// Each edge is a local wall-clock time on a calendar date, and stands for the
// first instant on that date at which the local clock shows that time or
// later (see edge). So on a day the clocks change, a span still opens and
// closes when the local clock shows its times, not a fixed number of hours
// after midnight.
func Inside(w *config.Window, now time.Time) (bool, string, error) {
	if w == nil {
		return true, "no window configured", nil
	}
	loc, err := time.LoadLocation(w.TZ)
	if err != nil {
		return false, "", err
	}
	now = now.In(loc)
	y, mo, d := now.Date()
	for _, back := range []int{1, 0} {
		// Noon exists on every date in every zone, so it names the day.
		day := time.Date(y, mo, d-back, 12, 0, 0, 0, loc)
		span, ok := w.Days[day.Format("Mon")]
		if !ok || len(span) != 2 {
			continue
		}
		sh, sm, err := clock(span[0])
		if err != nil {
			return false, "", err
		}
		eh, em, err := clock(span[1])
		if err != nil {
			return false, "", err
		}
		dy, dmo, dd := day.Date()
		endDay := dd
		if eh*60+em <= sh*60+sm {
			endDay++
		}
		start := edge(dy, dmo, dd, sh, sm, loc)
		end := edge(dy, dmo, endDay, eh, em, loc)
		if !now.Before(start) && now.Before(end) {
			return true, fmt.Sprintf("inside %s %s-%s %s", day.Format("Mon"), span[0], span[1], w.TZ), nil
		}
	}
	return false, fmt.Sprintf("outside window (%s %s)", now.Format("Mon 15:04"), w.TZ), nil
}

func clock(hhmm string) (int, int, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return 0, 0, fmt.Errorf("bad time %q: %w", hhmm, err)
	}
	return h, m, nil
}

// edge is the first instant on the given date at which the local clock in
// loc shows h:m or later:
//
//   - an ordinary time is that instant;
//   - a time in a repeated hour (clocks set back) is its first occurrence;
//   - a time in a skipped hour (clocks set forward) is the instant the clock
//     jumps past it, which is also when it first shows a later time.
//
// time.Date leaves the last two unspecified, so they are resolved here. A day
// past the end of the month rolls over, as in time.Date.
func edge(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
	want := time.Date(y, mo, d, h, m, 0, 0, time.UTC) // the wall clock, as if in UTC
	// Every offset the zone uses near that wall time: one, or two around a change.
	offsets := map[int]bool{}
	for k := -26; k <= 26; k++ {
		_, off := want.Add(time.Duration(k) * time.Hour).In(loc).Zone()
		offsets[off] = true
	}
	var first, lo, hi time.Time
	for off := range offsets {
		c := want.Add(-time.Duration(off) * time.Second)
		if lo.IsZero() || c.Before(lo) {
			lo = c
		}
		if hi.IsZero() || c.After(hi) {
			hi = c
		}
		if wall(c.In(loc)).Equal(want) && (first.IsZero() || c.Before(first)) {
			first = c
		}
	}
	if !first.IsZero() {
		return first.In(loc)
	}
	// Skipped: the clock never shows it. Find the first instant between the
	// two readings at which it shows a later time.
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2).Truncate(time.Second)
		if wall(mid.In(loc)).Before(want) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi.In(loc)
}

// wall is t's local clock reading as if it were UTC, for comparing readings.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

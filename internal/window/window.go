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
// means always. A span whose end is earlier than its start runs past
// midnight; the day key is the day the span starts, so yesterday's span is
// also checked.
func Inside(w *config.Window, now time.Time) (bool, string, error) {
	if w == nil {
		return true, "no window configured", nil
	}
	loc, err := time.LoadLocation(w.TZ)
	if err != nil {
		return false, "", err
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for _, day := range []time.Time{today.AddDate(0, 0, -1), today} {
		span, ok := w.Days[day.Format("Mon")]
		if !ok || len(span) != 2 {
			continue
		}
		start, err := at(day, span[0])
		if err != nil {
			return false, "", err
		}
		end, err := at(day, span[1])
		if err != nil {
			return false, "", err
		}
		if !end.After(start) {
			end = end.AddDate(0, 0, 1)
		}
		if !now.Before(start) && now.Before(end) {
			return true, fmt.Sprintf("inside %s %s-%s %s", day.Format("Mon"), span[0], span[1], w.TZ), nil
		}
	}
	return false, fmt.Sprintf("outside window (%s %s)", now.Format("Mon 15:04"), w.TZ), nil
}

func at(day time.Time, hhmm string) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return time.Time{}, fmt.Errorf("bad time %q: %w", hhmm, err)
	}
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), nil
}

// Overlap is how much of [from, to) falls in the configured window. A nil
// window means always, so it is the whole of it. At most maxOverlapDays days
// are looked at; a longer stretch counts only its first maxOverlapDays.
func Overlap(w *config.Window, from, to time.Time) (time.Duration, error) {
	if !to.After(from) {
		return 0, nil
	}
	if w == nil {
		return to.Sub(from), nil
	}
	loc, err := time.LoadLocation(w.TZ)
	if err != nil {
		return 0, err
	}
	from, to = from.In(loc), to.In(loc)
	if limit := from.AddDate(0, 0, maxOverlapDays); to.After(limit) {
		to = limit
	}
	var total time.Duration
	// Yesterday's span may run past midnight into the first day.
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -1)
	for ; day.Before(to); day = day.AddDate(0, 0, 1) {
		span, ok := w.Days[day.Format("Mon")]
		if !ok || len(span) != 2 {
			continue
		}
		start, err := at(day, span[0])
		if err != nil {
			return 0, err
		}
		end, err := at(day, span[1])
		if err != nil {
			return 0, err
		}
		if !end.After(start) {
			end = end.AddDate(0, 0, 1)
		}
		if s, e := maxTime(start, from), minTime(end, to); e.After(s) {
			total += e.Sub(s)
		}
	}
	return total, nil
}

const maxOverlapDays = 400

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

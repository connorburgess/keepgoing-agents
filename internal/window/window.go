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

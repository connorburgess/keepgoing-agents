package state

import (
	"testing"
	"time"
)

// The overdue rule: three times the loop's usual interval, never below 15
// minutes, never for a disabled loop or one that has not yet shown three
// gaps between checks. The
// usual interval is the longest of the recent gaps, or daemon.interval if
// that is longer, so a slow but regular scheduler is never flagged.
func TestScheduleOverdueRule(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	gaps := func(d ...time.Duration) []time.Duration { return d }
	for _, tc := range []struct {
		name      string
		gaps      []time.Duration
		daemon    time.Duration
		since     time.Duration
		enabled   bool
		overdue   bool
		threshold time.Duration
	}{
		{"every 5m, 14m without a check: under the 15m floor", gaps(5*time.Minute, 5*time.Minute, 5*time.Minute), 0, 14 * time.Minute, true, false, 15 * time.Minute},
		{"every 5m, 16m without a check", gaps(5*time.Minute, 5*time.Minute, 5*time.Minute), 0, 16 * time.Minute, true, true, 15 * time.Minute},
		{"every 10m, 29m", gaps(10*time.Minute, 10*time.Minute, 10*time.Minute), 0, 29 * time.Minute, true, false, 30 * time.Minute},
		{"every 10m, 31m", gaps(10*time.Minute, 10*time.Minute, 10*time.Minute), 0, 31 * time.Minute, true, true, 30 * time.Minute},
		{"hourly, 2h59m", gaps(time.Hour, time.Hour, time.Hour), 0, 179 * time.Minute, true, false, 3 * time.Hour},
		{"hourly, 3h10m", gaps(time.Hour, time.Hour, time.Hour), 0, 190 * time.Minute, true, true, 3 * time.Hour},
		{"irregular: the longest gap counts", gaps(5*time.Minute, 40*time.Minute, 10*time.Minute), 0, 100 * time.Minute, true, false, 120 * time.Minute},
		{"daemon.interval longer than any gap", gaps(time.Minute, time.Minute, time.Minute), 20 * time.Minute, 50 * time.Minute, true, false, time.Hour},
		{"daemon.interval longer than any gap, 61m", gaps(time.Minute, time.Minute, time.Minute), 20 * time.Minute, 61 * time.Minute, true, true, time.Hour},
		{"disabled: never", gaps(10*time.Minute, 10*time.Minute, 10*time.Minute), 0, 48 * time.Hour, false, false, 30 * time.Minute},
		{"no gaps yet: not judged", nil, 5 * time.Minute, 16 * time.Minute, true, false, 15 * time.Minute},
		{"two gaps: not judged yet", gaps(time.Hour, time.Hour), 5 * time.Minute, 4 * time.Hour, true, false, 3 * time.Hour},
		{"three gaps, no interval: the floor", gaps(time.Minute, time.Minute, time.Minute), 0, 16 * time.Minute, true, true, 15 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := File{CheckedAt: now.Add(-tc.since), RecentCheckGaps: tc.gaps}
			s := ScheduleOf(f, tc.daemon, tc.enabled, now)
			if s.Overdue != tc.overdue || s.Threshold != tc.threshold || s.For != tc.since {
				t.Fatalf("%+v", s)
			}
		})
	}
	if s := ScheduleOf(File{}, 10*time.Minute, true, now); s.Overdue || !s.LastCheckAt.IsZero() {
		t.Fatalf("a loop that never ran a check: %+v", s)
	}
}

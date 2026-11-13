package state

import "time"

// MinOverdue is the shortest time without a check that can make a loop
// overdue, whatever its interval.
const MinOverdue = 15 * time.Minute

// OverdueFactor times the loop's usual interval is how long it may go
// without a check before its scheduler is taken to have stopped. It is the
// same factor that makes a gap count as missed time.
const OverdueFactor = 3

// CadenceMinGaps is how many gaps between checks a loop must have shown
// before its usual interval is known. Until then no gap is judged, by the
// missed-time rule or the overdue rule: a slow scheduler's first gaps are
// not taken for a stopped one.
const CadenceMinGaps = 3

// Schedule is what the recorded checks say about the loop's scheduler.
type Schedule struct {
	// Overdue: the loop is enabled, has shown CadenceMinGaps gaps between
	// checks, and has gone longer than Threshold without one.
	Overdue bool
	// LastCheckAt is when the last check was recorded; zero if none was.
	LastCheckAt time.Time
	// For is how long ago that was.
	For time.Duration
	// Usual is the loop's usual time between checks: the longest of its last
	// few gaps, or daemon.interval if that is longer. Zero when unknown.
	Usual time.Duration
	// Threshold is OverdueFactor times Usual, and never below MinOverdue.
	Threshold time.Duration
	// StartedBy is the scheduler identity the last check was started with,
	// when one was supplied (tick --routine).
	StartedBy *Routine
}

// ScheduleOf applies the overdue rule. It reads nothing but the record: it
// needs no process of its own, so status, doctor and the panel can all ask.
// A disabled loop is never overdue (its scheduler may have been stopped on
// purpose), and neither is one that has not yet shown its usual interval.
func ScheduleOf(f File, daemonInterval time.Duration, enabled bool, now time.Time) Schedule {
	s := Schedule{LastCheckAt: f.CheckedAt, Usual: daemonInterval, StartedBy: f.StartedBy}
	for _, g := range f.RecentCheckGaps {
		s.Usual = max(s.Usual, g)
	}
	s.Threshold = max(MinOverdue, OverdueFactor*s.Usual)
	if f.CheckedAt.IsZero() {
		return s
	}
	s.For = max(0, now.Sub(f.CheckedAt))
	s.Overdue = enabled && len(f.RecentCheckGaps) >= CadenceMinGaps && s.For > s.Threshold
	return s
}

// Routine is a scheduler identity: source and routine, as given to
// `tick --routine source/routine[/invocation]`.
type Routine struct {
	Source  string `json:"source"`
	Routine string `json:"routine"`
}

// RoutineBBAutomation is the routine source a BB script automation gives, with
// its automation ID as the routine: `--routine bb-automation/auto_...`.
const RoutineBBAutomation = "bb-automation"

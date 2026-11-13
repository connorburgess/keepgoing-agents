package state

import (
	"slices"
	"strings"
	"time"
)

// The overdue rule. A loop is overdue when a check was expected and none has
// run for much longer than usual: its scheduler may have stopped, or the
// computer slept. It is judged from the record alone, so status, doctor and
// the panel can all ask with no process of its own.
const (
	// MinOverdue is the shortest time without a check that can make a loop
	// overdue, whatever its interval.
	MinOverdue = 15 * time.Minute
	// OverdueFactor times the usual interval is how long a loop may go
	// without a check.
	OverdueFactor = 3
	// CadenceMinGaps is how many gaps between checks a loop must have shown
	// before its usual interval is known. Until then the missed-time rule
	// judges no gap, and the overdue rule uses FallbackOverdue.
	CadenceMinGaps = 3
	// FallbackOverdue is how long a loop that has not yet shown its usual
	// interval may go without a check (or three daemon intervals, if longer).
	FallbackOverdue = 24 * time.Hour
	// ScheduleWindow is how many recent gaps the rule keeps.
	ScheduleWindow = 5
)

// ScheduleInput is what the rule needs besides state.json.
type ScheduleInput struct {
	DaemonInterval time.Duration
	// Enabled, and Held: paused, waiting for a person, or declared complete.
	// Neither a disabled nor a held loop is judged: its scheduler may have
	// been stopped on purpose. A requested stop is not a hold while it needs
	// a check to carry it out; once a check has (stopSent in state.json), it
	// is. StopRequested says a stop is requested.
	Enabled, Held, StopRequested bool
	// ControlChangedAt is when control.json last changed. The time without a
	// check is measured from it when it is later than the last check, so
	// resuming or enabling a loop starts the clock again.
	ControlChangedAt time.Time
	// InWindow is how much of [from, to) lies inside the loop's operating
	// window; nil when it has none. Time outside the window does not count.
	InWindow func(from, to time.Time) time.Duration
	// DaemonStartedAt is when a daemon running this loop now started, when
	// that was verified; zero otherwise. A daemon that started before the
	// last check and is still running means the computer slept, unless
	// there is evidence it was awake: a check turned away, or a daemon pass
	// that finished after the last recorded check (DaemonLastPassAt), or,
	// from a daemon that does not say when its last pass ended, a next check
	// it set for after one (DaemonNextCheckAt).
	DaemonStartedAt, DaemonLastPassAt, DaemonNextCheckAt time.Time
	// TurnedAwayAt is the latest check that exited without running because
	// an earlier check still held the tick lock; zero if none is recorded.
	TurnedAwayAt time.Time
}

// Schedule is the rule's verdict.
type Schedule struct {
	Overdue bool
	// NotJudged says why the loop is not judged at all: disabled, held or
	// never_checked. Empty when it is.
	NotJudged string
	// Basis is how it is judged: usual (its usual interval is known) or
	// fallback (fewer than CadenceMinGaps gaps seen).
	Basis    string
	GapsSeen int
	// LastCheckAt is the last recorded check; From is when the time without
	// a check is measured from: the later of it and the last control change.
	LastCheckAt, From time.Time
	// For is the time without a check since From, inside the operating
	// window.
	For time.Duration
	// Usual is the usual interval (Basis usual only); Threshold the time
	// without a check after which the loop is overdue.
	Usual, Threshold time.Duration
	// Slept: the daemon that ran the last check is still running and there
	// is no sign the computer was awake, so the time without a check is the
	// computer sleeping. Never overdue.
	Slept bool
	// NotRecorded: the daemon has finished a pass since the last recorded
	// check: checks are running but not being recorded.
	NotRecorded bool
	// Blocked: checks were started after the last one but found an earlier
	// check still running. The scheduler is running; a check may be hung.
	Blocked bool
	// StartedBy is the identity the last check before this time was
	// started with.
	StartedBy *Routine
}

// Gap is one stretch with no check, as a check_gap report describes it.
type Gap struct {
	From      time.Time     `json:"from"`
	To        time.Time     `json:"to"`
	For       time.Duration `json:"forNs"`
	Usual     time.Duration `json:"usualNs,omitempty"`
	Threshold time.Duration `json:"thresholdNs,omitempty"`
	// Windowed: For counts only time inside the operating window.
	Windowed    bool     `json:"windowed,omitempty"`
	StartedBy   *Routine `json:"startedBy,omitempty"`
	Blocked     bool     `json:"blocked,omitempty"`
	NotRecorded bool     `json:"notRecorded,omitempty"`
}

// ScheduleOf applies the overdue rule.
func ScheduleOf(f File, in ScheduleInput, now time.Time) Schedule {
	s := Schedule{LastCheckAt: f.CheckedAt, From: f.CheckedAt, StartedBy: f.StartedBy, GapsSeen: len(f.ScheduleGaps)}
	// A change stamped later than now is a clock that moved: ignored.
	if in.ControlChangedAt.After(s.From) && !in.ControlChangedAt.After(now) {
		s.From = in.ControlChangedAt
	}
	switch {
	case f.CheckedAt.IsZero():
		s.NotJudged = "never_checked"
		return s
	case !in.Enabled:
		s.NotJudged = "disabled"
		return s
	case in.Held, in.StopRequested && f.StopRequestedSent:
		s.NotJudged = "held"
		return s
	}
	s.For = gapLength(in, s.From, now)
	if s.GapsSeen < CadenceMinGaps {
		s.Basis, s.Threshold = "fallback", max(FallbackOverdue, OverdueFactor*in.DaemonInterval)
	} else {
		s.Basis, s.Usual = "usual", max(in.DaemonInterval, usualGap(f.ScheduleGaps))
		s.Threshold = max(MinOverdue, OverdueFactor*s.Usual)
	}
	// Evidence of being awake is dated against when the last check
	// finished, not when it began: a check turned away while it ran is
	// not one turned away after it. A time after now is a moved clock.
	finished := f.CheckedAt
	if f.CheckFinishedAt.After(finished) {
		finished = f.CheckFinishedAt
	}
	after := func(at time.Time) bool { return at.After(finished.Add(passSlack)) && !at.After(now) }
	s.Blocked = after(in.TurnedAwayAt)
	daemon := !in.DaemonStartedAt.IsZero() && !in.DaemonStartedAt.After(f.CheckedAt)
	if daemon {
		passed := after(in.DaemonLastPassAt)
		if in.DaemonLastPassAt.IsZero() && !in.DaemonNextCheckAt.IsZero() && in.DaemonInterval > 0 {
			passed = after(in.DaemonNextCheckAt.Add(-in.DaemonInterval))
		}
		s.NotRecorded = passed && !s.Blocked
	}
	s.Slept = daemon && !s.Blocked && !s.NotRecorded
	s.Overdue = !s.Slept && s.For > s.Threshold
	return s
}

// NoteScheduleGap records, for the rule, the time from the last check (or a
// later control change) to this one, when it lies wholly inside the
// operating window. It is called by each check before it records itself.
func NoteScheduleGap(f *File, in ScheduleInput, now time.Time) {
	if f.CheckedAt.IsZero() || !now.After(f.CheckedAt) {
		return
	}
	from := f.CheckedAt
	if in.ControlChangedAt.After(from) && in.ControlChangedAt.Before(now) {
		from = in.ControlChangedAt
	}
	// Only a gap wholly inside the operating window says how often checks
	// run. One that crosses time outside it (a night, a weekend) is not
	// recorded: its in-window part is not an interval between checks.
	raw := now.Sub(from)
	if in.InWindow != nil && in.InWindow(from, now) < raw {
		return
	}
	gaps := append(f.ScheduleGaps, raw)
	f.ScheduleGaps = gaps[max(0, len(gaps)-ScheduleWindow):]
}

// passSlack is how long after a check finishes the daemon may still be
// finishing the same pass.
const passSlack = time.Minute

// usualGap is the second longest of the recent gaps. One outage among them
// is the longest and is passed over, so it does not raise the threshold for
// later checks; a scheduler that has really slowed down shows two long gaps
// and is learned at the second; and one that alternates short and long gaps
// is judged by the long ones.
func usualGap(gaps []time.Duration) time.Duration {
	sorted := slices.Clone(gaps)
	slices.Sort(sorted)
	if len(sorted) < 2 {
		return 0
	}
	return sorted[len(sorted)-2]
}

func gapLength(in ScheduleInput, from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	if in.InWindow != nil {
		return in.InWindow(from, to)
	}
	return to.Sub(from)
}

// ShortDuration words a duration the way people say it, to the minute:
// 3h10m, 45m, 2h.
func ShortDuration(d time.Duration) string {
	d = d.Truncate(time.Minute)
	if d < time.Minute {
		return "under a minute"
	}
	out := d.String()
	for _, z := range []string{"m0s", "h0m"} {
		if strings.HasSuffix(out, z) {
			out = strings.TrimSuffix(out, z[1:])
		}
	}
	return out
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

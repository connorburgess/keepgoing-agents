package state

import (
	"testing"
	"time"
)

const (
	m = time.Minute
	h = time.Hour
)

func gaps(d ...time.Duration) []time.Duration { return d }

// The overdue rule, case by case. The usual interval is the second longest
// of the last five gaps, or daemon.interval if that is longer; a loop is
// overdue after three of them (never less than 15 minutes), or, before it
// has shown three gaps, after a day.
func TestScheduleOverdueRule(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	run := ScheduleInput{Enabled: true}
	for _, tc := range []struct {
		name      string
		gaps      []time.Duration
		in        ScheduleInput
		since     time.Duration
		overdue   bool
		threshold time.Duration
		basis     string
	}{
		{"every 5m, 14m: under the 15m floor", gaps(5*m, 5*m, 5*m), run, 14 * m, false, 15 * m, "usual"},
		{"every 5m, 16m", gaps(5*m, 5*m, 5*m), run, 16 * m, true, 15 * m, "usual"},
		{"every 10m, 29m", gaps(10*m, 10*m, 10*m), run, 29 * m, false, 30 * m, "usual"},
		{"every 10m, 31m", gaps(10*m, 10*m, 10*m), run, 31 * m, true, 30 * m, "usual"},
		{"hourly, 2h59m", gaps(h, h, h), run, 179 * m, false, 3 * h, "usual"},
		{"hourly, 3h10m", gaps(h, h, h), run, 190 * m, true, 3 * h, "usual"},
		// One outage is the longest gap and is passed over.
		{"one outage does not raise the threshold", gaps(10*m, 10*m, 4*h, 10*m, 10*m), run, 31 * m, true, 30 * m, "usual"},
		// A slowdown shows two long gaps and is learned at the second.
		{"10m to hourly, after one hourly gap", gaps(10*m, 10*m, 10*m, 10*m, h), run, 50 * m, true, 30 * m, "usual"},
		{"10m to hourly, after two", gaps(10*m, 10*m, 10*m, h, h), run, 2 * h, false, 3 * h, "usual"},
		{"10m to daily, after two", gaps(10*m, 10*m, 10*m, 24*h, 24*h), run, 48 * h, false, 72 * h, "usual"},
		// Alternating short and long gaps are judged by the long ones.
		{"5m and 60m alternating", gaps(5*m, 60*m, 5*m, 60*m, 5*m), run, 70 * m, false, 3 * h, "usual"},
		{"5m and 60m alternating, other phase", gaps(60*m, 5*m, 60*m, 5*m, 60*m), run, 70 * m, false, 3 * h, "usual"},
		{"daemon.interval longer than the gaps", gaps(m, m, m), ScheduleInput{Enabled: true, DaemonInterval: 20 * m}, 50 * m, false, h, "usual"},
		// Fewer than three gaps: the long fallback.
		{"two gaps, 23h", gaps(h, h), run, 23 * h, false, 24 * h, "fallback"},
		{"two gaps, 25h", gaps(h, h), run, 25 * h, true, 24 * h, "fallback"},
		{"no gaps, daemon.interval 10h: three of them", nil, ScheduleInput{Enabled: true, DaemonInterval: 10 * h}, 29 * h, false, 30 * h, "fallback"},
		// The daemon that ran the last check is still running: the computer slept.
		{"slept", gaps(10*m, 10*m, 10*m), ScheduleInput{Enabled: true, DaemonStartedAt: now.Add(-48 * h)}, 4 * h, false, 30 * m, "usual"},
		{"a daemon started since: not sleep", gaps(10*m, 10*m, 10*m), ScheduleInput{Enabled: true, DaemonStartedAt: now.Add(-time.Minute)}, 4 * h, true, 30 * m, "usual"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := File{CheckedAt: now.Add(-tc.since), ScheduleGaps: tc.gaps}
			s := ScheduleOf(f, tc.in, now)
			if s.Overdue != tc.overdue || s.Threshold != tc.threshold || s.For != tc.since || s.Basis != tc.basis || s.NotJudged != "" {
				t.Fatalf("%+v", s)
			}
		})
	}
}

// A loop that is disabled, held or has never run a check is not judged; a
// requested stop is not a hold. The time is measured from a control change
// later than the last check, and only inside the operating window.
func TestScheduleHoldsWindowAndControlChanges(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	f := File{CheckedAt: now.Add(-3 * 24 * h), ScheduleGaps: gaps(10*m, 10*m, 10*m)}
	for _, tc := range []struct {
		name string
		in   ScheduleInput
		why  string
	}{
		{"disabled", ScheduleInput{}, "disabled"},
		{"paused, waiting for a person or completed", ScheduleInput{Enabled: true, Held: true}, "held"},
	} {
		if s := ScheduleOf(f, tc.in, now); s.Overdue || s.NotJudged != tc.why {
			t.Fatalf("%s: %+v", tc.name, s)
		}
	}
	if s := ScheduleOf(File{}, ScheduleInput{Enabled: true}, now); s.Overdue || s.NotJudged != "never_checked" {
		t.Fatalf("never checked: %+v", s)
	}
	// Resumed or enabled 20 minutes ago after three days held: 20 minutes.
	if s := ScheduleOf(f, ScheduleInput{Enabled: true, ControlChangedAt: now.Add(-20 * m)}, now); s.Overdue || s.For != 20*m {
		t.Fatalf("resumed: %+v", s)
	}
	if s := ScheduleOf(f, ScheduleInput{Enabled: true, ControlChangedAt: now.Add(-40 * m)}, now); !s.Overdue || s.For != 40*m {
		t.Fatalf("resumed 40m ago: %+v", s)
	}
	// Only time inside the window counts: here, none of it.
	closed := func(from, to time.Time) time.Duration { return 0 }
	if s := ScheduleOf(f, ScheduleInput{Enabled: true, InWindow: closed}, now); s.Overdue || s.For != 0 {
		t.Fatalf("outside the window: %+v", s)
	}
	// Checks turned away by a running one: still overdue, but blocked.
	if s := ScheduleOf(f, ScheduleInput{Enabled: true, TurnedAwayAt: now.Add(-m)}, now); !s.Overdue || !s.Blocked {
		t.Fatalf("blocked: %+v", s)
	}
}

// The rule's own record keeps raw gaps, measured from a later control change
// and inside the window, the last ScheduleWindow of them.
func TestNoteScheduleGap(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	f := File{CheckedAt: now.Add(-4 * h), ScheduleGaps: gaps(m, 2*m, 3*m, 4*m, 5*m)}
	NoteScheduleGap(&f, ScheduleInput{}, now)
	if len(f.ScheduleGaps) != ScheduleWindow || f.ScheduleGaps[0] != 2*m || f.ScheduleGaps[4] != 4*h {
		t.Fatalf("%v", f.ScheduleGaps)
	}
	f = File{CheckedAt: now.Add(-4 * h)}
	NoteScheduleGap(&f, ScheduleInput{ControlChangedAt: now.Add(-10 * m)}, now)
	if f.ScheduleGaps[0] != 10*m {
		t.Fatalf("%v", f.ScheduleGaps)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{3*h + 10*m + 20*time.Second: "3h10m", 3 * h: "3h", 45 * m: "45m", 71*h + 30*m: "71h30m", 30 * time.Second: "under a minute"} {
		if got := ShortDuration(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

func TestTurnedAwayRecord(t *testing.T) {
	dir := t.TempDir()
	if !TurnedAwayAt(dir).IsZero() {
		t.Fatal("a record without a file")
	}
	at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := NoteTurnedAway(dir, at); err != nil || !TurnedAwayAt(dir).Equal(at) {
		t.Fatalf("%v %v", err, TurnedAwayAt(dir))
	}
}

// A control change stamped after now is a clock that moved, not a resume.
func TestScheduleIgnoresAControlChangeInTheFuture(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	f := File{CheckedAt: now.Add(-4 * h), ScheduleGaps: gaps(10*m, 10*m, 10*m)}
	if s := ScheduleOf(f, ScheduleInput{Enabled: true, ControlChangedAt: now.Add(time.Hour)}, now); !s.Overdue || s.For != 4*h {
		t.Fatalf("%+v", s)
	}
}

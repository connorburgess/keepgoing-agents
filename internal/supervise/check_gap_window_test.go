package supervise

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// weekWithWindow runs the overdue rule as the checks and status would, over
// a week of a loop whose operating window is Monday to Friday 09:00-17:00
// UTC, with daemon.interval 5m. checksAt says whether a check runs at a
// minute of the week. It returns every check that would send check_gap, and
// the first and last minute status would call the loop overdue.
func weekWithWindow(t *testing.T, checksAt func(at time.Time) bool) (gaps []string, overdue []string) {
	t.Helper()
	loop := config.Loop{StateDir: t.TempDir(), Daemon: config.Daemon{Interval: config.Duration{Duration: 5 * time.Minute}},
		Window: &config.Window{TZ: "UTC", Days: map[string][]string{"Mon": {"09:00", "17:00"}, "Tue": {"09:00", "17:00"}, "Wed": {"09:00", "17:00"}, "Thu": {"09:00", "17:00"}, "Fri": {"09:00", "17:00"}}}}
	in := ScheduleInput(&loop, control.File{Enabled: true}, DaemonInfo{})
	monday := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC)
	var f state.File
	for at := monday; at.Before(monday.AddDate(0, 0, 14)); at = at.Add(5 * time.Minute) {
		if f.CheckedAt.IsZero() && !checksAt(at) {
			continue
		}
		// The second week only: by then the loop has shown its interval.
		late := at.After(monday.AddDate(0, 0, 7))
		if s := state.ScheduleOf(f, in, at); s.Overdue && late {
			overdue = append(overdue, at.Format("Mon 15:04"))
		}
		if checksAt(at) {
			if s := state.ScheduleOf(f, in, at); s.Overdue && late {
				gaps = append(gaps, at.Format("Mon 15:04"))
			}
			state.NoteScheduleGap(&f, in, at)
			f.CheckedAt = at
		}
	}
	return gaps, overdue
}

func every24h(interval time.Duration) func(time.Time) bool {
	return func(at time.Time) bool { return at.Sub(at.Truncate(24*time.Hour))%interval == 0 }
}

// A scheduler that keeps running at night and at the weekend, every 30
// minutes, every hour or every 10 minutes, is never called overdue when the
// window opens: gaps that cross time outside the window are not recorded,
// so they do not shrink the loop's usual interval.
func TestWindowedLoopIsNotOverdueEachMorning(t *testing.T) {
	for _, every := range []time.Duration{30 * time.Minute, time.Hour, 10 * time.Minute} {
		t.Run(every.String(), func(t *testing.T) {
			if gaps, overdue := weekWithWindow(t, every24h(every)); len(gaps)+len(overdue) != 0 {
				t.Fatalf("check_gap at %v; overdue at %v", gaps, overdue)
			}
		})
	}
}

// A scheduler that runs only in working hours, its first check of the day
// 20 minutes after the window opens, every 10 minutes after that.
func TestWindowedLoopWithALateFirstCheckIsNotOverdue(t *testing.T) {
	checks := func(at time.Time) bool {
		if wd := at.Weekday(); wd == time.Saturday || wd == time.Sunday {
			return false
		}
		minute := at.Hour()*60 + at.Minute()
		return minute >= 9*60+20 && minute < 17*60 && at.Minute()%10 == 0
	}
	if gaps, overdue := weekWithWindow(t, checks); len(gaps)+len(overdue) != 0 {
		t.Fatalf("check_gap at %v; overdue at %v", gaps, overdue)
	}
}

// A requested stop that a check has carried out is a hold: the loop is not
// judged after it, however long its scheduler is gone. One not yet carried
// out still is, because it needs a check.
func TestACarriedOutStopIsAHold(t *testing.T) {
	now := t0.Add(48 * time.Hour)
	f := state.File{CheckedAt: t0, ScheduleGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}}
	in := state.ScheduleInput{Enabled: true, StopRequested: true}
	if s := state.ScheduleOf(f, in, now); !s.Overdue {
		t.Fatalf("a stop not yet carried out: %+v", s)
	}
	f.StopRequestedSent = true
	if s := state.ScheduleOf(f, in, now); s.Overdue || s.NotJudged != "held" {
		t.Fatalf("a carried-out stop: %+v", s)
	}
}

// Evidence that the computer was awake is dated against when the last check
// finished. A check turned away while the last one ran does not make a hung
// check; one turned away after it does, and so does one under a daemon,
// which is then not taken for sleep. A daemon pass that ended after the last
// recorded check means checks are running but not recorded. A time in the
// future is a moved clock and is ignored.
func TestEvidenceOfBeingAwake(t *testing.T) {
	now := t0.Add(4 * time.Hour)
	f := state.File{CheckedAt: t0, CheckFinishedAt: t0.Add(2 * time.Minute), ScheduleGaps: []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute}}
	daemon := t0.Add(-time.Hour)
	for _, tc := range []struct {
		name                        string
		in                          state.ScheduleInput
		slept, blocked, notRecorded bool
	}{
		{"turned away while the last check ran", state.ScheduleInput{TurnedAwayAt: t0.Add(time.Minute)}, false, false, false},
		{"turned away after it", state.ScheduleInput{TurnedAwayAt: t0.Add(time.Hour)}, false, true, false},
		{"turned away in the future", state.ScheduleInput{TurnedAwayAt: now.Add(time.Hour)}, false, false, false},
		{"the same daemon, no pass since: slept", state.ScheduleInput{DaemonStartedAt: daemon, DaemonLastPassAt: t0.Add(2*time.Minute + time.Second)}, true, false, false},
		{"the same daemon, turned away since", state.ScheduleInput{DaemonStartedAt: daemon, TurnedAwayAt: t0.Add(time.Hour)}, false, true, false},
		{"the same daemon, a pass since that did not record", state.ScheduleInput{DaemonStartedAt: daemon, DaemonLastPassAt: t0.Add(3 * time.Hour)}, false, false, true},
		{"an older daemon whose next check was set after a pass", state.ScheduleInput{DaemonStartedAt: daemon, DaemonInterval: 10 * time.Minute, DaemonNextCheckAt: t0.Add(3*time.Hour + 10*time.Minute)}, false, false, true},
		{"an older daemon whose next check passed in its sleep", state.ScheduleInput{DaemonStartedAt: daemon, DaemonInterval: 10 * time.Minute, DaemonNextCheckAt: t0.Add(12 * time.Minute)}, true, false, false},
	} {
		in := tc.in
		in.Enabled = true
		s := state.ScheduleOf(f, in, now)
		if s.Slept != tc.slept || s.Blocked != tc.blocked || s.NotRecorded != tc.notRecorded || s.Overdue == tc.slept {
			t.Errorf("%s: %+v", tc.name, s)
		}
	}
}

// The check_gap text says what its duration counts, names the threshold a
// loop without a usual interval is held to, and says when the daemon's
// checks were not being recorded.
func TestCheckGapText(t *testing.T) {
	text := func(t *testing.T, configure func(w *world, opts *Options)) string {
		t.Helper()
		w := newWorld(t, true, false)
		subscribe(w, true)
		w.primary(decide.Observation{State: decide.Active})
		lastCheckBeforeAGap(t, w, t0, nil)
		opts := w.opts(t0.Add(72 * time.Hour))
		configure(w, &opts)
		if _, err := Tick(t.Context(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
		for _, r := range w.reports {
			if r.Type == "check_gap" {
				return r.Text
			}
		}
		t.Fatalf("no check_gap: %s", w.reportTypes())
		return ""
	}
	t.Run("before three gaps: the real threshold", func(t *testing.T) {
		got := text(t, func(w *world, _ *Options) {
			w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Hour}
			st, _ := state.Load(w.dir)
			st.ScheduleGaps = nil
			state.Save(w.dir, st)
		})
		if !strings.Contains(got, "longer than the 30h a loop may go without a check") {
			t.Fatalf("%q", got)
		}
	})
	t.Run("in a window: of its working hours", func(t *testing.T) {
		got := text(t, func(w *world, _ *Options) {
			w.cfg.Loop.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Mon": {"00:00", "23:59"}, "Tue": {"00:00", "23:59"}, "Wed": {"00:00", "23:59"}, "Thu": {"00:00", "23:59"}, "Fri": {"00:00", "23:59"}, "Sat": {"00:00", "23:59"}, "Sun": {"00:00", "23:59"}}}
		})
		if !strings.Contains(got, " of its working hours, where checks normally run every 10m") {
			t.Fatalf("%q", got)
		}
	})
	t.Run("a daemon whose passes were not recorded", func(t *testing.T) {
		got := text(t, func(_ *world, opts *Options) {
			opts.Daemon = DaemonInfo{StartedAt: t0.Add(-time.Hour), LastPassAt: t0.Add(71 * time.Hour)}
		})
		if !strings.Contains(got, "the daemon was running checks, but they were not being recorded") || strings.Contains(got, "slept") {
			t.Fatalf("%q", got)
		}
	})
}

// A check that fails after a gap sends its error report first, so check_gap
// cannot take a late-report slot the error report needs.
func TestErrorReportGoesBeforeCheckGap(t *testing.T) {
	w := newWorld(t, true, false)
	subscribe(w, true, "error")
	p := w.primary(decide.Observation{State: decide.Active})
	p.obsErr = errors.New("example observe failure")
	lastCheckBeforeAGap(t, w, t0, nil)
	Tick(t.Context(), w.cfg, w.opts(t0.Add(4*time.Hour)))
	if got := w.reportTypes(); got != "error,check_gap" {
		t.Fatalf("reports %s", got)
	}
	_ = fmt.Sprint
}

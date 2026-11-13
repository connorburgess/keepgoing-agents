package supervise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The first check after the loop went overdue sends one check_gap report,
// keyed on the last check before the gap; the next checks do not repeat it.
// A later gap gets its own. A gap within the rule, or on a disabled loop,
// sends none. A BB automation named by the last check is named in the text.
func TestCheckGapIsReportedOncePerGap(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Minute}
	w.cfg.Loop.Report = &config.Report{Events: []string{"check_gap"}}
	w.primary(decide.Observation{State: decide.Active})
	tick := func(at time.Duration, routine ...rundiscovery.RoutineRef) {
		t.Helper()
		opts := w.opts(t0.Add(at))
		if len(routine) > 0 {
			opts.Routine = routine[0]
		}
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
	}
	bbAuto := rundiscovery.RoutineRef{Source: state.RoutineBBAutomation, Routine: "auto_example1"}
	for m := 0; m <= 30; m += 10 {
		tick(time.Duration(m)*time.Minute, bbAuto)
	}
	if strings.Contains(w.reportTypes(), "check_gap") {
		t.Fatalf("reported without a gap: %s", w.reportTypes())
	}
	// The automation stopped for three hours.
	tick(30*time.Minute+3*time.Hour, bbAuto)
	tick(30*time.Minute+3*time.Hour+10*time.Minute, bbAuto)
	if n := strings.Count(w.reportTypes(), "check_gap"); n != 1 {
		t.Fatalf("%d check_gap reports for one gap", n)
	}
	e := w.reports[len(w.reports)-1]
	for _, r := range w.reports {
		if r.Type == "check_gap" {
			e = r
		}
	}
	if !strings.Contains(e.Text, "no check ran from") || !strings.Contains(e.Text, "auto_example1") {
		t.Fatalf("%q", e.Text)
	}
	// A second gap is a new one.
	tick(30*time.Minute+8*time.Hour, bbAuto)
	if n := strings.Count(w.reportTypes(), "check_gap"); n != 2 {
		t.Fatalf("%d check_gap reports for two gaps", n)
	}
	if st, _ := state.Load(w.dir); st.StartedBy == nil || st.StartedBy.Routine != "auto_example1" {
		t.Fatalf("%+v", st.StartedBy)
	}
}

// A disabled loop's scheduler may have been stopped on purpose: no report.
func TestCheckGapIsNotReportedForADisabledLoop(t *testing.T) {
	w := newWorld(t, false, false)
	w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Minute}
	w.cfg.Loop.Report = &config.Report{Events: []string{"check_gap"}}
	w.primary(decide.Observation{State: decide.Active})
	for _, at := range []time.Duration{0, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 5 * time.Hour} {
		Tick(context.Background(), w.cfg, w.opts(t0.Add(at)))
	}
	if strings.Contains(w.reportTypes(), "check_gap") {
		t.Fatalf("%s", w.reportTypes())
	}
}

// A loop that does not subscribe to check_gap records no send for it.
func TestCheckGapIsNotRecordedWhenNotSubscribed(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Minute}
	w.cfg.Loop.Report = &config.Report{Events: []string{"error"}}
	w.primary(decide.Observation{State: decide.Active})
	for _, at := range []time.Duration{0, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 5 * time.Hour} {
		if _, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(at))); err != nil {
			t.Fatal(err)
		}
	}
	l, err := state.LoadLedger(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range l.Pending {
		if p.Action == "report:check_gap" {
			t.Fatalf("recorded %+v", p)
		}
	}
	if st, _ := state.Load(w.dir); !st.CheckGapReported.IsZero() {
		t.Fatalf("marked reported: %v", st.CheckGapReported)
	}
}

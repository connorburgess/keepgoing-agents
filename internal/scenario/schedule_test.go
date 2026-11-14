package scenario

import (
	"strings"
	"testing"
	"time"
)

// TestScenarioSchedulerStops: a BB automation runs the loop's checks every
// ten minutes, identifying itself with --routine, and then stops (as one
// does after repeated failures). While no check runs, status says the
// scheduler may have stopped and names the automation. When checks run
// again, the first one reports the gap once; the next one does not.
func TestScenarioSchedulerStops(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("check_gap")
	w.Control("enable")
	automation := "bb-automation/auto_example1"
	tick := func(label string) *Step {
		r := w.Run("tick", "-c", w.Config, "--now", w.stamp(), "--routine", automation)
		return w.record(label, r, nil)
	}
	for i := 0; i < 4; i++ {
		w.Advance(10 * time.Minute)
		s := tick("an automation check")
		w.Check(s, "no gap reported while checks run", noReports(s))
	}
	// The automation stops. Three hours later nobody has looked.
	w.Advance(3*time.Hour + 10*time.Minute)
	st := w.Run("status", "-c", w.Config, "--now", w.stamp())
	s := w.record("status while no check runs", st, nil)
	w.Check(s, "status says the scheduler may have stopped and names the automation", all(
		exit(s, 0),
		check(strings.Contains(st.Stdout, "schedule:  no check has run for 3h10m"), "status: %s", st.Stdout),
		check(strings.Contains(st.Stdout, "the BB automation that runs this loop (auto_example1) may have disabled itself"), "status: %s", st.Stdout),
	))
	// Someone turns it back on.
	s = tick("the first check after the gap")
	w.Check(s, "the gap is reported once", reported(s, "keepgoing.check_gap"))
	w.Advance(10 * time.Minute)
	s = tick("the next check")
	w.Check(s, "and not again", noReports(s))
	st = w.Run("status", "-c", w.Config, "--now", w.stamp())
	s = w.record("status once checks run", st, nil)
	w.Check(s, "status no longer says it", check(!strings.Contains(st.Stdout, "schedule:"), "status: %s", st.Stdout))
}

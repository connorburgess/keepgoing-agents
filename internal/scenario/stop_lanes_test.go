package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	laneActive  = "thr_example_lane_a"
	laneIdle    = "thr_example_lane_b"
	laneParent  = "thr_example_lane_c"
	laneGrandch = "thr_example_lane_c1"
)

// stopsIn lists the threads a step stopped.
func stopsIn(s *Step) []string {
	var out []string
	for _, c := range s.Calls {
		if len(c) >= 3 && c[0] == "thread" && c[1] == "stop" {
			out = append(out, c[2])
		}
	}
	return out
}

// TestScenarioStopLanesNow: a loop whose working session has three lanes,
// one active, one idle, and one idle with an active child of its own. A
// person runs `keepgoing stop --lanes --now`. The command itself stops the
// session and both active threads, leaves the idle ones alone, sends no
// message, and sends one stop report. Later checks stop nothing more.
func TestScenarioStopLanesNow(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("await_user", "complete", "stop")
	primary := w.Thread(primaryThread)
	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake", action(s, "wake"))

	primary.Child(laneActive).StartTurn()
	primary.Child(laneIdle).Set("idle")
	parent := primary.Child(laneParent)
	parent.Set("idle")
	parent.Child(laneGrandch).StartTurn()

	w.Advance(time.Minute)
	s = w.Control("stop", "--lanes", "--now")
	w.Check(s, "the command carried out the stop", all(exit(s, 0), nothingSent(s), reported(s, "keepgoing.stop")))
	if !strings.Contains(s.Result.Stdout, "stop carried out now: stop") || !strings.Contains(s.Result.Stdout, "lanes: 2 stopped, 2 already idle, 0 left to another loop, 0 failed, 0 not checked") {
		w.Failf(s, "output:\n%s", s.Result.Stdout)
	}
	if got := strings.Join(stopsIn(s), ","); got != primaryThread+","+laneActive+","+laneGrandch {
		w.Failf(s, "stopped %s", got)
	}
	for _, c := range s.Calls {
		if c[0] == "thread" && (c[1] == "tell" || c[1] == "retry" || c[1] == "spawn" || c[1] == "update") {
			w.Failf(s, "the stop ran bb %v", c)
		}
	}
	if r := s.Reports[0].Body; !strings.Contains(fmt.Sprint(r), "stopped:2") {
		w.Failf(s, "report %v", r)
	}

	// The stop is done. Later checks keep the loop stopped, stop nothing more
	// and report nothing.
	for i := 0; i < 2; i++ {
		w.Advance(10 * time.Minute)
		s = w.Tick()
		w.Check(s, "stopped", all(nothingSent(s), noReports(s)))
		if got := stopsIn(s); len(got) != 0 {
			w.Failf(s, "stopped %v again", got)
		}
	}
}

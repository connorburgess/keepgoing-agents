package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	childA = "thr_example_child_a"
	childB = "thr_example_child_b"
	childC = "thr_example_child_c"
)

// TestScenarioChildFailureNoticeAndReport follows a conversation that hands
// parts of its work to child conversations, with lane recovery off (the
// default): keepgoing never touches a child. When a child fails the owner
// gets one child_error report at once, and the conversation that owns the
// child is told once, with a count and nothing else: on its own while other
// children are still working, or at the end of its next ordinary wake.
// A child that fails again soon after recovering says nothing new; one that
// stayed recovered for over an hour is reported again; a failure during a
// pause is reported when the loop is resumed.
func TestScenarioChildFailureNoticeAndReport(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("child_error", "await_user", "complete")
	primary := w.Thread(primaryThread)
	// Children are never sent anything, at any step of the journey.
	untouched := func(s *Step) {
		t.Helper()
		for _, id := range []string{childA, childB, childC} {
			w.Check(s, "children are never sent anything", nothingSentTo(s, id))
		}
	}
	tick := func() *Step {
		t.Helper()
		s := w.Tick()
		untouched(s)
		return s
	}

	w.Control("enable")
	w.Advance(time.Minute)
	s := tick()
	w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}), childErrors(s, 0)))

	// The conversation starts two children and ends its own turn to wait.
	a, b := primary.Child(childA), primary.Child(childB)
	a.StartTurn()
	b.StartTurn()
	primary.FinishTurn()
	w.Advance(10 * time.Minute)
	s = tick()
	w.Check(s, "waiting on children", all(action(s, "none"), reasonIs(s, "child work running"), nothingSent(s), noReports(s), childErrors(s, 0)))

	// Child A fails while B is still working. The owner hears at once; the
	// waiting conversation is not woken yet, because it was sent something
	// less than min_wake_interval (an hour) ago.
	w.Advance(10 * time.Minute)
	a.NetworkError(false)
	w.Advance(time.Minute)
	s = tick()
	w.Check(s, "first failure is reported at once", all(
		action(s, "none"), nothingSent(s), reported(s, "keepgoing.child_error"), holds(s), childErrors(s, 1),
		childReport(s, "reported", 1),
	))
	if len(s.Reports) == 1 {
		w.Check(s, "the report carries counts and no child identity", reportIsCountOnly(s.Reports[0], 1, 1))
	}
	w.Advance(10 * time.Minute)
	s = tick()
	w.Check(s, "nothing is repeated", all(action(s, "none"), nothingSent(s), noReports(s), childErrors(s, 1)))

	// An hour after the last send the waiting conversation is told, once,
	// with the notice alone: a count, no task, no child identity.
	w.At(scenarioStart.Add(time.Minute + time.Hour + time.Minute))
	s = tick()
	w.Check(s, "notice-only wake", all(
		action(s, "wake"), reasonIs(s, "children in error while other children run"),
		sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}), noReports(s), holds(s),
	))
	if len(s.Sent) == 1 {
		m := s.Sent[0].Message
		w.Check(s, "the notice is a count and nothing else", all(
			check(strings.HasPrefix(m, "[keepgoing:child-errors count=1 running=1]"), "the message starts %q", firstLine(m)),
			check(!strings.Contains(m, scenarioTask), "the notice carries the task prompt"),
			check(!strings.Contains(m, childA) && !strings.Contains(m, childB), "the notice names a child"),
		))
	}
	// The conversation acts on the notice: it retries A itself, outside
	// keepgoing, and goes back to waiting. It is then left alone.
	a.StartTurn()
	primary.FinishTurn()
	w.Advance(30 * time.Minute)
	s = tick()
	w.Check(s, "left alone again", all(action(s, "none"), reasonIs(s, "child work running"), nothingSent(s), noReports(s), childErrors(s, 0)))

	// A fails again 38 minutes after it left error. That is the same
	// failure: no report, and no second notice to the waiting conversation.
	w.Advance(8 * time.Minute)
	a.NetworkError(false)
	w.Advance(2 * time.Minute)
	s = tick()
	w.Check(s, "a quick re-failure says nothing new", all(action(s, "none"), nothingSent(s), noReports(s), childErrors(s, 1), childReport(s, "reported", 1)))
	w.At(scenarioStart.Add(2*time.Hour + 10*time.Minute)) // past the wake spacing
	s = tick()
	w.Check(s, "and causes no second notice", all(action(s, "none"), nothingSent(s), noReports(s)))

	// BB retries A by itself, and both children finish. With every child
	// done and none in error, the next check is an ordinary wake with the
	// task and no notice.
	a.StartTurn()
	recovered := w.Now
	w.Advance(10 * time.Minute)
	a.FinishTurn()
	b.FinishTurn()
	w.Commit("feat: fictional work from the children")
	w.Advance(5 * time.Minute)
	s = tick()
	w.Check(s, "ordinary wake after the children finish", all(
		action(s, "wake"), reasonIs(s, "idle"), sentContains(s, scenarioTask), noNotice(s), noReports(s), childErrors(s, 0),
	))

	// A fails once more, 80 minutes after it recovered. A child that stayed
	// recovered for over an hour is a new failure: a new report (the first of
	// a new episode), and the notice at the end of the next ordinary wake.
	primary.FinishTurn()
	w.At(recovered.Add(80 * time.Minute))
	a.NetworkError(false)
	w.Advance(time.Minute)
	s = tick()
	w.Check(s, "a failure after a long recovery is reported again", all(
		action(s, "wake"), reported(s, "keepgoing.child_error"), childErrors(s, 1), holds(s),
		sentContains(s, scenarioTask), sentContains(s, "[keepgoing:child-errors count=1]"),
	))
	// No check ran while A was healthy, as when the host sleeps. The failure
	// is reported as new, but numbered inside the episode that should have
	// ended, so it also uses up that episode's cap of three reports.
	w.Known(s, Waits(WaitChildEpisode), "a failure after a long recovery starts a new episode: report 1", childReport(s, "reported", 1))

	// The loop is paused. A third child fails during the pause: nothing is
	// observed, sent or reported while the pause holds.
	primary.FinishTurn()
	s = w.Control("pause")
	untouched(s)
	c := primary.Child(childC)
	c.NetworkError(false)
	for i := 0; i < 2; i++ {
		w.Advance(30 * time.Minute)
		s = tick()
		w.Check(s, "paused", all(action(s, "none"), nothingSent(s), noCalls(s), noReports(s), holds(s, "paused")))
	}

	// Resumed: the first check that observes the children reports the new
	// failure (the second report of the episode), and the ordinary wake that
	// check sends ends with the notice, after the task.
	s = w.Control("resume")
	untouched(s)
	w.Advance(time.Minute)
	s = tick()
	w.Check(s, "failure during the pause is reported on resume", all(
		action(s, "wake"), reported(s, "keepgoing.child_error"), childErrors(s, 2), holds(s),
		sentContains(s, scenarioTask), sentContains(s, "[keepgoing:child-errors count=2]"),
	))
	w.Known(s, Waits(WaitChildEpisode), "the failure during the pause is the new episode's second report", childReport(s, "reported", 2))
	if len(s.Sent) == 1 {
		m := s.Sent[0].Message
		w.Check(s, "the notice follows the task and names no child", all(
			check(strings.Index(m, scenarioTask) < strings.Index(m, "[keepgoing:child-errors"), "the notice comes before the task"),
			check(!strings.Contains(m, childA) && !strings.Contains(m, childC), "the notice names a child"),
		))
	}

	// Nothing more is said about the same children, and they are still in
	// error: keepgoing has not retried, stopped or messaged them.
	primary.FinishTurn()
	w.Advance(10 * time.Minute)
	s = tick()
	w.Check(s, "announced once", all(action(s, "wake"), noNotice(s), noReports(s), childErrors(s, 2)))
	w.Check(s, "children left as they were", all(
		check(a.Status() == "error" && c.Status() == "error", "a child's status changed: %s %s", a.Status(), c.Status()),
		noChildCommands(w),
	))

	var reports []string
	for _, st := range w.Journal {
		reports = append(reports, reportTypes(st.Reports)...)
	}
	w.Check(s, "three reports in the whole journey", check(fmt.Sprint(reports) == "[keepgoing.child_error keepgoing.child_error keepgoing.child_error]", "reported %v", reports))
}

// WaitChildEpisode: a child that recovered for over an hour and fails again
// is a new failure, and the documentation says the earlier episode has then
// ended. When no check ran while the child was healthy, the new failure is
// reported but numbered inside the old episode and counted against its cap
// of three reports. Reported from this scenario; not assigned yet.
const WaitChildEpisode Waiting = "child report episode when checks were missed, unassigned"

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// childErrors compares the count of children in error the check recorded, in
// the tick line and in status.
func childErrors(s *Step, want int) error {
	got, _ := s.Tick["erroredChildren"].(float64)
	var st struct {
		State struct {
			LastCheck struct {
				ErroredChildren int `json:"erroredChildren"`
			} `json:"lastCheck"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(s.Status.Raw), &st); err != nil {
		return err
	}
	return check(int(got) == want && st.State.LastCheck.ErroredChildren == want,
		"children in error: tick %d, status %d, want %d", int(got), st.State.LastCheck.ErroredChildren, want)
}

// childReport compares what status says about the owner report.
func childReport(s *Step, state string, reports int) error {
	var st struct {
		ChildErrorReport struct {
			State   string `json:"state"`
			Reports int    `json:"reports"`
		} `json:"childErrorReport"`
	}
	if err := json.Unmarshal([]byte(s.Status.Raw), &st); err != nil {
		return err
	}
	r := st.ChildErrorReport
	return check(r.State == state && r.Reports == reports, "status child report %s (%d), want %s (%d)", r.State, r.Reports, state, reports)
}

// reportIsCountOnly checks a child_error payload: counts, and no child ID.
func reportIsCountOnly(r Report, errored, running int) error {
	data, _ := r.Body["data"].(map[string]any)
	raw := fmt.Sprint(r.Body)
	return all(
		check(data["erroredChildren"] == float64(errored), "erroredChildren %v, want %d", data["erroredChildren"], errored),
		check(data["runningChildren"] == float64(running), "runningChildren %v, want %d", data["runningChildren"], running),
		check(!strings.Contains(raw, "thr_example_child"), "the report names a child"),
	)
}

// noNotice: nothing sent in the step carries a child-error notice.
func noNotice(s *Step) error {
	for _, m := range s.Sent {
		if strings.Contains(m.Message, "[keepgoing:child-errors") {
			return fmt.Errorf("the message to %s carries a child-error notice", m.Thread)
		}
	}
	return nil
}

// noChildCommands: over the whole journey the only bb commands naming a
// child read its status or pending interactions.
func noChildCommands(w *World) error {
	for _, st := range w.Journal {
		for _, c := range st.Calls {
			line := strings.Join(c, " ")
			if !strings.Contains(line, "thr_example_child") {
				continue
			}
			if len(c) > 1 && (c[1] == "show" || c[1] == "interactions") {
				continue
			}
			return fmt.Errorf("ran bb %s", line)
		}
	}
	return nil
}

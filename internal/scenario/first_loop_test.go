package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var scenarioStart = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// TestScenarioFirstLoop follows a new user from `init` to a completed task:
// a disabled setup touches nothing, enabling wakes the idle agent with the
// task, a working agent is left alone, an idle agent with progress is woken
// again, and the agent's own `complete` ends supervision with one report.
func TestScenarioFirstLoop(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("complete", "await_user", "stop")
	primary := w.Thread(primaryThread)

	// Setup leaves supervision disabled: a check runs no bb command at all.
	s := w.Tick()
	w.Check(s, "disabled check", all(
		action(s, "none"), reasonIs(s, "disabled"), nothingSent(s), noCalls(s), noReports(s),
		holds(s, "disabled"), runsState(s, "disabled"),
	))

	s = w.Control("enable")
	w.Check(s, "enable", all(exit(s, 0), holds(s), nothingSent(s), noReports(s)))

	// The idle agent is woken once, with the task.
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "first wake", all(
		action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}),
		sentContains(s, scenarioTask), holds(s), noReports(s), attemptsAre(s, 1),
	))

	// While the turn runs, checks leave the agent alone.
	for i := 0; i < 3; i++ {
		w.Advance(5 * time.Minute)
		s = w.Tick()
		w.Check(s, "working agent", all(action(s, "none"), reasonIs(s, "active"), nothingSent(s), holds(s), noReports(s)))
	}

	// The turn ends with progress; the next check wakes it to continue.
	w.Commit("feat: fictional first step")
	primary.FinishTurn()
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "wake after progress", all(
		action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}),
		holds(s), noReports(s), attemptsAre(s, 2),
	))

	// The agent finishes and declares completion from its own thread, as the
	// generated task prompt tells it to.
	w.Advance(10 * time.Minute)
	w.Commit("feat: fictional final step")
	s = w.AgentRuns("complete", "--note", "Fictional task verified")
	w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), nothingSent(s), reported(s, "keepgoing.complete")))
	primary.FinishTurn()

	// A completed loop never queries or wakes the agent again, and reports
	// nothing more.
	for i := 0; i < 2; i++ {
		w.Advance(5 * time.Minute)
		s = w.Tick()
		w.Check(s, "completed check", all(
			action(s, "none"), reasonIs(s, "completed"), nothingSent(s), noCalls(s), noReports(s),
			holds(s, "completed"), runsState(s, "completed"), attemptsAre(s, 2),
		))
	}
	if s.Status.Control.Note != "Fictional task verified" {
		w.Failf(s, "status note %q", s.Status.Control.Note)
	}
}

// Assertions return an error describing what differs, so a scenario can
// either enforce them (Check) or record them as known failures (Known).

func all(errs ...error) error {
	var msgs []string
	for _, e := range errs {
		if e != nil {
			msgs = append(msgs, e.Error())
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

func exit(s *Step, code int) error {
	if s.Result.Code != code {
		return fmt.Errorf("exit %d, want %d: %s", s.Result.Code, code, strings.TrimSpace(s.Result.Stderr))
	}
	return nil
}

func action(s *Step, want string) error {
	if s.action() != want {
		return fmt.Errorf("action %q (%s), want %q", s.action(), s.reason(), want)
	}
	return nil
}

func reasonIs(s *Step, want string) error {
	if s.reason() != want {
		return fmt.Errorf("reason %q, want %q", s.reason(), want)
	}
	return nil
}

func reasonHas(s *Step, want string) error {
	if !strings.Contains(s.reason(), want) {
		return fmt.Errorf("reason %q does not mention %q", s.reason(), want)
	}
	return nil
}

func nothingSent(s *Step) error {
	if len(s.Sent) != 0 {
		return fmt.Errorf("sent %s", describeSends(s.Sent))
	}
	return nil
}

// sentExactly compares kind, thread and mode of every send in the step.
func sentExactly(s *Step, want ...Send) error {
	got := make([]Send, len(s.Sent))
	for i, m := range s.Sent {
		got[i] = Send{Kind: m.Kind, Thread: m.Thread, Mode: m.Mode}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("sent %s, want %s", describeSends(s.Sent), describeSends(want))
	}
	return nil
}

func sentContains(s *Step, text string) error {
	for _, m := range s.Sent {
		if strings.Contains(m.Message, text) {
			return nil
		}
	}
	return fmt.Errorf("no message contained %q", text)
}

func describeSends(sends []Send) string {
	if len(sends) == 0 {
		return "nothing"
	}
	var parts []string
	for _, m := range sends {
		p := m.Kind + " to " + m.Thread
		if m.Mode != "" {
			p += " (" + m.Mode + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

func noCalls(s *Step) error {
	if len(s.Calls) != 0 {
		return fmt.Errorf("ran bb %v", s.Calls)
	}
	return nil
}

func noReports(s *Step) error {
	if len(s.Reports) != 0 {
		return fmt.Errorf("reported %v", reportTypes(s.Reports))
	}
	return nil
}

func reported(s *Step, types ...string) error {
	if got := reportTypes(s.Reports); fmt.Sprint(got) != fmt.Sprint(types) {
		return fmt.Errorf("reported %v, want %v", got, types)
	}
	return nil
}

func reportTypes(rs []Report) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Type)
	}
	return out
}

// holds compares the holds in force, in any order.
func holds(s *Step, want ...string) error {
	got := s.Holds()
	if len(got) != len(want) {
		return fmt.Errorf("holds %v, want %v", got, want)
	}
	for _, h := range want {
		if !strings.Contains(" "+strings.Join(got, " ")+" ", " "+h+" ") {
			return fmt.Errorf("holds %v, want %v", got, want)
		}
	}
	return nil
}

func runsState(s *Step, want string) error {
	if got := s.Runs.ControlState(); got != want {
		return fmt.Errorf("runs control state %q, want %q", got, want)
	}
	return nil
}

func attemptsAre(s *Step, want int) error {
	if got := s.Runs.Attempts(); got != want {
		return fmt.Errorf("runs shows %d attempts, want %d", got, want)
	}
	return nil
}

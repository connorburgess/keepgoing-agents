package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scheduler is a stand-in for an external scheduler that runs `keepgoing
// tick`, such as a BB automation. Like one, it counts consecutive failing
// exits and disables itself after three. Exit 75 (skipped) is a success.
type scheduler struct {
	w *World
	// identity is the BB thread the scheduler runs its checks as.
	identity string
	failures int
	disabled bool
	// fired counts the checks it actually ran.
	fired int
}

// fire runs one scheduled check, unless the scheduler has disabled itself.
func (sch *scheduler) fire() *Step {
	sch.w.t.Helper()
	if sch.disabled {
		return nil
	}
	sch.fired++
	s := sch.w.tickAs(sch.identity)
	switch s.Result.Code {
	case 0, 75:
		sch.failures = 0
	default:
		sch.failures++
		sch.disabled = sch.failures >= 3
	}
	return s
}

// tickAs runs one check as the given BB thread. A check run as the loop's
// owner is an ordinary check; any other is recorded under its own label, so
// the rule that a check exits 0 is not applied to a refused one.
func (w *World) tickAs(thread string) *Step {
	w.t.Helper()
	w.settle()
	r := w.RunAs(thread, "tick", "-c", w.Config, "--now", w.stamp())
	var line map[string]any
	if r.Code == 0 {
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &line); err != nil {
			w.t.Fatalf("tick output is not one JSON line: %v\n%s", err, r.Stdout)
		}
	}
	label := "tick"
	if thread != primaryThread {
		label = "tick as " + thread
	}
	return w.record(label, r, line)
}

// bbDown makes every bb command fail with no output, or work again.
func (w *World) bbDown(down bool) {
	w.t.Helper()
	path := filepath.Join(w.world, "bb.down")
	if !down {
		os.Remove(path)
		return
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// TestScenarioSchedulerDeathAndRestart has two halves.
//
// Three checks in a row cannot observe the session, because BB cannot be
// reached. Each is recorded, exits 0 and is reported, so a scheduler that
// disables a job after three failures keeps running, and the loop carries on
// by itself when BB is back.
//
// Then the scheduler does die: it is moved to another BB thread, every check
// is refused, and it disables itself after the third. Nothing runs, nothing
// is sent and nothing is reported while it is dead; status says the history
// is stale. A person restarts it under the right identity, and the first
// check carries on from the saved state without repeating a send.
func TestScenarioSchedulerDeathAndRestart(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init("error", "await_user", "complete")
	primary := w.Thread(primaryThread)
	// The loop is enabled from its own thread, which becomes its owner.
	s := w.AgentRuns("enable")
	w.Check(s, "enable", all(exit(s, 0), holds(s)))
	sch := &scheduler{w: w, identity: primaryThread}

	w.Advance(time.Minute)
	s = sch.fire()
	w.Check(s, "first wake", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}), failedChecks(s, 0)))

	// BB cannot be reached for three checks.
	w.bbDown(true)
	firstFailure := w.Now.Add(5 * time.Minute)
	for i := 1; i <= 3; i++ {
		w.Advance(5 * time.Minute)
		s = sch.fire()
		w.Check(s, fmt.Sprintf("failed check %d is recorded and reported", i), all(
			exit(s, 0), action(s, "none"), outcome(s, "failed"), nothingSent(s), holds(s),
			onlyErrorReports(s), failedChecks(s, i), failedSince(s, firstFailure),
		))
		// One failure, one report. Today the check reports it twice: once as
		// "could not observe" and once as "tick failed".
		w.Known(s, Waits(WaitDuplicateErrorReport), "a failed check sends one error report", reported(s, "keepgoing.error"))
	}
	w.Check(s, "the scheduler is still running after three failed checks", check(!sch.disabled && sch.failures == 0, "disabled=%t failures=%d", sch.disabled, sch.failures))

	// BB is back. The turn finished meanwhile; the next check wakes it, and
	// the run of failed checks is over.
	w.bbDown(false)
	w.Commit("feat: fictional first step")
	primary.FinishTurn()
	w.Advance(5 * time.Minute)
	s = sch.fire()
	w.Check(s, "recovery without a person", all(
		action(s, "wake"), outcome(s, "succeeded"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}),
		noReports(s), holds(s), failedChecks(s, 0),
	))

	// The scheduler is moved to another BB thread. Every check is refused
	// before it starts, and the scheduler disables itself after the third.
	sch.identity = "thr_example_other"
	lastGood := w.Now
	for i := 1; i <= 3; i++ {
		w.Advance(5 * time.Minute)
		s = sch.fire()
		w.Check(s, fmt.Sprintf("refused check %d", i), all(
			exit(s, 1), nothingSent(s), noCalls(s), noReports(s), holds(s), lastCheckAt(s, lastGood),
		))
	}
	w.Check(s, "the scheduler disabled itself", check(sch.disabled, "the scheduler is still enabled after three exits of 1"))

	// Nothing runs while it is dead. The turn ends and nobody wakes it.
	w.Commit("feat: fictional second step")
	primary.FinishTurn()
	fired, sent, reports := sch.fired, w.sendsTotal, len(w.Hooks.Events())
	for i := 0; i < 36; i++ { // three hours of five-minute slots
		w.Advance(5 * time.Minute)
		if sch.fire() != nil {
			t.Fatal("a disabled scheduler ran a check")
		}
	}
	w.Check(s, "nothing ran, was sent or was reported while the scheduler was dead", check(
		sch.fired == fired && w.sendsTotal == sent && len(w.Hooks.Events()) == reports,
		"fired %d->%d, sends %d->%d, reports %d->%d", fired, sch.fired, sent, w.sendsTotal, reports, len(w.Hooks.Events())))

	// What a person sees when they look: the history is stale.
	st := w.Run("status", "-c", w.Config, "--json", "--now", w.stamp())
	var runtime struct {
		Runtime struct {
			Freshness string `json:"freshness"`
			Reason    string `json:"reason"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal([]byte(st.Stdout), &runtime); err != nil || st.Code != 0 {
		t.Fatalf("status: exit %d %v\n%s", st.Code, err, st.Stderr)
	}
	w.Check(s, "status says the history is stale", check(
		runtime.Runtime.Freshness == "stale" && runtime.Runtime.Reason == "no recent check; inspect the supervisor and its scheduler",
		"freshness %q, reason %q", runtime.Runtime.Freshness, runtime.Runtime.Reason))

	// The person restarts the scheduler under the loop's own thread. The
	// first check carries on: one wake for the idle session, no hold, and
	// nothing that was sent before is sent again.
	sch.identity, sch.disabled, sch.failures = primaryThread, false, 0
	w.Advance(time.Minute)
	s = sch.fire()
	w.Check(s, "first check after the restart", all(
		exit(s, 0), action(s, "wake"), outcome(s, "succeeded"),
		sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}), noReports(s), holds(s), failedChecks(s, 0),
	))
	w.Advance(5 * time.Minute)
	s = sch.fire()
	w.Check(s, "the session it woke is left to work", all(action(s, "none"), reasonIs(s, "active"), nothingSent(s), noReports(s)))
	w.Check(s, "three wakes in the whole journey", journeySends(w, primaryThread, "tell", "tell", "tell"))
}

// WaitDuplicateErrorReport: a check that cannot observe its session sends two
// error reports for the one failure, where the documentation promises one
// per failed check. Reported from this scenario; not assigned yet.
const WaitDuplicateErrorReport Waiting = "duplicate error report for an unobservable session, unassigned"

// onlyErrorReports: the step reported at least once, and only errors.
func onlyErrorReports(s *Step) error {
	if len(s.Reports) == 0 {
		return fmt.Errorf("nothing was reported")
	}
	for _, r := range s.Reports {
		if r.Type != "keepgoing.error" {
			return fmt.Errorf("reported %v", reportTypes(s.Reports))
		}
	}
	return nil
}

func outcome(s *Step, want string) error {
	got, _ := s.Tick["outcome"].(string)
	return check(got == want && s.Status.State.LastCheck.Outcome == want, "outcome: tick %q, status %q, want %q", got, s.Status.State.LastCheck.Outcome, want)
}

// failedState reads the run of failed checks from status.
func failedState(s *Step) (n int, since time.Time, err error) {
	var st struct {
		State struct {
			FailedChecks int       `json:"failedChecks"`
			FailedSince  time.Time `json:"failedSince"`
		} `json:"state"`
	}
	err = json.Unmarshal([]byte(s.Status.Raw), &st)
	return st.State.FailedChecks, st.State.FailedSince, err
}

func failedChecks(s *Step, want int) error {
	n, _, err := failedState(s)
	if err != nil {
		return err
	}
	return check(n == want, "status failedChecks %d, want %d", n, want)
}

func failedSince(s *Step, want time.Time) error {
	_, since, err := failedState(s)
	if err != nil {
		return err
	}
	return check(since.Equal(want), "status failedSince %s, want %s", since.Format(time.RFC3339), want.Format(time.RFC3339))
}

// lastCheckAt: the last recorded check is still the one at the given time.
func lastCheckAt(s *Step, want time.Time) error {
	got := s.Status.State.LastCheck.At
	return check(got.Equal(want), "status last check at %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
}

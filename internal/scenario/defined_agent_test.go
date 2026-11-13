package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// definedAgentConfig is a loop whose agent keepgoing has no built-in runner
// for. The definition is written from the fictional agent's documented
// interface (see the stand-in): its commands, where the session comes from,
// and which exit codes and status values mean finished or waiting on a
// person. %[1]s is the stand-in executable, %[2]s the project.
const definedAgentConfig = `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: %[3]q
  adapter: custom
  custom:
    name: example-agent
    workdir: %[2]s
    definition:
      start: ['%[1]s', run, --json]
      resume: ['%[1]s', run, --json, --session, "{session}"]
      output: {format: jsonl, select: {field: type, equals: result}}
      session: {field: session}
      finished: {exit_codes: [0], field: status, equals: [ok]}
      waiting: {exit_codes: [2], field: status, equals: [needs_input]}
      capabilities: {exact_resume: true, can_be_stopped: true, structured_waiting: true}
  report: {events: [await_user, complete], timeout: 5s}
`

const exampleSession = "sess_example_0001"

func (w *World) nextTurns(outcomes ...string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.world, "example-agent.turns"), []byte(strings.Join(outcomes, "\n")+"\n"), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// TestDefinedAgentJourney supervises an agent defined only in configuration
// through the built binary: the first turn starts a session, later turns
// resume exactly that session, a failed turn is re-prompted in it, a turn
// that ends waiting on a person raises one hold and one report and sends
// nothing until a person resumes, and completion ends supervision.
func TestDefinedAgentJourney(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.install("example-agent")
	w.Headless = "example-agent" // steps wait for its detached turns to end
	dir := filepath.Join(w.Root, "setup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.Config = filepath.Join(dir, "keepgoing.yaml")
	if err := os.WriteFile(w.Config, []byte(fmt.Sprintf(definedAgentConfig, w.BinPath("example-agent"), w.Project, scenarioTask)), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Must("validate", "-c", w.Config)

	// doctor says what this definition lets keepgoing do, and what it cannot.
	doctor := w.Run("doctor", "-c", w.Config, "--offline")
	for _, want := range []string{"Defined agent example-agent", "continue the same session on later turns", "hold for a person when a turn ends waiting on one",
		"classify a failed turn: every failure is unknown", "it cannot be a fallback without a command probe"} {
		if !strings.Contains(doctor.Stdout, want) {
			t.Errorf("doctor does not say %q:\n%s", want, doctor.Stdout)
		}
	}

	s := w.Control("enable")
	w.Check(s, "enable", all(exit(s, 0), holds(s), nothingSent(s)))

	// The first turn starts a new session with the task on standard input.
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "first turn", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: ""}), sentContains(s, scenarioTask), holds(s), noReports(s), noCalls(s)))

	// It ended healthy, so the next check continues exactly that session.
	w.Commit("feat: fictional first step")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "second turn resumes the session", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession}), holds(s), noReports(s)))

	// A turn fails with nothing the definition declares: an unknown error,
	// re-prompted in the same session.
	w.nextTurns("failed", "needs_input")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "the turn that will fail", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession})))
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "re-prompt after a failed turn", all(action(s, "reprompt"), reasonIs(s, "error: unknown"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession}), holds(s), noReports(s)))

	// That turn ended waiting on a person: one hold, one report, nothing sent,
	// however many checks follow.
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "waiting on a person", all(action(s, "await_user"), nothingSent(s), holds(s, "awaitingUser"), reported(s, "keepgoing.await_user")))
	for i := 0; i < 3; i++ {
		w.Advance(10 * time.Minute)
		s = w.Tick()
		w.Check(s, "held for a person", all(action(s, "none"), nothingSent(s), holds(s, "awaitingUser"), noReports(s)))
	}

	// A person deals with it and resumes: the same wait is not raised again,
	// and the session is continued.
	s = w.Control("resume")
	w.Check(s, "resume", all(exit(s, 0), holds(s), nothingSent(s)))
	w.Advance(15 * time.Minute)
	s = w.Tick()
	w.Check(s, "continues after resume", all(sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession}), holds(s), noReports(s)))
	if a := s.action(); a != "reprompt" && a != "wake" {
		w.Failf(s, "action %q after resume", a)
	}

	// Completion ends supervision; nothing more is sent.
	s = w.Control("complete")
	w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), nothingSent(s)))
	w.Advance(time.Hour)
	s = w.Tick()
	w.Check(s, "after completion", all(action(s, "none"), nothingSent(s), holds(s, "completed")))
}

// TestDefinedAgentOutage runs the network gate with an agent defined in
// configuration: the turn that runs into the outage is spent, every check
// while the network is down sends nothing and raises nothing, and the first
// check after it returns re-prompts once, in the same session.
func TestDefinedAgentOutage(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.install("example-agent")
	w.Headless = "example-agent"
	dir := filepath.Join(w.Root, "setup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.Config = filepath.Join(dir, "keepgoing.yaml")
	if err := os.WriteFile(w.Config, []byte(fmt.Sprintf(definedAgentConfig, w.BinPath("example-agent"), w.Project, scenarioTask)), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Control("enable")

	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first turn", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent"})))

	// The network drops; the next turn runs into it and fails unclassified.
	w.Net.Offline()
	w.Advance(10 * time.Minute)
	s = w.Tick()
	w.Check(s, "the turn the outage breaks", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession})))

	// While it is down nothing is sent, held or reported, however long.
	for i, wait := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour, 48 * time.Hour} {
		w.Advance(wait)
		s = w.Tick()
		w.Check(s, fmt.Sprintf("offline check %d", i+1), all(nothingSent(s), holds(s), noReports(s)))
		if a := s.action(); a == "reprompt" || a == "await_user" {
			w.Failf(s, "offline check %d acted: %s", i+1, a)
		}
	}

	// It returns: one re-prompt, in the same session, and the loop goes on.
	w.Net.Online()
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "first check after the outage", all(action(s, "reprompt"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession}), holds(s), noReports(s)))
	w.Commit("feat: fictional step after the outage")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "back to normal", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession}), holds(s), noReports(s)))
}

// TestDefinedAgentLostSession: the saved session becomes unusable and the
// last turn did not report one, so keepgoing cannot tell which conversation
// to continue. It does not start another by itself: it holds, and the hold
// names the way out. A person runs `session reset`, resumes, and the next
// turn starts a new conversation on purpose, recorded in the timeline.
func TestDefinedAgentLostSession(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.install("example-agent")
	w.Headless = "example-agent"
	dir := filepath.Join(w.Root, "setup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.Config = filepath.Join(dir, "keepgoing.yaml")
	if err := os.WriteFile(w.Config, []byte(fmt.Sprintf(definedAgentConfig, w.BinPath("example-agent"), w.Project, scenarioTask)), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first turn", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent"})))

	// The next turn fails before reporting a session, and the saved
	// identifier is damaged.
	w.nextTurns("failed_no_session")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "the turn that fails", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession})))
	session := filepath.Join(dir, "state", "targets", "primary", "session")
	if err := os.WriteFile(session, []byte("--not-a-session\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "held instead of starting another conversation", all(action(s, "await_user"), reasonHas(s, "run keepgoing session reset"), nothingSent(s), holds(s, "awaitingUser"), reported(s, "keepgoing.await_user")))
	// Resuming without resetting holds again; still nothing is sent.
	s = w.Control("resume")
	w.Advance(15 * time.Minute)
	s = w.Tick()
	w.Check(s, "resume alone holds again", all(action(s, "await_user"), nothingSent(s), holds(s, "awaitingUser")))

	// The way out, on purpose.
	s = w.record("session reset", w.Run("session", "reset", "-c", w.Config), nil)
	w.Check(s, "session reset", all(exit(s, 0), nothingSent(s), holds(s, "awaitingUser")))
	if !strings.Contains(s.Result.Stdout, "starts a new conversation") || !strings.Contains(s.Timeline.Raw, "session reset from a terminal or scheduler") {
		w.Failf(s, "reset output %q; timeline does not record it", s.Result.Stdout)
	}
	if _, err := os.Stat(session); !os.IsNotExist(err) {
		w.Failf(s, "the session file is still there: %v", err)
	}
	s = w.Control("resume")
	w.Check(s, "resume", all(exit(s, 0), holds(s), nothingSent(s)))
	w.Advance(15 * time.Minute)
	s = w.Tick()
	w.Check(s, "a new conversation", all(sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: ""}), holds(s), noReports(s)))
	// And it is continued from then on.
	w.Commit("feat: fictional step in the new conversation")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "the new conversation is resumed", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-agent", Mode: exampleSession})))
}

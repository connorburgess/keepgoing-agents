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

// tickTurn runs one check and, when it started a turn, waits for the
// detached turn to end before the step is recorded, so the step shows what
// the agent received.
func (w *World) tickTurn() *Step {
	w.t.Helper()
	r := w.Run("tick", "-c", w.Config, "--now", w.stamp())
	var line map[string]any
	if r.Code == 0 {
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &line); err != nil {
			w.t.Fatalf("tick output is not one JSON line: %v\n%s", err, r.Stdout)
		}
	}
	switch line["action"] {
	case "wake", "nudge", "reprompt":
		exit := filepath.Join(filepath.Dir(w.Config), "state", "targets", "primary", "exit")
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat(exit); err == nil {
				break
			}
			if time.Now().After(deadline) {
				w.t.Fatalf("the turn started by %v never ended", line["action"])
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return w.record("tick", r, line)
}

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
	s = w.tickTurn()
	w.Check(s, "first turn", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "", Mode: "auto"}), sentContains(s, scenarioTask), holds(s), noReports(s), noCalls(s)))

	// It ended healthy, so the next check continues exactly that session.
	w.Commit("feat: fictional first step")
	w.Advance(5 * time.Minute)
	s = w.tickTurn()
	w.Check(s, "second turn resumes the session", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: exampleSession, Mode: "auto"}), holds(s), noReports(s)))

	// A turn fails with nothing the definition declares: an unknown error,
	// re-prompted in the same session.
	w.nextTurns("failed", "needs_input")
	w.Advance(5 * time.Minute)
	s = w.tickTurn()
	w.Check(s, "the turn that will fail", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: exampleSession, Mode: "auto"})))
	w.Advance(time.Minute)
	s = w.tickTurn()
	w.Check(s, "re-prompt after a failed turn", all(action(s, "reprompt"), reasonIs(s, "error: unknown"), sentExactly(s, Send{Kind: "turn", Thread: exampleSession, Mode: "auto"}), holds(s), noReports(s)))

	// That turn ended waiting on a person: one hold, one report, nothing sent,
	// however many checks follow.
	w.Advance(5 * time.Minute)
	s = w.tickTurn()
	w.Check(s, "waiting on a person", all(action(s, "await_user"), nothingSent(s), holds(s, "awaitingUser"), reported(s, "keepgoing.await_user")))
	for i := 0; i < 3; i++ {
		w.Advance(10 * time.Minute)
		s = w.tickTurn()
		w.Check(s, "held for a person", all(action(s, "none"), nothingSent(s), holds(s, "awaitingUser"), noReports(s)))
	}

	// A person deals with it and resumes: the same wait is not raised again,
	// and the session is continued.
	s = w.Control("resume")
	w.Check(s, "resume", all(exit(s, 0), holds(s), nothingSent(s)))
	w.Advance(15 * time.Minute)
	s = w.tickTurn()
	w.Check(s, "continues after resume", all(sentExactly(s, Send{Kind: "turn", Thread: exampleSession, Mode: "auto"}), holds(s), noReports(s)))
	if a := s.action(); a != "reprompt" && a != "wake" {
		w.Failf(s, "action %q after resume", a)
	}

	// Completion ends supervision; nothing more is sent.
	s = w.Control("complete")
	w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), nothingSent(s)))
	w.Advance(time.Hour)
	s = w.tickTurn()
	w.Check(s, "after completion", all(action(s, "none"), nothingSent(s), holds(s, "completed")))
}

package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// acpAgentConfig is a loop whose agent is reached over the Agent Client
// Protocol. %[1]s is the stand-in executable, %[2]s the project.
const acpAgentConfig = `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: %[3]q
  adapter: acp
  acp:
    name: example-acp
    command: ['%[1]s', acp]
    workdir: %[2]s
  report: {events: [await_user, complete], timeout: 5s}
`

const exampleACPSession = "sess_example_acp_0001"

// TestACPAgentJourney supervises an agent over ACP through the built binary:
// the first prompt opens a session, later prompts continue exactly that
// session, a prompt that ends on an untyped failure is re-prompted in it, a
// permission request is never granted and raises one hold and one report,
// and after a person resumes the session is continued.
func TestACPAgentJourney(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.install("example-acp")
	w.Headless = "example-acp" // steps wait for its detached turns to end
	dir := filepath.Join(w.Root, "setup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.Config = filepath.Join(dir, "keepgoing.yaml")
	if err := os.WriteFile(w.Config, []byte(fmt.Sprintf(acpAgentConfig, w.BinPath("example-acp"), w.Project, scenarioTask)), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Must("validate", "-c", w.Config)
	turns := func(outcomes ...string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(w.world, "example-acp.turns"), []byte(strings.Join(outcomes, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := w.Control("enable")
	w.Check(s, "enable", all(exit(s, 0), holds(s), nothingSent(s)))

	// The first prompt opens a session; the task travels over the protocol.
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "first prompt", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-acp", Mode: ""}), sentContains(s, scenarioTask), holds(s), noReports(s), noCalls(s)))

	// It ended with end_turn, so the next check continues that session.
	w.Commit("feat: fictional first step")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "second prompt continues the session", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-acp", Mode: exampleACPSession}), holds(s), noReports(s)))

	// A prompt ends on a stop reason with no class, then one asks for a
	// permission.
	turns("max_tokens", "permission")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "the prompt that will fail", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "example-acp", Mode: exampleACPSession})))
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "re-prompt after an untyped failure", all(action(s, "reprompt"), reasonIs(s, "error: unknown"), sentExactly(s, Send{Kind: "turn", Thread: "example-acp", Mode: exampleACPSession}), holds(s), noReports(s)))

	// That prompt asked for a permission. It was answered "cancelled", never
	// granted; the loop is held once and reports once.
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "a permission request holds", all(action(s, "await_user"), nothingSent(s), holds(s, "awaitingUser"), reported(s, "keepgoing.await_user")))
	replies, _ := os.ReadFile(filepath.Join(w.world, "permission-replies.jsonl"))
	if !strings.Contains(string(replies), `cancelled`) || strings.Contains(string(replies), "allow") || strings.Count(string(replies), "\n") != 1 {
		w.Failf(s, "permission replies: %s", replies)
	}
	for i := 0; i < 2; i++ {
		w.Advance(10 * time.Minute)
		s = w.Tick()
		w.Check(s, "held for a person", all(action(s, "none"), nothingSent(s), holds(s, "awaitingUser"), noReports(s)))
	}

	// A person resumes: the same request is not raised again and the
	// session is continued.
	s = w.Control("resume")
	w.Check(s, "resume", all(exit(s, 0), holds(s), nothingSent(s)))
	w.Advance(15 * time.Minute)
	s = w.Tick()
	w.Check(s, "continues after resume", all(sentExactly(s, Send{Kind: "turn", Thread: "example-acp", Mode: exampleACPSession}), holds(s), noReports(s)))

	s = w.Control("complete")
	w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), nothingSent(s)))
}

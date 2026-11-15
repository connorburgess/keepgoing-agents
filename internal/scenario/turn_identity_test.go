package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/envvar"
)

// A supervisor started by hand inside a BB thread inherits that thread's
// variables and its agent session's. The headless session it starts is
// neither, so its turn receives none of envvar.SupervisorSession. It still
// receives the supervised-loop marker (#276). Names only.
func TestScenarioHeadlessTurnIsNotItsSupervisorsSession(t *testing.T) {
	t.Parallel()
	for _, cli := range []string{"claude", "codex"} {
		t.Run(cli, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			w.InitHeadless(cli)
			w.Control("enable")
			w.Advance(time.Minute)
			w.settle()
			// As if started from an operator thread: every listed name is set,
			// BB_THREAD_ID by RunAs itself.
			for _, n := range envvar.SupervisorSession {
				if n != "BB_THREAD_ID" {
					w.env = append(w.env, n+"=fictional")
				}
			}
			r := w.RunAs("thr_example_operator", "tick", "-c", w.Config, "--now", w.stamp())
			s := w.record("tick from an operator thread", r, nil)
			w.settle()
			w.read(s)
			w.Check(s, "the first turn ran", sentExactly(s, Send{Kind: "turn", Thread: cli}))
			b, err := os.ReadFile(filepath.Join(w.world, "turn-session-vars.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(b)); got != `{"cli":"`+cli+`","received":"LOOPTENDER_SUPERVISED_LOOP"}` {
				t.Fatalf("the turn received %s", got)
			}
		})
	}
}

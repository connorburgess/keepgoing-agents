package scenario

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A bb command the host kills in the middle of a check (an overloaded or
// out-of-memory machine does this) is not a failure of the session: the
// check treats what it could not read as unverified, sends nothing, raises
// no hold, says the host killed a command, and the next check carries on.
// The harness recognises that warning, so a kill it did not ask for fails a
// scenario as "HOST KILLED A STAND-IN" and not as a product assertion.
func TestScenarioHostKilledCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot be ended by a signal on Windows")
	}
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.AllowHostKills = true
	w.Init("await_user", "complete", "stop")
	primary := w.Thread(primaryThread)
	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake", action(s, "wake"))
	// The turn fails and BB gives up on it.
	w.Advance(2 * time.Minute)
	primary.NetworkError(false)

	// The host kills the read of the thread's log on the next check.
	if err := os.WriteFile(filepath.Join(w.world, "kill.next"), []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "the check waits", all(exit(s, 0), action(s, "none"), reasonIs(s, "provider retry state unverified"), nothingSent(s), holds(s), noReports(s)))
	if killed := hostKilled(s); !strings.Contains(killed, "killed by the host, not by keepgoing's time limit") {
		w.Failf(s, "the check did not say the host killed a command: warnings %v", s.Tick["warnings"])
	}

	// Nothing was spent: the next check recovers the turn as usual.
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "the next check recovers the turn", all(action(s, "reprompt"), sentTo(s, primaryThread, Send{Kind: "retry", Thread: primaryThread}), holds(s)))
	if hostKilled(s) != "" {
		w.Failf(s, "the kill was reported again")
	}
}

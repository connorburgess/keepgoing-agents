package scenario

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A stand-in bb self-signals during a read. The check keeps the read
// unverified, sends nothing, raises no hold, and warns about the signal with
// unknown source/cause. The next check carries on without another warning.
// This explicitly opts out of the harness's unexpected-termination check.
func TestScenarioSignalTerminatedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot be ended by a signal on Windows")
	}
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.AllowSignalExits = true
	w.Init("await_user", "complete", "stop")
	primary := w.Thread(primaryThread)
	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake", action(s, "wake"))
	// The turn fails and BB gives up on it.
	w.Advance(2 * time.Minute)
	primary.NetworkError(false)

	// The stand-in self-signals during the next read of the thread log.
	if err := os.WriteFile(filepath.Join(w.world, "kill.next"), []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "the check waits", all(exit(s, 0), action(s, "none"), reasonIs(s, "provider retry state unverified"), nothingSent(s), holds(s), noReports(s)))
	if killed := signalTermination(s); !strings.Contains(killed, "terminated by a Unix signal outside the adapter timeout/cancellation; signal source and cause are unknown") {
		w.Failf(s, "the check did not report a signal with unknown source/cause: warnings %v", s.Tick["warnings"])
	}

	// Nothing was spent: the next check recovers the turn as usual.
	w.Advance(time.Minute)
	s = w.Tick()
	w.Check(s, "the next check recovers the turn", all(action(s, "reprompt"), sentTo(s, primaryThread, Send{Kind: "retry", Thread: primaryThread}), holds(s)))
	if signalTermination(s) != "" {
		w.Failf(s, "the kill was reported again")
	}
}

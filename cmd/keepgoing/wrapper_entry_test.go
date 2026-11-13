package main

import (
	"os"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
)

// Direct command-layer tests can launch a headless turn from the test binary.
// Dispatch wrapper mode before the test harness, just as the production CLI does.
func TestMain(m *testing.M) {
	runner.MaybeRunTurn()
	cursorJourneyHelper()
	initCursorInvocationTrap()
	// Loaded configurations check connectivity before an error re-prompt or
	// a probe. Tests must not depend on the real network: this stand-in
	// reports it up unless a test replaces it.
	os.Setenv("KEEPGOING_NET_PROBE", "exit 0")
	os.Exit(m.Run())
}

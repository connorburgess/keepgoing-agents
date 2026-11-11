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
	os.Exit(m.Run())
}

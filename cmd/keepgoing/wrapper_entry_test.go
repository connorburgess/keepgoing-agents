package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/session"
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
	// No test may open a real browser: unless a test names its own stand-in,
	// the panel's launcher is a program that does nothing.
	os.Setenv("KEEPGOING_PANEL_OPENER", "/usr/bin/true")
	// No test reads or writes the person's own registry, session claims or
	// standard route list.
	// The variables are inherited by every binary a test runs.
	private, err := os.MkdirTemp("", "keepgoing-test-user")
	if err != nil {
		panic(err)
	}
	os.Setenv(registryDirEnv, filepath.Join(private, "registry"))
	os.Setenv(session.DirEnv, filepath.Join(private, "sessions"))
	// Nor the person's standard route list: a test that needs one writes it
	// here or names its own.
	os.Setenv(config.RoutesFileEnv, filepath.Join(private, config.RoutesFileName))
	code := m.Run()
	panelTestCleanup() // the binary and launcher the panel tests share
	os.RemoveAll(private)
	os.Exit(code)
}

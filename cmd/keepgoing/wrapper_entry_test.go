package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/envvar"
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
	os.Setenv(envvar.NetProbe.Name(), "exit 0")
	// No test may open a real browser: unless a test names its own stand-in,
	// the panel's launcher is a program that does nothing.
	os.Setenv(panelOpenerEnv, "/usr/bin/true")
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
	// Nor the panel's directory, where --client reports are kept, in this
	// process: panelStateBase, a package variable, points at a private
	// directory. A test that moves HOME to a home of its own gets the real
	// lookup under it, as the userdir tests need. On Linux that lookup reads
	// XDG_STATE_HOME first, so it too points somewhere private unless a test
	// sets its own. A binary a test runs gets the same environment.
	os.Setenv("XDG_STATE_HOME", filepath.Join(private, "xdg-state"))
	home := os.Getenv("HOME")
	realConfigHome := buildUserConfigHome
	buildUserConfigHome = func() (string, error) {
		if os.Getenv("HOME") != home {
			return realConfigHome()
		}
		return filepath.Join(private, "config"), nil
	}
	realStateBase := panelStateBase
	panelStateBase = func() (string, error) {
		if os.Getenv("HOME") != home {
			return realStateBase()
		}
		return filepath.Join(private, "state"), nil
	}
	// The earlier names of these variables are still read. One set in the
	// shell that runs the tests must not reach them.
	for _, v := range envvar.Set {
		os.Unsetenv(v.Legacy())
	}
	// Nor the BB thread identity of the shell that runs the tests: a test
	// that is a BB thread says so with t.Setenv. Only the first test process
	// clears it: a copy of this binary a test starts as a helper inherits the
	// marker, and keeps the identity the test gave it on purpose.
	if os.Getenv("KEEPGOING_TEST_IDENTITY_CLEARED") == "" {
		os.Unsetenv("BB_THREAD_ID")
		os.Setenv("KEEPGOING_TEST_IDENTITY_CLEARED", "1")
	}
	code := m.Run()
	panelTestCleanup() // the binary and launcher the panel tests share
	os.RemoveAll(private)
	os.Exit(code)
}

// TestMain clears the BB thread identity of the shell that runs the tests,
// but a copy of the test binary a test starts as a helper, with an
// identity in its environment, must keep it.
func TestHelperProcessKeepsTheIdentityItIsGiven(t *testing.T) {
	if os.Getenv("BB_THREAD_ID") != "" {
		t.Fatalf("the test process inherited a BB thread identity")
	}
	c := exec.Command(os.Args[0], "-test.run=^TestIdentityHelper$")
	c.Env = append(os.Environ(), "KEEPGOING_IDENTITY_HELPER=1", "BB_THREAD_ID=thr_example_helper")
	out, err := c.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "identity=thr_example_helper\n") {
		t.Fatalf("helper: %v\n%s", err, out)
	}
}

func TestIdentityHelper(t *testing.T) {
	if os.Getenv("KEEPGOING_IDENTITY_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	fmt.Printf("identity=%s\n", os.Getenv("BB_THREAD_ID"))
}

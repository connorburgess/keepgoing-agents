package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/envvar"
	"github.com/looptender/looptender/internal/session"
)

// Direct command-layer tests can launch a headless turn from the test binary.
// Dispatch wrapper mode before the test harness, just as the production CLI does.
func TestMain(m *testing.M) {
	runner.MaybeRunTurn()
	adapterCheckHelper()
	contextHereAgentHelper()
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
	// Nor the person's accounts catalog, which doctor reads.
	doctorAccountsCatalog = filepath.Join(private, "accounts")
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
	guard := guardAgentCLIs(private)
	code := m.Run()
	if reached := guard(); reached != "" {
		fmt.Fprintf(os.Stderr, "FAIL: a test ran an agent CLI found on PATH instead of a stand-in:\n%s", reached)
		code = 1
	}
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

// guardedAgentCLIs are the agent and BB programs a configuration may name by
// bare name, so that a route without a cli setting runs the one PATH finds.
var guardedAgentCLIs = []string{"claude", "codex", "cursor-agent", "agent", "bb"}

// guardAgentCLIs puts a program first on PATH for each guardedAgentCLIs
// name, so that no test can run the real one installed on the machine: it
// would act on the person's own account, and its answer would make the test
// pass or fail by machine (CI has none installed). Each records the call and
// fails as a missing program would. The returned function lists the calls.
// A test that needs one of these programs installs a stand-in of its own,
// ahead of these. Windows does not run these scripts; nothing is installed
// there.
func guardAgentCLIs(private string) func() string {
	if runtime.GOOS == "windows" {
		return func() string { return "" }
	}
	dir := filepath.Join(private, "agent-cli-guard")
	log := filepath.Join(private, "agent-cli-guard.log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		panic(err)
	}
	for _, name := range guardedAgentCLIs {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"" + name + "\" \"$*\" >> '" + log + "'\n" +
			"echo 'test guard: no test may run the " + name + " found on PATH; install a stand-in' >&2\nexit 127\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			panic(err)
		}
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

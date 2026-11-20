//go:build unix

package netretry

import (
	"context"
	"github.com/looptender/looptender/internal/envvar"
	"github.com/looptender/looptender/internal/testexec"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectivityProbeRunsWithoutTheSupervisorsSession(t *testing.T) {
	out := setSessionVars(t)
	script := filepath.Join(t.TempDir(), "probe")
	testexec.Script(t, script, "#!/bin/sh\n"+testexec.ReportNames(out, reported()...))
	if (Probe{Command: []string{script}}).Check(context.Background()) != Reached {
		t.Fatal("probe failed")
	}
	checkReceivedExcept(t, out, "")
	out2 := setSessionVars(t)
	script2 := filepath.Join(t.TempDir(), "line")
	testexec.Script(t, script2, "#!/bin/sh\n"+testexec.ReportNames(out2, reported()...))
	if err := shell(context.Background(), script2).Run(); err != nil {
		t.Fatal(err)
	}
	checkReceivedExcept(t, out2, "")
}

// setSessionVars sets every listed variable and one kept variable to a
// fictional value, and returns where the stand-in reports the names it got.
func setSessionVars(t *testing.T) string {
	t.Helper()
	for _, n := range envvar.SupervisorSession {
		t.Setenv(n, "fictional")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "fictional")
	return filepath.Join(t.TempDir(), "received")
}

func reported() []string {
	return append(append([]string{}, envvar.SupervisorSession...), "CLAUDE_CONFIG_DIR")
}

// checkReceived expects the kept variable and the configured BB_PROJECT_ID.
func checkReceived(t *testing.T, out string) {
	t.Helper()
	checkReceivedExcept(t, out, "BB_PROJECT_ID")
}

// checkReceivedExcept expects only the kept variable and, if not "", configured.
func checkReceivedExcept(t *testing.T, out, configured string) {
	t.Helper()
	b, _ := os.ReadFile(out)
	want := []string{"CLAUDE_CONFIG_DIR"}
	if configured != "" {
		want = []string{configured, "CLAUDE_CONFIG_DIR"}
	}
	if got := strings.Fields(string(b)); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("received %q, want %q", got, want)
	}
}

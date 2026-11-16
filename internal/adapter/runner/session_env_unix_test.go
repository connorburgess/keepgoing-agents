//go:build unix

package runner

import (
	"context"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/envvar"
	"github.com/looptender/looptender/internal/testexec"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderReadersRunWithoutTheSupervisorsSession(t *testing.T) {
	report := func(t *testing.T, tail string) (string, string) {
		out := setSessionVars(t)
		script := filepath.Join(t.TempDir(), "cli")
		testexec.Script(t, script, "#!/bin/sh\n"+testexec.ReportNames(out, reported()...)+tail)
		return out, script
	}
	t.Run("custom limits lookup", func(t *testing.T) {
		out, script := report(t, "echo '{}'\n")
		readDefinedLimits(context.Background(), config.CustomLimits{Argv: config.Argv{{Parts: []string{script}}}}, config.Custom{Workdir: t.TempDir()}, []string{"BB_PROJECT_ID=configured"})
		checkReceived(t, out)
	})
	t.Run("codex limits", func(t *testing.T) {
		out, script := report(t, "exit 1\n")
		readCodexLimits(context.Background(), script, []string{"BB_PROJECT_ID=configured"})
		checkReceived(t, out)
	})
	t.Run("cursor version", func(t *testing.T) {
		out, script := report(t, "exit 1\n")
		(&Cursor{cfg: config.Cursor{CLI: script, Workdir: t.TempDir(), Env: map[string]string{"BB_PROJECT_ID": "configured"}}}).version(context.Background())
		checkReceived(t, out)
	})
}

// The #276 marker is not part of the list: a turn still receives it.
func TestTurnKeepsTheSupervisedLoopMarker(t *testing.T) {
	t.Setenv(control.SupervisedLoopVar, "/fictional/state")
	t.Setenv("BB_THREAD_ID", "fictional")
	cmd, err := wrapperCommand([]string{"x"}, ".", "/fictional/dir", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	marked := false
	for _, kv := range cmd.Env {
		name, _, _ := strings.Cut(kv, "=")
		marked = marked || name == control.SupervisedLoopVar
		if name == "BB_THREAD_ID" {
			t.Fatal("BB_THREAD_ID reached the wrapper")
		}
	}
	if !marked {
		t.Fatal("the supervised-loop marker did not reach the wrapper")
	}
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

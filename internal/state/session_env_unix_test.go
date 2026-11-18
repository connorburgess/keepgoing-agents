//go:build unix

package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/envvar"
	"github.com/looptender/looptender/internal/testexec"
)

// The progress check's git runs without the session the supervisor was
// started from: a repository's configuration can name programs git runs.
// A stand-in git on PATH reports the names it received, never values.
func TestGitRunsWithoutTheSupervisorsSession(t *testing.T) {
	for _, n := range envvar.SupervisorSession {
		t.Setenv(n, "fictional")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "fictional")
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "received")
	testexec.Script(t, filepath.Join(bin, "git"), "#!/bin/sh\n"+testexec.ReportNames(out, append(append([]string{}, envvar.SupervisorSession...), "CLAUDE_CONFIG_DIR", "GIT_OPTIONAL_LOCKS")...))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	git(context.Background(), t.TempDir(), "status")
	b, _ := os.ReadFile(out)
	if got := strings.Join(strings.Fields(string(b)), " "); got != "CLAUDE_CONFIG_DIR GIT_OPTIONAL_LOCKS" {
		t.Fatalf("git received %q", got)
	}
}

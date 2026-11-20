package supervise

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/envvar"
	"github.com/looptender/looptender/internal/session"
)

// No test takes a claim in the person's own session directory.
func TestMain(m *testing.M) {
	runner.MaybeRunTurn()
	if os.Getenv("KEEPGOING_SUPERVISE_AGY_HELPER") == "1" {
		runSuperviseAntigravityStandIn()
		os.Exit(0)
	}
	private, err := os.MkdirTemp("", "keepgoing-supervise-sessions")
	if err != nil {
		panic(err)
	}
	os.Setenv(session.DirEnv, filepath.Join(private, "sessions"))
	os.Unsetenv(envvar.SessionDir.Legacy()) // the earlier name is still read
	code := m.Run()
	os.RemoveAll(private)
	os.Exit(code)
}

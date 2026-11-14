package supervise

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/session"
)

// No test takes a claim in the person's own session directory.
func TestMain(m *testing.M) {
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

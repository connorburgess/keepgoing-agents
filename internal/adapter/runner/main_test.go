package runner

import (
	"os"
	"testing"
)

// The Windows runner re-runs the current executable as the turn wrapper;
// under go test that executable is the test binary.
func TestMain(m *testing.M) {
	MaybeRunTurn()
	os.Exit(m.Run())
}

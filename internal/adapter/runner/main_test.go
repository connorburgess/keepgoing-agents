package runner

import (
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The runner re-runs the current executable as the turn wrapper;
// under go test that executable is the test binary.
func TestMain(m *testing.M) {
	MaybeRunTurn()
	switch os.Getenv("KEEPGOING_TEST_HELPER") {
	case "environment":
		input, _ := io.ReadAll(os.Stdin)
		value := os.Getenv(envTurnDir) + "|" + os.Getenv(envTurnArgv) + "|" + os.Getenv(envTurnKey) + "|" + string(input)
		if os.WriteFile(os.Getenv("KEEPGOING_TEST_RESULT"), []byte(value), 0600) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "tree":
		self, _ := os.Executable()
		cmd := exec.Command(self)
		cmd.Env = append(os.Environ(), "KEEPGOING_TEST_HELPER=leaf")
		if cmd.Run() != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "leaf":
		path := os.Getenv("KEEPGOING_TEST_RESULT")
		if os.WriteFile(path+".ready", nil, 0600) != nil {
			os.Exit(2)
		}
		time.Sleep(2 * time.Second)
		_ = os.WriteFile(path, nil, 0600)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

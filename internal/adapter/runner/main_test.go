package runner

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// The runner re-runs the current executable as the turn wrapper;
// under go test that executable is the test binary.
func TestMain(m *testing.M) {
	MaybeRunTurn()
	switch os.Getenv("KEEPGOING_TEST_HELPER") {
	case "antigravity-acp":
		antigravityStandIn()
	case "cursor-crash":
		// The fixture exits on release or its own deadline even if the wrapper
		// dies. No surviving child writes to a later turn's stdout file.
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdout.WriteString(cursorInit + "\n")
		ready := os.Getenv("KEEPGOING_TEST_RESULT")
		if os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
			os.Exit(2)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(ready + ".release"); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(3)
	case "cursor":
		// The version query is not a turn: it records nothing and reads no prompt.
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			switch v := os.Getenv("KEEPGOING_CURSOR_FIXTURE_VERSION"); v {
			case "":
				_, _ = os.Stdout.WriteString("2026.09.18-9a7762b\n")
			case "fail":
				os.Exit(1)
			default:
				_, _ = os.Stdout.WriteString(v + "\n")
			}
			os.Exit(0)
		}
		input, _ := io.ReadAll(os.Stdin)
		cwd, _ := os.Getwd()
		capture, _ := json.Marshal(map[string]any{"args": os.Args[1:], "prompt": string(input), "cwd": cwd, "value": os.Getenv("KEEPGOING_CURSOR_FIXTURE_VALUE")})
		if os.WriteFile(os.Getenv("KEEPGOING_TEST_RESULT"), capture, 0600) != nil {
			os.Exit(2)
		}
		if os.Getenv("KEEPGOING_CURSOR_FIXTURE_WAIT") != "" {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(os.Getenv("KEEPGOING_CURSOR_FIXTURE_WAIT")); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		_, _ = os.Stdout.WriteString(os.Getenv("KEEPGOING_CURSOR_FIXTURE_STDOUT"))
		_, _ = os.Stderr.WriteString("fictional rate limit 429 authentication network failure")
		code, _ := strconv.Atoi(os.Getenv("KEEPGOING_CURSOR_FIXTURE_EXIT"))
		os.Exit(code)
	case "environment":
		input, _ := io.ReadAll(os.Stdin)
		value := os.Getenv(envTurnDir) + os.Getenv(envTurnACP) + "|" + os.Getenv(envTurnArgv) + "|" + os.Getenv(envTurnKey) + "|" + string(input)
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

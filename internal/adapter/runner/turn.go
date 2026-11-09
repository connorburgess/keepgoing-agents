package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Platforms without a POSIX shell run each turn through a copy of the
// current executable acting as the wrapper: it feeds the prompt to the
// agent, captures its output, and records the exit code atomically, as the
// shell wrapper does on Unix. The wrapper is selected by these variables.
const (
	envTurnDir  = "KEEPGOING_RUN_TURN_DIR"
	envTurnArgv = "KEEPGOING_RUN_TURN_ARGV"
)

// MaybeRunTurn runs the turn wrapper and exits when this process was started
// as one. Call it first thing in main, and in TestMain for tests that spawn
// turns. It returns normally otherwise.
func MaybeRunTurn() {
	dir := os.Getenv(envTurnDir)
	if dir == "" {
		return
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(envTurnArgv)), &argv); err != nil || len(argv) == 0 {
		os.Exit(2)
	}
	os.Exit(runTurn(dir, argv))
}

// runTurn executes argv with the prompt on stdin and records the exit code.
func runTurn(dir string, argv []string) int {
	stdin, err := os.Open(filepath.Join(dir, "prompt"))
	if err != nil {
		return 2
	}
	defer stdin.Close()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		return 2
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		return 2
	}
	defer stderr.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	// The wrapper must not pass its own selection variables to the agent.
	for _, kv := range os.Environ() {
		if !hasPrefix(kv, envTurnDir+"=") && !hasPrefix(kv, envTurnArgv+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			fmt.Fprintln(stderr, "keepgoing turn wrapper:", err)
			code = 127
		}
	}
	tmp := filepath.Join(dir, "exit.tmp")
	if os.WriteFile(tmp, []byte(strconv.Itoa(code)+"\n"), 0o600) == nil {
		os.Rename(tmp, filepath.Join(dir, "exit"))
	}
	return code
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// wrapperCommand builds the command that re-runs this executable as the
// turn wrapper for argv.
func wrapperCommand(argv []string, workdir, dir string, env []string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(argv)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self)
	cmd.Dir = workdir
	cmd.Env = append(append(os.Environ(), env...), envTurnDir+"="+dir, envTurnArgv+"="+string(encoded))
	return cmd, nil
}

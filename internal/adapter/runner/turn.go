package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Every turn runs through a detached copy of the executable. The live wrapper
// owns its cancellation boundary; callers only submit a private per-turn request.
const (
	envTurnDir  = "KEEPGOING_RUN_TURN_DIR"
	envTurnArgv = "KEEPGOING_RUN_TURN_ARGV"
	envTurnKey  = "KEEPGOING_RUN_TURN_KEY"
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
	key := os.Getenv(envTurnKey)
	if !validStopID(key) {
		os.Exit(2)
	}
	terminate, err := ownedTerminator()
	if err != nil {
		os.Exit(2)
	}
	done := make(chan struct{})
	go watchStop(Dir(dir), key, terminate, done)
	var code int
	if spec := os.Getenv(envTurnACP); spec != "" {
		code = runAntigravityACPTurn(dir, argv, spec)
	} else {
		code = runTurn(dir, argv)
	}
	close(done)
	os.Exit(code)
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
	cmd.Env = turnChildEnv()
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

// turnChildEnv is the agent's environment: the wrapper's own, without the
// variables that select and configure the wrapper.
func turnChildEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.EqualFold(name, envTurnDir) && !strings.EqualFold(name, envTurnArgv) && !strings.EqualFold(name, envTurnKey) && !strings.EqualFold(name, envTurnACP) {
			env = append(env, kv)
		}
	}
	return env
}

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
	configureWrapper(cmd)
	cmd.Dir = workdir
	cmd.Env = append(append(os.Environ(), env...), envTurnDir+"="+dir, envTurnArgv+"="+string(encoded))
	return cmd, nil
}

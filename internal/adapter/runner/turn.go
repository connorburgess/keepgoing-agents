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
	// envTurnStdin set to "none" gives the agent an empty standard input:
	// its prompt is in the private file it was pointed at.
	envTurnStdin = "KEEPGOING_RUN_TURN_STDIN"
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
	source := filepath.Join(dir, "prompt")
	if os.Getenv(envTurnStdin) == "none" {
		source = os.DevNull
	}
	stdin, err := os.Open(source)
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
		if !strings.EqualFold(name, envTurnDir) && !strings.EqualFold(name, envTurnArgv) && !strings.EqualFold(name, envTurnKey) && !strings.EqualFold(name, envTurnACP) && !strings.EqualFold(name, envTurnStdin) {
			env = append(env, kv)
		}
	}
	return env
}

// wrapperCommand builds the command that re-runs this executable as the
// turn wrapper for argv.
//
// acp selects the ACP worker and is the only way to do so. The variable that
// carries it is removed from the inherited environment and from the loop's
// own entries first, so a value set anywhere else can never divert a turn of
// any runner into that worker.
func wrapperCommand(argv []string, workdir, dir string, env []string, acp string) (*exec.Cmd, error) {
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
	for _, kv := range append(os.Environ(), env...) {
		if name, _, _ := strings.Cut(kv, "="); !strings.EqualFold(name, envTurnACP) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, envTurnDir+"="+dir, envTurnArgv+"="+string(encoded))
	if acp != "" {
		cmd.Env = append(cmd.Env, envTurnACP+"="+acp)
	}
	return cmd, nil
}

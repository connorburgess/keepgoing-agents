package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/looptender/looptender/internal/envvar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/looptender/looptender/internal/lock"
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
	// Asked which wrapper this binary is: answer and run nothing.
	if os.Getenv(envTurnProbe) != "" {
		fmt.Println(turnProtocol)
		os.Exit(0)
	}
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
	// Hold the turn lock until the exit code is published. The operating system
	// releases it when this process ends, so a reader never has to trust a
	// saved PID. Start holds the lock while it sets this turn up, so wait for it,
	// then write this turn's key so Start knows which wrapper has it. The
	// record must name this turn: one that names another, or that Start has
	// marked abandoned, means this wrapper is not wanted and runs nothing.
	turnKey = key
	if !holdTurnLock(dir) {
		os.Exit(2)
	}
	if rec, err := Dir(dir).record(); err != nil || rec.StopKey != key || rec.Abandoned {
		if onDecline != nil {
			onDecline()
		}
		os.Exit(2)
	}
	if os.WriteFile(filepath.Join(dir, startedName), []byte(key), 0o600) != nil {
		os.Exit(2)
	}
	stopAll := func() error {
		killOwnedChild()
		return terminate()
	}
	drainStopWatcher = startStopWatcher(Dir(dir), key, stopAll)
	code := runOwnedTurn(dir, argv, os.Getenv(envTurnACP), drainStopWatcher)
	os.Exit(code)
}

// runOwnedTurn joins the owned watcher even when setup of the agent's IO
// fails before publishExit. Publication itself drains earlier, before unlock.
func runOwnedTurn(dir string, argv []string, acp string, drain func() bool) int {
	defer drain()
	if acp != "" {
		return runAntigravityACPTurn(dir, argv, acp)
	}
	return runTurn(dir, argv)
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
	// Close the output files before the exit code is published. Once a reader
	// can see the code, the next start may move these files, and on Windows
	// a file that is still open cannot be moved.
	stdin.Close()
	stdout.Close()
	stderr.Close()
	tmp := filepath.Join(dir, "exit.tmp")
	if os.WriteFile(tmp, []byte(strconv.Itoa(code)+"\n"), 0o600) == nil {
		publishExit(dir, tmp)
	}
	return code
}

// turnChildEnv is the agent's environment: the wrapper's own, without the
// variables that select and configure the wrapper.
func turnChildEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.EqualFold(name, envTurnDir) && !strings.EqualFold(name, envTurnArgv) && !strings.EqualFold(name, envTurnKey) && !strings.EqualFold(name, envTurnACP) && !strings.EqualFold(name, envTurnProbe) && !strings.EqualFold(name, envTurnStdin) {
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
	// The session a supervisor was started from is not this turn's: a turn
	// started by a supervisor launched from a BB thread is not that thread
	// or its agent session. An entry the loop's configuration sets in env
	// comes after, so it is kept.
	for _, kv := range append(envvar.WithoutSupervisorSession(os.Environ()), env...) {
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

// turnLockHeld owns setup exclusion through publication and finishing cleanup.
// turnKey is this wrapper's own turn key.
var (
	turnLockHeld *lock.Lock
	turnKey      string
)

// afterPublish runs once the exit code is visible, and onDecline when a
// wrapper finds it is not wanted. Only tests set them.
var afterPublish, onDecline func()

// Initialized only in the owned wrapper, before the agent begins. It reports
// whether the wrapper accepted a stop.
var drainStopWatcher func() bool

var (
	ownedChildMu   sync.Mutex
	ownedChildKill func()
)

// registerOwnedChildTerminator lets a worker that runs its child in a separate
// process group or nested job ensure wrapper Stop ends that tree as well.
func registerOwnedChildTerminator(fn func()) func() {
	ownedChildMu.Lock()
	ownedChildKill = fn
	ownedChildMu.Unlock()
	return func() {
		ownedChildMu.Lock()
		ownedChildKill = nil
		ownedChildMu.Unlock()
	}
}

func killOwnedChild() {
	ownedChildMu.Lock()
	fn := ownedChildKill
	ownedChildMu.Unlock()
	if fn != nil {
		fn()
	}
}

// errStoppedTurn refuses publication for a turn the wrapper was stopping.
var errStoppedTurn = errors.New("runner: turn was stopped; no exit code is published")

// Publication boundary hooks are set only by deterministic lifecycle tests.
var beforeExitRename, afterExitRename, afterTurnRelease, afterFinishingRead func()

func holdTurnLock(dir string) bool {
	held, err := acquireTurnLock(dir)
	turnLockHeld = held
	return err == nil
}

// publishExit owns turn.lock through exit publication and finishing cleanup.
// After release it performs no shared-file IO: a later setup can immediately
// own the directory. Status defers Finished until this publisher releases.
func publishExit(dir, tmp string) (result error) {
	held := turnLockHeld
	if held == nil {
		return errors.New("runner: exit publisher does not hold the turn lock")
	}
	defer func() {
		releaseErr := held.Release()
		turnLockHeld = nil
		if result == nil {
			result = releaseErr
		}
		if afterTurnRelease != nil {
			afterTurnRelease()
		}
		if afterPublish != nil {
			afterPublish()
		}
	}()
	// A stop terminates the wrapper's whole process group (on Windows, its
	// job), the wrapper included, but the members do not all end at the same
	// moment: the wrapper can see its agent exit before its own termination
	// arrives. Publishing then would make a stopped turn read as finished and
	// its partial output be read as the turn's result, depending on which
	// process the signal reached first. A stopped turn has no exit code: it
	// reads as crashed every time. Joining the watcher first settles whether
	// a stop was accepted.
	if drainStopWatcher != nil && drainStopWatcher() {
		// The watcher is settled and turn.lock still excludes the next setup.
		_ = os.Remove(tmp)
		return errStoppedTurn
	}
	rec, err := Dir(dir).record()
	if err != nil || rec.StopKey != turnKey || rec.Abandoned {
		return errors.New("runner: exit publisher no longer owns the turn")
	}
	finishing := filepath.Join(dir, finishingName)
	if err := os.WriteFile(finishing, []byte(turnKey), 0o600); err != nil {
		return err
	}
	if beforeExitRename != nil {
		beforeExitRename()
	}
	if err := os.Rename(tmp, filepath.Join(dir, "exit")); err != nil {
		return err
	}
	if afterExitRename != nil {
		afterExitRename()
	}
	if b, err := os.ReadFile(finishing); err == nil && string(b) == turnKey {
		if afterFinishingRead != nil {
			afterFinishingRead()
		}
		if err := os.Remove(finishing); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

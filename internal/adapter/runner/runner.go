// Package runner starts one headless agent turn as a detached process and
// lets later ticks find out how it ended. A tick never waits for a turn: it
// starts a wrapper process, waits only until that wrapper holds the turn
// lock, and exits. The wrapper writes the agent's stdout and exit status into
// the target's directory. A turn is running while its wrapper holds the lock;
// the saved PID is consulted only for a record an older build wrote.
//
// Start and Status for one directory are called under the loop's tick lock.
// The turn lock makes a turn's liveness readable; it does not make Start
// single-flight.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// Phase is what the runner knows about the latest turn.
type Phase string

// Phases.
const (
	NeverRun Phase = "never_run"
	Running  Phase = "running"
	Finished Phase = "finished"
	Crashed  Phase = "crashed" // process gone without recording an exit code
)

// Record is run.json.
type Record struct {
	StopKey   string    `json:"stopKey,omitempty"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Argv0     string    `json:"argv0"`
	// Liveness says how to tell that this turn's wrapper is still running.
	// "lock" means the wrapper holds turn.lock until it publishes its exit
	// code, and loses it if it ends any other way, so the
	// operating system releases it when the wrapper ends, however it ends.
	// A record without it was written by an older build, whose wrapper holds
	// no lock; only for those is the saved PID still consulted.
	Liveness string `json:"liveness,omitempty"`
	// Abandoned is set by Start when the wrapper did not take the lock in
	// time. A wrapper that finds it runs nothing, and the turn reads as
	// crashed at once, so it is recovered like any turn that died.
	Abandoned bool `json:"abandoned,omitempty"`
}

const (
	livenessLock = "lock"
	turnLock     = "turn" // listed in migrateLockNames (cmd/keepgoing/migrate.go); so must any new lock file
)

// wrapperStartTimeout bounds two waits: Start's wait for the turn lock, and
// its wait for the new wrapper to take that lock. Tests shorten it.
var wrapperStartTimeout = 10 * time.Second

// bootSlack is how much earlier than the machine's start a legacy record must
// be before it is taken as written before that start.
const bootSlack = time.Minute

// machineStarted is when this machine started, when that can be told.
var machineStarted = bootTime

// errnoText keeps the operating system's error number without the path that
// usually comes with it.
func errnoText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf(" (errno %d)", int(errno))
	}
	return ""
}

// wrapperLive reports whether the wrapper of the recorded turn is running.
// For a current record that means its lock is held, never that a PID exists:
// after a reboot the saved PID can belong to an unrelated process.
func (d Dir) wrapperLive(rec Record) (bool, error) {
	if rec.Liveness != livenessLock {
		// An older build's record. Its PID is all there is, except that no
		// process outlives a restart of the machine: a record from before the
		// machine started names a turn that is gone, whatever has its PID now.
		if !alive(rec.PID) {
			return false, nil
		}
		if boot, ok := machineStarted(); ok && !rec.StartedAt.IsZero() && rec.StartedAt.Before(boot.Add(-bootSlack)) {
			return false, nil
		}
		return true, nil
	}
	held, err := lock.AcquireNamed(string(d), turnLock)
	if err == nil {
		return false, held.Release()
	}
	if errors.Is(err, lock.ErrHeld) {
		return true, nil
	}
	return false, errors.New("runner: the turn lock could not be checked" + errnoText(err))
}

// acquireTurnLock takes the turn lock, waiting for a wrapper that is still
// exiting to let go of it.
func acquireTurnLock(dir string) (*lock.Lock, error) {
	for deadline := time.Now().Add(wrapperStartTimeout); ; time.Sleep(5 * time.Millisecond) {
		held, err := lock.AcquireNamed(dir, turnLock)
		if err == nil {
			return held, nil
		}
		if !errors.Is(err, lock.ErrHeld) || time.Now().After(deadline) {
			return nil, errors.New("runner: the turn lock is not available" + errnoText(err))
		}
	}
}

// startedName is the file a wrapper writes, holding its turn key, once it
// holds the turn lock. finishingName is the file it writes just before it
// lets go of the lock to publish its exit code.
const (
	startedName   = "turn.started"
	finishingName = "turn.finishing"
)

// finishingWait bounds how long a reader waits for an exit code a wrapper
// was in the middle of publishing.
var finishingWait = 2 * time.Second

// errSuperseded says another Start replaced this turn's record.
var errSuperseded = errors.New("runner: another start replaced this turn")

// errNoWrapper says the wrapper has not taken the lock yet.
var errNoWrapper = errors.New("runner: the turn wrapper did not start")

// record reads run.json.
func (d Dir) record() (Record, error) {
	var rec Record
	b, err := os.ReadFile(d.path("run.json"))
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(b, &rec)
}

// saveRecord replaces run.json whole, so a reader never sees part of one and
// a failed write leaves the previous record.
func (d Dir) saveRecord(rec Record) error {
	b, _ := json.Marshal(rec)
	tmp := d.path("run.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, d.path("run.json")); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// wrapperStarted reports whether this turn's own wrapper has taken the lock
// or the turn has already finished. It matches the turn key, so the previous
// turn's wrapper is never mistaken for it.
func (d Dir) wrapperStarted(key string) bool {
	if _, err := os.Stat(d.path("exit")); err == nil {
		return true
	}
	b, err := os.ReadFile(d.path(startedName))
	return err == nil && string(b) == key
}

// awaitWrapper returns nil once this turn's own wrapper holds the lock or the
// turn has finished, errSuperseded when the record no longer names this turn,
// and errNoWrapper at the limit.
func (d Dir) awaitWrapper(key string, limit time.Duration) error {
	for deadline := time.Now().Add(limit); ; time.Sleep(5 * time.Millisecond) {
		if d.wrapperStarted(key) {
			return nil
		}
		if rec, err := d.record(); err == nil && rec.StopKey != key {
			return errSuperseded
		}
		if time.Now().After(deadline) {
			return errNoWrapper
		}
	}
}

// abandon ends a turn whose wrapper did not take the lock in time. It takes
// the lock itself, so the wrapper cannot be starting meanwhile, and marks the
// record. A wrapper that arrives later finds the mark under the lock and runs
// nothing. It returns nil when the wrapper turned out to have started.
func (d Dir) abandon(key string) error {
	var held *lock.Lock
	for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
		var err error
		if held, err = lock.AcquireNamed(string(d), turnLock); err == nil {
			break
		}
		// Held: by this turn's wrapper, which is about to say so, or not.
		if d.wrapperStarted(key) {
			return nil
		}
		if !errors.Is(err, lock.ErrHeld) || time.Now().After(deadline) {
			return errors.New("runner: the turn wrapper did not start and the turn lock is not available" + errnoText(err))
		}
	}
	defer held.Release()
	if d.wrapperStarted(key) {
		return nil
	}
	rec, err := d.record()
	if err != nil || rec.StopKey != key {
		return errSuperseded
	}
	rec.Abandoned = true
	if err := d.saveRecord(rec); err != nil {
		return errors.New("runner: the turn wrapper did not start and its record could not be marked" + errnoText(err))
	}
	return notSpawned{errNoWrapper}
}

// Status is the latest turn's outcome.
type Status struct {
	ExitCodeKnown bool
	Phase         Phase
	ExitCode      int
	Stdout        []byte
	Record        Record
	// Incomplete: a start that failed set this turn's files aside and could
	// not put them all back. The turn reads as crashed, and whatever output
	// is left is not its whole result.
	Incomplete bool
}

// Dir is one target's runner directory.
type Dir string

func (d Dir) path(name string) string { return filepath.Join(string(d), name) }

// Status inspects the latest turn.
func (d Dir) Status() (Status, error) { return d.statusWithOutputLimit(0) }

// statusWithOutputLimit bounds stdout loading for adapters with a finite stream
// contract. Zero preserves the legacy adapters' output behavior.
func (d Dir) statusWithOutputLimit(limit int64) (Status, error) {
	var st Status
	b, err := os.ReadFile(d.path("run.json"))
	if errors.Is(err, os.ErrNotExist) {
		st.Phase = NeverRun
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st.Record); err != nil {
		return st, err
	}
	finished := func() (bool, error) {
		code, err := os.ReadFile(d.path("exit"))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			// Something is there that is not a readable exit code. Say so
			// rather than guess between finished and crashed.
			return false, errors.New("runner: the turn's exit status could not be read" + errnoText(err))
		}
		st.Phase = Finished
		var parseErr error
		st.ExitCode, parseErr = strconv.Atoi(strings.TrimSpace(string(code)))
		st.ExitCodeKnown = parseErr == nil && st.ExitCode >= 0
		st.Stdout, err = d.readOutput(limit)
		return true, err
	}
	if done, err := finished(); done || err != nil {
		return st, err
	}
	crashed := func() (Status, error) {
		st.Phase = Crashed
		if _, err := os.Stat(d.aside("exit")); err == nil {
			st.Incomplete = true
		}
		var err error
		st.Stdout, err = d.readOutput(limit)
		return st, err
	}
	if st.Record.Abandoned {
		return crashed()
	}
	live, err := d.wrapperLive(st.Record)
	if err != nil {
		return st, err
	}
	if live {
		st.Phase = Running
		return st, nil
	}
	// The wrapper lets go of the lock just before it publishes its exit code.
	// If it was doing that, wait for the code instead of reading a crash. The
	// wait ends finishingWait after the marker was written, so a marker left
	// by a wrapper that died there costs a later read nothing.
	until := time.Now()
	if info, err := os.Stat(d.path(finishingName)); err == nil {
		if end := info.ModTime().Add(finishingWait); end.Before(until.Add(finishingWait)) {
			until = end
		} else {
			until = until.Add(finishingWait)
		}
	}
	for {
		// Also covers a code published between the first look and the lock check.
		if done, err := finished(); done || err != nil {
			return st, err
		}
		if !time.Now().Before(until) {
			return crashed()
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Session returns the saved session id, if any.
func (d Dir) Session() string {
	b, _ := os.ReadFile(d.path("session"))
	return strings.TrimSpace(string(b))
}

// SaveSession stores the session id so the next turn can resume it.
func (d Dir) SaveSession(id string) error {
	if id == "" {
		return nil
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	return os.WriteFile(d.path("session"), []byte(id+"\n"), 0o600)
}

// Start launches argv in workdir with prompt on stdin, detached from the
// calling process so it survives the tick. It returns once the turn's wrapper
// holds the turn lock or the turn has finished.
//
// When a turn is still running it waits for the lock up to
// wrapperStartTimeout, then refuses and changes nothing. When the new wrapper
// does not take the lock in that time again, the turn is abandoned: its
// record is marked, a wrapper that arrives later runs nothing, the turn reads
// as crashed, and Start returns an error.
//
// Callers must hold the loop's tick lock, as every check does. Start is not
// single-flight on its own: two concurrent Starts never run two agents at
// once, but each can run its turn, one after the other.
func (d Dir) Start(argv []string, workdir, prompt string, env []string) error {
	_, err := d.start(argv, workdir, prompt, env, "")
	return err
}

// turnFiles are what one turn leaves, the exit code last: it is what makes a
// turn read as finished, so it is the last to go and the last to come back.
var turnFiles = []string{"prompt", "exit.tmp", "stdout", "stderr", "stop-request.json", "stop-ack.json", startedName, finishingName, waitAdoptedFile, "exit"}

// renameFile is os.Rename. Tests replace it to make one file immovable.
var renameFile = os.Rename

func (d Dir) aside(name string) string { return d.path(name + ".old") }

// agentHeld are the files a turn's agent is given: its standard input and
// output. Only these may be left in place when they are in use.
var agentHeld = map[string]bool{"prompt": true, "stdout": true, "stderr": true}

// fileInUse reports whether a move failed only because another process has
// the file open. Tests replace it.
var fileInUse = openElsewhere

// setAside moves the previous turn's files out of the way, all of them or
// none. A stale exit code would make the new turn read as finished before it
// ran, and an exit code removed while another file stayed would make a
// finished turn read as crashed. The files are only deleted once the new
// turn's record is saved; until then a start that fails puts them back.
func (d Dir) setAside() ([]string, error) {
	var moved []string
	for _, name := range turnFiles {
		err := renameFile(d.path(name), d.aside(name))
		if err == nil {
			moved = append(moved, name)
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		// The prompt and the output files are handed to the agent. An agent
		// that outlived its wrapper can still hold them open, and on Windows
		// an open file cannot be moved. Those stay where they are and the new
		// turn overwrites them, as it always did. Nothing that decides how a
		// turn reads (its exit code, keys and markers) is ever left.
		if agentHeld[name] && fileInUse(err) {
			continue
		}
		err = errors.New("runner: the previous turn's files could not be cleared" + errnoText(err))
		if back := d.putBack(moved); back != nil {
			err = fmt.Errorf("%v; %v", err, back)
		}
		return nil, err
	}
	return moved, nil
}

// putBack restores what setAside moved, after a start that failed. The exit
// code comes back last, and only when everything else did: a turn whose
// output could not be restored must not read as finished with empty output.
// It then reads as crashed, and Status says its result is incomplete.
func (d Dir) putBack(moved []string) error {
	var lost []string
	for _, name := range moved {
		if name == "exit" && len(lost) > 0 {
			lost = append(lost, name)
			continue
		}
		os.Remove(d.path(name)) // what the failed start wrote in its place
		if err := renameFile(d.aside(name), d.path(name)); err != nil {
			lost = append(lost, name+errnoText(err))
		}
	}
	if len(lost) == 0 {
		return nil
	}
	return errors.New("runner: the previous turn's files could not all be put back (" + strings.Join(lost, ", ") + "), so its result is incomplete and it now reads as failed")
}

// discardAside deletes everything set aside, including what an earlier start
// left when it could not put a file back.
func (d Dir) discardAside() {
	for _, name := range turnFiles {
		os.Remove(d.aside(name))
	}
}

// turnProtocol is what a binary answers when asked which turn wrapper it
// is. It names the contract between Start and the wrapper: what the wrapper
// locks, checks and writes. It changes only when that contract changes, not
// with every release, so two builds with the same label can be mixed. A
// binary that answers anything else, or nothing, would not keep the contract:
// started as a wrapper it could run the agent without the turn lock.
const (
	turnProtocol = "keepgoing-turn-wrapper 2"
	envTurnProbe = "KEEPGOING_RUN_TURN_PROBE"
)

// The two reasons a start is refused over the binary on disk. They are fixed
// texts: the check reports them and status and doctor repeat them.
const (
	ReasonWrapperDiffers = "the keepgoing binary on disk does not speak the turn-wrapper protocol of the running process (it is another build): stop the daemon, then start it again"
	ReasonWrapperSilent  = "the keepgoing binary on disk could not be asked which turn wrapper it is (it did not run, or did not answer in time): check that it is in place and executable, then restart the daemon"
)

// wrapperAskTimeout bounds the question. Tests shorten it.
var wrapperAskTimeout = 10 * time.Second

var agreed struct {
	sync.Mutex
	info os.FileInfo
}

// wrapperAgrees asks the binary on disk which wrapper it is, once per binary:
// the answer is kept until the file is replaced or changes. It starts no turn.
// A refusal is an adapter.ErrNotStarted: nothing was sent.
func wrapperAgrees(env []string) error {
	// A process that was itself started to answer the question starts no
	// turn. This only matters for a program that does not call MaybeRunTurn
	// first: it stops the question from being asked again by its own copy.
	if os.Getenv(envTurnProbe) != "" {
		return errors.New("runner: this program does not answer as a turn wrapper; call MaybeRunTurn first in main")
	}
	self, err := os.Executable()
	if err != nil {
		return adapter.ErrNotStarted{Reason: ReasonWrapperSilent}
	}
	info, err := os.Stat(self)
	if err != nil {
		return adapter.ErrNotStarted{Reason: ReasonWrapperSilent}
	}
	agreed.Lock()
	defer agreed.Unlock()
	// The same file, by the platform's own identity where it has one, with
	// the same size and modification time.
	if agreed.info != nil && os.SameFile(agreed.info, info) && agreed.info.Size() == info.Size() && agreed.info.ModTime().Equal(info.ModTime()) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), wrapperAskTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, self)
	cmd.Env = append(append(os.Environ(), env...), envTurnProbe+"=1")
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, left: 256}
	runErr := cmd.Run()
	var exited *exec.ExitError
	switch {
	case ctx.Err() != nil, runErr != nil && !errors.As(runErr, &exited):
		// It was not started, or it was cut off: no answer either way.
		return adapter.ErrNotStarted{Reason: ReasonWrapperSilent}
	case runErr != nil, strings.TrimSpace(out.String()) != turnProtocol:
		return adapter.ErrNotStarted{Reason: ReasonWrapperDiffers}
	}
	agreed.info = info
	return nil
}

// limitedWriter keeps the first bytes written and discards the rest.
type limitedWriter struct {
	w    io.Writer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if n := min(len(p), l.left); n > 0 {
		l.w.Write(p[:n])
		l.left -= n
	}
	return len(p), nil
}

// notSpawned marks a start after which no process runs or will run the turn:
// none was created, or the wrapper was shut out before it began. Its text is
// the underlying error's.
type notSpawned struct{ error }

func (e notSpawned) Unwrap() error { return e.error }

// start is Start with an optional ACP worker specification. It returns the
// new turn's stop key, which identifies the worker that was started.
func (d Dir) start(argv []string, workdir, prompt string, env []string, acp string) (string, error) {
	return d.startTurn(argv, workdir, prompt, env, acp, false)
}

// StartWithPromptFile is Start for an agent that reads its prompt from the
// private prompt file it was pointed at: its standard input is empty.
func (d Dir) StartWithPromptFile(argv []string, workdir, prompt string, env []string) error {
	_, err := d.startTurn(argv, workdir, prompt, env, "", true)
	return err
}

// startTurn says explicitly what the turn's standard input is, after removing
// the variable that carries it from the loop's own entries, so a value set
// anywhere else can never take a prompt away from a turn.
func (d Dir) startTurn(argv []string, workdir, prompt string, env []string, acp string, emptyStdin bool) (string, error) {
	kept := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !strings.EqualFold(name, envTurnStdin) {
			kept = append(kept, kv)
		}
	}
	stdin := "prompt"
	if emptyStdin {
		stdin = "none"
	}
	env = append(kept, envTurnStdin+"="+stdin)
	if len(argv) == 0 {
		return "", notSpawned{errors.New("runner: empty command")}
	}
	// The binary on disk is the wrapper. If it does not keep this process's
	// wrapper contract it could run the agent without taking the turn lock:
	// refuse before anything changes.
	if err := wrapperAgrees(env); err != nil {
		return "", notSpawned{err}
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return "", notSpawned{err}
	}
	// Hold the turn lock while the new turn is set up. This waits for the
	// previous turn's wrapper to finish exiting, so its lock can never stand
	// in for the new wrapper's, and it is released only after the new record
	// is written. The new wrapper then takes it and says so.
	setup, err := acquireTurnLock(string(d))
	if err != nil {
		return "", notSpawned{err}
	}
	defer setup.Release()
	moved, err := d.setAside()
	if err != nil {
		return "", notSpawned{err}
	}
	// Until the new turn's record is saved, a failure puts the previous turn
	// back as it was: a start that could not happen must not turn a finished
	// turn into a crashed one.
	fail := func(err error) (string, error) {
		if back := d.putBack(moved); back != nil {
			err = fmt.Errorf("%v; %v", err, back)
		}
		return "", notSpawned{err}
	}
	if err := os.WriteFile(d.path("prompt"), []byte(prompt), 0o600); err != nil {
		return fail(errors.New("runner: the prompt could not be written" + errnoText(err)))
	}
	key, err := randomStopID()
	if err != nil {
		return fail(err)
	}
	pid, err := spawn(argv, workdir, string(d), append(env, envTurnKey+"="+key), acp)
	if err != nil {
		return fail(err)
	}
	rec := Record{StopKey: key, PID: pid, StartedAt: time.Now().UTC(), Argv0: filepath.Base(argv[0]), Liveness: livenessLock}
	if err := d.saveRecord(rec); err != nil {
		// The previous record stands. The wrapper that was started takes the
		// lock, finds that no record names it, and runs nothing.
		return fail(errors.New("runner: the turn record could not be saved" + errnoText(err)))
	}
	d.discardAside()
	// A release that failed shows up as a wrapper that cannot start.
	_ = setup.Release()
	switch err := d.awaitWrapper(key, wrapperStartTimeout); err {
	case nil:
		return key, nil
	case errNoWrapper:
		if err := d.abandon(key); err != nil {
			return "", err
		}
		return key, nil
	default:
		return "", err
	}
}

// Stop asks the live wrapper to stop its own turn. Saved PIDs never authorize signals.
func (d Dir) Stop() error {
	// Only the turn's phase and key are needed: its output is not read.
	st, err := d.statusWithOutputLimit(skipOutput)
	if err != nil || st.Phase != Running {
		return err
	}
	return d.requestStop(st.Record.StopKey)
}

// waitAdoptedFile records that the awaiting-user hold for a turn that ended
// waiting on a person has been saved. It names the turn, so it cannot apply
// to a later one, and a new turn removes it.
const waitAdoptedFile = "wait-adopted"

func waitKey(rec Record) string {
	return rec.StopKey + "|" + rec.StartedAt.UTC().Format(time.RFC3339Nano)
}

func (d Dir) waitAdopted(rec Record) bool {
	b, err := os.ReadFile(d.path(waitAdoptedFile))
	return err == nil && strings.TrimSpace(string(b)) == waitKey(rec)
}

func (d Dir) adoptWait(rec Record) error {
	return os.WriteFile(d.path(waitAdoptedFile), []byte(waitKey(rec)+"\n"), 0o600)
}

// errOutputOverLimit says a turn printed more than its adapter reads.
var errOutputOverLimit = errors.New("runner: turn output exceeds the adapter limit")

// envList supplies overrides to subprocesses without changing the supervisor's
// environment or persisting values in turn metadata.
func envList(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for k, v := range env {
		entries = append(entries, k+"="+v)
	}
	return entries
}

// skipOutput, as an output limit, reads no output at all.
const skipOutput = -1

func (d Dir) readOutput(limit int64) ([]byte, error) {
	if limit == skipOutput {
		return nil, nil
	}
	if limit == 0 {
		b, _ := os.ReadFile(d.path("stdout"))
		return b, nil
	}
	info, err := os.Lstat(d.path("stdout"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("runner: turn output is not an accessible regular file")
	}
	f, err := os.Open(d.path("stdout"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("runner: turn output is unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errors.New("runner: turn output could not be read")
	}
	if int64(len(b)) > limit {
		return nil, errOutputOverLimit
	}
	return b, nil
}

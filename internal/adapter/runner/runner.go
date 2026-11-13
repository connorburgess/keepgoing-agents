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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	turnLock     = "turn"
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

// notSpawned marks a start after which no process runs or will run the turn:
// none was created, or the wrapper was shut out before it began. Its text is
// the underlying error's.
type notSpawned struct{ error }

func (e notSpawned) Unwrap() error { return e.error }

// start is Start with an optional ACP worker specification. It returns the
// new turn's stop key, which identifies the worker that was started.
func (d Dir) start(argv []string, workdir, prompt string, env []string, acp string) (string, error) {
	if len(argv) == 0 {
		return "", notSpawned{errors.New("runner: empty command")}
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
	// The previous turn's exit code goes first. If it cannot be removed the
	// new turn would read as finished, with that code, before it had run: so
	// refuse, with everything still as it was.
	for _, name := range []string{"exit", "exit.tmp", "stdout", "stderr", "stop-request.json", "stop-ack.json", startedName, finishingName} {
		if err := os.Remove(d.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", notSpawned{errors.New("runner: the previous turn's files could not be cleared" + errnoText(err))}
		}
	}
	if err := os.WriteFile(d.path("prompt"), []byte(prompt), 0o600); err != nil {
		return "", notSpawned{err}
	}
	key, err := randomStopID()
	if err != nil {
		return "", notSpawned{err}
	}
	pid, err := spawn(argv, workdir, string(d), append(env, envTurnKey+"="+key), acp)
	if err != nil {
		return "", notSpawned{err}
	}
	rec := Record{StopKey: key, PID: pid, StartedAt: time.Now().UTC(), Argv0: filepath.Base(argv[0]), Liveness: livenessLock}
	if err := d.saveRecord(rec); err != nil {
		// The previous record stands. The wrapper that was started takes the
		// lock, finds that no record names it, and runs nothing.
		return "", notSpawned{errors.New("runner: the turn record could not be saved" + errnoText(err))}
	}
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
	st, err := d.Status()
	if err != nil || st.Phase != Running {
		return err
	}
	return d.requestStop(st.Record.StopKey)
}

// envList supplies overrides to subprocesses without changing the supervisor's
// environment or persisting values in turn metadata.
func envList(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for k, v := range env {
		entries = append(entries, k+"="+v)
	}
	return entries
}

func (d Dir) readOutput(limit int64) ([]byte, error) {
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
		return nil, errors.New("runner: turn output exceeds the adapter limit")
	}
	return b, nil
}

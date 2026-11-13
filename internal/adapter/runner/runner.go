// Package runner starts one headless agent turn as a detached process and
// lets later ticks find out how it ended. A tick never waits for a turn: it
// starts the process, records its pid, and exits. The process writes its
// stdout and exit status into the target's directory.
package runner

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// "lock" means the wrapper holds turn.lock for its whole life, so the
	// operating system releases it when the wrapper ends, however it ends.
	// A record without it was written by an older build, whose wrapper holds
	// no lock; only for those is the saved PID still consulted.
	Liveness string `json:"liveness,omitempty"`
}

const (
	livenessLock = "lock"
	turnLock     = "turn"
)

// wrapperStartTimeout bounds how long Start waits for a new wrapper to take
// its lock. Tests shorten it.
var wrapperStartTimeout = 10 * time.Second

// wrapperLive reports whether the wrapper of the recorded turn is running.
// For a current record that means its lock is held, never that a PID exists:
// after a reboot the saved PID can belong to an unrelated process.
func (d Dir) wrapperLive(rec Record) (bool, error) {
	if rec.Liveness != livenessLock {
		return alive(rec.PID), nil
	}
	held, err := lock.AcquireNamed(string(d), turnLock)
	if err == nil {
		return false, held.Release()
	}
	if errors.Is(err, lock.ErrHeld) {
		return true, nil
	}
	return false, errors.New("runner: the turn lock could not be checked")
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
			return nil, errors.New("runner: the turn lock is not available")
		}
	}
}

// startedName is the file a wrapper writes, holding its turn key, once it
// holds the turn lock.
const startedName = "turn.started"

// awaitWrapper returns once this turn's own wrapper has taken the lock, or
// the turn has already finished. It matches the turn key, so the previous
// turn's wrapper still letting go of the lock is never mistaken for it.
func (d Dir) awaitWrapper(key string) error {
	for deadline := time.Now().Add(wrapperStartTimeout); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(d.path("exit")); err == nil {
			return nil
		}
		if b, err := os.ReadFile(d.path(startedName)); err == nil && string(b) == key {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("runner: the turn wrapper did not start")
		}
	}
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
	if code, err := os.ReadFile(d.path("exit")); err == nil {
		st.Phase = Finished
		var parseErr error
		st.ExitCode, parseErr = strconv.Atoi(strings.TrimSpace(string(code)))
		st.ExitCodeKnown = parseErr == nil && st.ExitCode >= 0
		st.Stdout, err = d.readOutput(limit)
		return st, err
	}
	live, err := d.wrapperLive(st.Record)
	if err != nil {
		return st, err
	}
	if live {
		st.Phase = Running
		return st, nil
	}
	st.Phase = Crashed
	st.Stdout, err = d.readOutput(limit)
	return st, err
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
// calling process so it survives the tick.
func (d Dir) Start(argv []string, workdir, prompt string, env []string) error {
	if len(argv) == 0 {
		return errors.New("runner: empty command")
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	// Hold the turn lock while the new turn is set up. This waits for the
	// previous turn's wrapper to finish exiting, so its lock can never stand
	// in for the new wrapper's, and it is released only after the new record
	// is written. The new wrapper then takes it and says so.
	setup, err := acquireTurnLock(string(d))
	if err != nil {
		return err
	}
	defer setup.Release()
	for _, name := range []string{"exit", "exit.tmp", "stdout", "stderr", "stop-request.json", "stop-ack.json", startedName} {
		os.Remove(d.path(name))
	}
	if err := os.WriteFile(d.path("prompt"), []byte(prompt), 0o600); err != nil {
		return err
	}
	key, err := randomStopID()
	if err != nil {
		return err
	}
	pid, err := spawn(argv, workdir, string(d), append(env, envTurnKey+"="+key))
	if err != nil {
		return err
	}
	rec := Record{StopKey: key, PID: pid, StartedAt: time.Now().UTC(), Argv0: filepath.Base(argv[0]), Liveness: livenessLock}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(d.path("run.json"), append(b, '\n'), 0o600); err != nil {
		// The wrapper owns cancellation even when its record could not be saved.
		_ = d.requestStop(key)
		return err
	}
	if err := setup.Release(); err != nil {
		return err
	}
	if err := d.awaitWrapper(key); err != nil {
		// A wrapper that starts late still finds this request and stops.
		_ = d.requestStop(key)
		return err
	}
	return nil
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

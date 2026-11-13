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
	if alive(st.Record.PID) {
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
	_, err := d.start(argv, workdir, prompt, env, "")
	return err
}

// notSpawned marks a start that created no process, so nothing can be running
// for it. Its text is the underlying error's.
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
	for _, name := range []string{"exit", "exit.tmp", "stdout", "stderr", "stop-request.json", "stop-ack.json"} {
		os.Remove(d.path(name))
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
	rec := Record{StopKey: key, PID: pid, StartedAt: time.Now().UTC(), Argv0: filepath.Base(argv[0])}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(d.path("run.json"), append(b, '\n'), 0o600); err != nil {
		// The wrapper owns cancellation even when its record could not be saved.
		_ = d.requestStop(key)
		return "", err
	}
	return key, nil
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

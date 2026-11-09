// Package runner starts one headless agent turn as a detached process and
// lets later ticks find out how it ended. A tick never waits for a turn: it
// starts the process, records its pid, and exits. The process writes its
// stdout and exit status into the target's directory.
package runner

import (
	"encoding/json"
	"errors"
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
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Argv0     string    `json:"argv0"`
}

// Status is the latest turn's outcome.
type Status struct {
	Phase    Phase
	ExitCode int
	Stdout   []byte
	Record   Record
}

// Dir is one target's runner directory.
type Dir string

func (d Dir) path(name string) string { return filepath.Join(string(d), name) }

// Status inspects the latest turn.
func (d Dir) Status() (Status, error) {
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
		st.ExitCode, _ = strconv.Atoi(strings.TrimSpace(string(code)))
		st.Stdout, _ = os.ReadFile(d.path("stdout"))
		return st, nil
	}
	if alive(st.Record.PID) {
		st.Phase = Running
		return st, nil
	}
	st.Phase = Crashed
	st.Stdout, _ = os.ReadFile(d.path("stdout"))
	return st, nil
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
	for _, name := range []string{"exit", "exit.tmp", "stdout", "stderr"} {
		os.Remove(d.path(name))
	}
	if err := os.WriteFile(d.path("prompt"), []byte(prompt), 0o600); err != nil {
		return err
	}
	pid, err := spawn(argv, workdir, string(d), env)
	if err != nil {
		return err
	}
	rec := Record{PID: pid, StartedAt: time.Now().UTC(), Argv0: filepath.Base(argv[0])}
	b, _ := json.Marshal(rec)
	return os.WriteFile(d.path("run.json"), append(b, '\n'), 0o600)
}

// Stop terminates the running turn's process group.
func (d Dir) Stop() error {
	st, err := d.Status()
	if err != nil || st.Phase != Running {
		return err
	}
	return terminate(st.Record.PID)
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

// Package process supervises anything that can be described by three
// commands: one that prints a JSON status, one that accepts a prompt on
// stdin, and an optional one that stops the session.
package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// Adapter runs the configured commands.
type Adapter struct{ cfg config.Process }

// New returns an adapter for the configuration.
func New(cfg config.Process) *Adapter { return &Adapter{cfg: cfg} }

// Name implements adapter.Adapter.
func (a *Adapter) Name() string { return "process" }

// Status is the JSON the status command prints on stdout.
//
//	{"state":"idle","errorClass":"rate_limit","queued":0,"background":0,"gone":false}
//
// errorClass is optional. When the status command exits non-zero, the exit
// code is looked up in exit_codes to classify the error; an unmapped code
// is an error of class unknown.
type Status struct {
	State      string `json:"state"`
	ErrorClass string `json:"errorClass,omitempty"`
	Queued     int    `json:"queued,omitempty"`
	Background int    `json:"background,omitempty"`
	Gone       bool   `json:"gone,omitempty"`
	// AwaitingInput: the session is blocked on a person.
	AwaitingInput bool `json:"awaitingInput,omitempty"`
}

// Observe implements adapter.Adapter.
func (a *Adapter) Observe(ctx context.Context) (decide.Observation, error) {
	out, code, err := a.run(ctx, a.cfg.StatusCommand, "", nil)
	if err != nil && code < 0 {
		return decide.Observation{State: decide.Unknown}, fmt.Errorf("status command: %w", err)
	}
	if code != 0 {
		class := decide.ErrUnknown
		if name, ok := a.cfg.ExitCodes[code]; ok {
			class = decide.ErrorClass(name)
		}
		return decide.Observation{State: decide.Error, ErrorClass: class, Evidence: evidence.Clean(evidence.Detail{Source: "process_exit", ExitCode: evidence.Number(code)})}, nil
	}
	var st Status
	if err := json.Unmarshal(bytes.TrimSpace(out), &st); err != nil {
		return decide.Observation{State: decide.Unknown}, errors.New("status command: stdout is not a JSON status object")
	}
	obs := decide.Observation{
		Evidence:      evidence.Clean(evidence.Detail{Source: "process_json", Code: st.ErrorClass, ExitCode: evidence.Number(code)}),
		State:         adapter.ParseState(st.State),
		ErrorClass:    decide.ErrNone,
		Queued:        st.Queued,
		Background:    st.Background,
		Gone:          st.Gone,
		AwaitingInput: st.AwaitingInput,
	}
	if obs.State == decide.Error {
		obs.ErrorClass = decide.ErrUnknown
		if st.ErrorClass != "" {
			if config.ValidErrorClass(st.ErrorClass) {
				obs.ErrorClass = decide.ErrorClass(st.ErrorClass)
			} else {
				obs.ErrorClass = adapter.ClassifyCode(st.ErrorClass)
			}
		}
	}
	return obs, nil
}

// Send implements adapter.Adapter. The prompt arrives on stdin and the mode
// in KEEPGOING_MODE.
func (a *Adapter) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	_, code, err := a.run(ctx, a.cfg.ResumeCommand, prompt, map[string]string{"KEEPGOING_MODE": string(mode)})
	if err != nil {
		return fmt.Errorf("resume command (exit %d): %w", code, err)
	}
	return nil
}

// Stop implements adapter.Adapter.
func (a *Adapter) Stop(ctx context.Context) error {
	if len(a.cfg.StopCommand) == 0 {
		return adapter.ErrUnsupported{Op: "stop"}
	}
	_, code, err := a.run(ctx, a.cfg.StopCommand, "", nil)
	if err != nil {
		return fmt.Errorf("stop command (exit %d): %w", code, err)
	}
	return nil
}

// run executes argv with the configured timeout. It returns stdout, the exit
// code (or -1 when the process could not run), and any error.
func (a *Adapter) run(ctx context.Context, argv []string, stdin string, extra map[string]string) ([]byte, int, error) {
	timeout := a.cfg.Timeout.Duration
	if timeout <= 0 {
		timeout = config.DefaultProcessTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	for k, v := range a.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range extra {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // never captured: it may contain prose we must not interpret
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.Bytes(), -1, fmt.Errorf("timed out after %s", timeout)
		}
		return stdout.Bytes(), exitErr.ExitCode(), err
	}
	return stdout.Bytes(), -1, err
}

// Package bb supervises a BB thread through the bb CLI's JSON output.
package bb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Adapter drives one BB thread.
type Adapter struct {
	CLI     string
	Thread  string
	Timeout time.Duration
	// run is swapped in tests.
	run func(ctx context.Context, stdin string, args ...string) ([]byte, error)
}

// New returns an adapter for the thread.
func New(cli, thread string) *Adapter {
	a := &Adapter{CLI: cli, Thread: thread, Timeout: 30 * time.Second}
	a.run = a.runInput
	return a
}

// Name implements adapter.Adapter.
func (a *Adapter) Name() string { return "bb" }

type envelope struct {
	OK    *bool `json:"ok"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type showResponse struct {
	Thread *struct {
		Status                   string `json:"status"`
		ProviderID               string `json:"providerId"`
		QueuedMessageCount       int    `json:"queuedMessageCount"`
		ActiveBackgroundAgentCnt int    `json:"activeBackgroundAgentCount"`
		ArchivedAt               any    `json:"archivedAt"`
		DeletedAt                any    `json:"deletedAt"`
	} `json:"thread"`
}

// Observe reads `bb thread show --json`. When the thread is in error it also
// reads the newest log events and classifies a structured code if one is
// present. Message text is never consulted.
func (a *Adapter) Observe(ctx context.Context) (decide.Observation, error) {
	out, err := a.run(ctx, "", "thread", "show", a.Thread, "--json")
	if err != nil {
		if code, ok := envelopeError(out); ok {
			if code == "thread_not_found" {
				return decide.Observation{State: decide.Unknown, Gone: true}, nil
			}
			return decide.Observation{State: decide.Unknown}, fmt.Errorf("bb thread show: %s", code)
		}
		return decide.Observation{State: decide.Unknown}, fmt.Errorf("bb thread show: %w", err)
	}
	if code, ok := envelopeError(out); ok {
		if code == "thread_not_found" {
			return decide.Observation{State: decide.Unknown, Gone: true}, nil
		}
		return decide.Observation{State: decide.Unknown}, fmt.Errorf("bb thread show: %s", code)
	}
	var resp showResponse
	if err := json.Unmarshal(out, &resp); err != nil || resp.Thread == nil {
		return decide.Observation{State: decide.Unknown}, errors.New("bb thread show: unexpected JSON")
	}
	t := resp.Thread
	obs := decide.Observation{
		State:      adapter.ParseState(t.Status),
		ErrorClass: decide.ErrNone,
		Queued:     t.QueuedMessageCount,
		Background: t.ActiveBackgroundAgentCnt,
		Gone:       t.ArchivedAt != nil || t.DeletedAt != nil,
	}
	if obs.State == decide.Error {
		obs.ErrorClass = a.errorClass(ctx)
	}
	return obs, nil
}

// errorClass looks for a structured code on the most recent error-like log
// events. Any failure to find one is "unknown", which the engine treats
// conservatively.
func (a *Adapter) errorClass(ctx context.Context) decide.ErrorClass {
	out, err := a.run(ctx, "", "thread", "log", a.Thread, "--json", "--limit", "10")
	if err != nil {
		return decide.ErrUnknown
	}
	var events []struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out, &events); err != nil {
		return decide.ErrUnknown
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		typ := strings.ToLower(e.Type)
		if !strings.Contains(typ, "error") && !strings.Contains(typ, "fail") {
			continue
		}
		if code, ok := structuredCode(e.Data); ok {
			return adapter.ClassifyCode(code)
		}
		return decide.ErrUnknown
	}
	return decide.ErrUnknown
}

// structuredCode pulls a code-like field out of an event payload, looking
// one level into a nested "error" object. It never returns message text.
func structuredCode(data map[string]any) (string, bool) {
	if data == nil {
		return "", false
	}
	for _, key := range []string{"code", "errorCode", "error_code", "status", "httpStatus", "statusCode", "type"} {
		if v, ok := data[key]; ok {
			switch x := v.(type) {
			case string:
				if x != "" && !strings.ContainsAny(x, " \n") {
					return x, true
				}
			case float64:
				return fmt.Sprintf("%d", int(x)), true
			}
		}
	}
	if inner, ok := data["error"].(map[string]any); ok {
		return structuredCode(inner)
	}
	return "", false
}

// Send implements adapter.Adapter via `bb thread tell`.
func (a *Adapter) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	out, err := a.run(ctx, prompt, "thread", "tell", a.Thread, "--message-file", "-", "--mode", string(mode), "--json")
	if err != nil {
		if code, ok := envelopeError(out); ok {
			return fmt.Errorf("bb thread tell: %s", code)
		}
		return fmt.Errorf("bb thread tell: %w", err)
	}
	if code, ok := envelopeError(out); ok {
		return fmt.Errorf("bb thread tell: %s", code)
	}
	return nil
}

// Stop is not available through the bb CLI yet.
func (a *Adapter) Stop(ctx context.Context) error { return adapter.ErrUnsupported{Op: "stop"} }

func envelopeError(out []byte) (string, bool) {
	var env envelope
	if json.Unmarshal(out, &env) != nil || env.OK == nil || *env.OK || env.Error == nil {
		return "", false
	}
	return env.Error.Code, true
}

func (a *Adapter) runInput(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.CLI, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// Only the JSON envelope on stdout is trusted; stderr may hold prose.
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

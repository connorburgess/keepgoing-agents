package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Claude runs Claude Code headless, one turn per Send, resuming the same
// session. Status comes from the result JSON's is_error, subtype, and
// api_error_status fields and from the exit code, never from result text.
type Claude struct {
	cfg config.Claude
	dir Dir
}

// NewClaude returns a headless Claude Code adapter storing turn files in dir.
func NewClaude(cfg config.Claude, dir string) *Claude { return &Claude{cfg: cfg, dir: Dir(dir)} }

// Name implements adapter.Adapter.
func (c *Claude) Name() string { return "claude" }

// ClaudeResult is the subset of `claude -p --output-format json` we read.
type ClaudeResult struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	SessionID      string `json:"session_id"`
	APIErrorStatus any    `json:"api_error_status"`
	TerminalReason string `json:"terminal_reason"`
}

// ParseClaudeResult reads the last JSON object in stdout.
func ParseClaudeResult(stdout []byte) (ClaudeResult, bool) {
	var r ClaudeResult
	lines := bytes.Split(bytes.TrimSpace(stdout), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if json.Unmarshal(lines[i], &r) == nil && r.Type == "result" {
			return r, true
		}
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &r) == nil && r.Type == "result" {
		return r, true
	}
	return r, false
}

// Class classifies a failed result from structured fields only.
func (r ClaudeResult) Class() decide.ErrorClass {
	switch v := r.APIErrorStatus.(type) {
	case float64:
		if cl := adapter.ClassifyCode(strconv.Itoa(int(v))); cl != decide.ErrUnknown {
			return cl
		}
	case string:
		if cl := adapter.ClassifyCode(v); cl != decide.ErrUnknown {
			return cl
		}
	}
	if cl := adapter.ClassifyCode(r.TerminalReason); cl != decide.ErrUnknown {
		return cl
	}
	return adapter.ClassifyCode(r.Subtype)
}

// Observe implements adapter.Adapter.
func (c *Claude) Observe(ctx context.Context) (decide.Observation, error) {
	st, err := c.dir.Status()
	if err != nil {
		return decide.Observation{State: decide.Unknown}, err
	}
	switch st.Phase {
	case NeverRun:
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone}, nil
	case Running:
		return decide.Observation{State: decide.Active, ErrorClass: decide.ErrNone}, nil
	case Crashed:
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	}
	r, ok := ParseClaudeResult(st.Stdout)
	if ok {
		c.dir.SaveSession(r.SessionID)
	}
	if ok && !r.IsError && st.ExitCode == 0 {
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone}, nil
	}
	class := decide.ErrUnknown
	if ok {
		class = r.Class()
	}
	return decide.Observation{State: decide.Error, ErrorClass: class}, nil
}

// Args builds the claude argv for one turn.
func (c *Claude) Args() []string {
	args := []string{c.cfg.CLI, "-p", "--output-format", "json"}
	if c.cfg.Model != "" {
		args = append(args, "--model", c.cfg.Model)
	}
	if c.cfg.Effort != "" {
		args = append(args, "--effort", c.cfg.Effort)
	}
	if c.cfg.PermissionMode != "" {
		args = append(args, "--permission-mode", c.cfg.PermissionMode)
	}
	if c.cfg.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(c.cfg.MaxBudgetUSD, 'f', -1, 64))
	}
	if s := c.dir.Session(); s != "" {
		args = append(args, "--resume", s)
	}
	return append(args, c.cfg.ExtraArgs...)
}

// Send starts a turn. A running turn cannot be steered or queued into.
func (c *Claude) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	st, err := c.dir.Status()
	if err != nil {
		return err
	}
	if st.Phase == Running {
		return adapter.ErrUnsupported{Op: fmt.Sprintf("sending to a running headless turn (%s mode)", mode)}
	}
	if st.Phase == Finished {
		if r, ok := ParseClaudeResult(st.Stdout); ok {
			c.dir.SaveSession(r.SessionID)
		}
	}
	return c.dir.Start(c.Args(), c.cfg.Workdir, prompt, envList(c.cfg.Env))
}

// Stop implements adapter.Adapter.
func (c *Claude) Stop(ctx context.Context) error { return c.dir.Stop() }

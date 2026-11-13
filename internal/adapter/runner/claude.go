package runner

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// claudeSpec is Claude Code headless: `claude -p --output-format json`, one
// turn per process, resuming the same session. Status comes from the result
// JSON's is_error, subtype, and api_error_status fields and from the exit
// code, never from result text.
var claudeSpec = &Spec{
	Name:         "claude",
	Provider:     "claude-code",
	DefaultCLI:   "claude",
	AccountEnv:   "CLAUDE_CONFIG_DIR",
	SettingNames: []string{"cli", "workdir", "model", "effort", "permission_mode", "max_budget_usd", "extra_args", "env"},
	Prompt:       PromptStdin,
	Capabilities: Capabilities{ExactResume: true, Stoppable: true, StructuredErrors: true},
	Settings: func(a config.Agent) (Settings, bool) {
		if a.Claude == nil {
			return Settings{}, false
		}
		return Settings{CLI: a.Claude.CLI, Workdir: a.Claude.Workdir, Model: a.Claude.Model, Env: a.Claude.Env}, true
	},
	Argv: func(a config.Agent, session string) []string { return claudeArgv(*a.Claude, session) },
	Read: func(turn Status) Reading {
		r, ok := ParseClaudeResult(turn.Stdout)
		if !ok {
			return Reading{Class: decide.ErrUnknown}
		}
		return Reading{Session: r.SessionID, Completed: !r.IsError && !r.Refused(), Class: r.Class(), Evidence: r.evidence(turn)}
	},
	Probe: ProbeSpec{
		// stream-json carries the same final result object as json, plus the
		// usage events the probe reads. Print mode requires --verbose with it.
		Argv: func(cli, model, prompt string) []string {
			argv := []string{cli, "-p", prompt, "--output-format", "stream-json", "--verbose", "--no-session-persistence",
				"--restricted", "--strict-mcp-config", "--tools", "", "--max-turns", "1"}
			if model != "" {
				argv = append(argv, "--model", model)
			}
			return argv
		},
		Judge: judgeClaudeProbe,
		Trim:  dropClaudeInit,
	},
}

// NewClaude returns a headless Claude Code adapter storing turn files in dir.
func NewClaude(cfg config.Claude, dir string) *Headless {
	h, _ := New(config.Agent{Adapter: claudeSpec.Name, Claude: &cfg}, dir)
	return h
}

// ClaudeResult is the subset of `claude -p --output-format json` we read.
type ClaudeResult struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	SessionID      string `json:"session_id"`
	APIErrorStatus any    `json:"api_error_status"`
	TerminalReason string `json:"terminal_reason"`
	// StopReason is "refusal" when the model declined the turn. Claude Code
	// documents that such a result usually has subtype "success".
	StopReason string `json:"stop_reason"`
}

// Refused reports whether the model declined the turn, from the result's
// structured stop reason.
func (r ClaudeResult) Refused() bool { return r.StopReason == "refusal" }

// ParseClaudeResult reads the last result object in stdout.
func ParseClaudeResult(stdout []byte) (ClaudeResult, bool) {
	r, _, ok := claudeResult(stdout)
	return r, ok
}

// ClaudeResultLine returns the JSON text ParseClaudeResult reads: the last
// line holding a result object, wherever it sits in the output, or the whole
// output when that is one multi-line result object.
func ClaudeResultLine(stdout []byte) ([]byte, bool) {
	_, line, ok := claudeResult(stdout)
	return line, ok
}

func claudeResult(stdout []byte) (ClaudeResult, []byte, bool) {
	var r ClaudeResult
	lines := bytes.Split(bytes.TrimSpace(stdout), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if json.Unmarshal(lines[i], &r) == nil && r.Type == "result" {
			return r, lines[i], true
		}
	}
	if whole := bytes.TrimSpace(stdout); json.Unmarshal(whole, &r) == nil && r.Type == "result" {
		return r, whole, true
	}
	return r, nil, false
}

// Class classifies a failed result from structured fields only.
func (r ClaudeResult) Class() decide.ErrorClass {
	if r.Refused() {
		return decide.ErrRestricted
	}
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

// claudeArgv builds the claude argv for one turn.
func claudeArgv(cfg config.Claude, session string) []string {
	args := []string{cfg.CLI, "-p", "--output-format", "json"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.Effort != "" {
		args = append(args, "--effort", cfg.Effort)
	}
	if cfg.PermissionMode != "" {
		args = append(args, "--permission-mode", cfg.PermissionMode)
	}
	if cfg.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(cfg.MaxBudgetUSD, 'f', -1, 64))
	}
	if session != "" {
		args = append(args, "--resume", session)
	}
	return append(args, cfg.ExtraArgs...)
}

// judgeClaudeProbe reads a probe's result object.
func judgeClaudeProbe(stdout []byte, code int) ProbeVerdict {
	var v ProbeVerdict
	r, ok := ParseClaudeResult(stdout)
	var extra struct {
		CredentialExpiresAt time.Time `json:"credential_expires_at"`
	}
	// Read the expiry from the result object itself: stream output may carry
	// other events after it.
	if line, found := ClaudeResultLine(stdout); found && json.Unmarshal(line, &extra) == nil && !extra.CredentialExpiresAt.IsZero() {
		t := extra.CredentialExpiresAt
		v.CredentialExpiresAt = &t
	}
	switch {
	case !ok:
		v.Class, v.Detail = decide.ErrUnknown, "no result object"
	case !r.IsError && code == 0:
		v.OK, v.Detail = true, "subtype="+r.Subtype
	default:
		v.Class, v.Detail = r.Class(), "subtype="+r.Subtype
	}
	return v
}

// dropClaudeInit drops the CLI's initialization message, which describes the
// local installation (working directory, session and tool lists) and says
// nothing about a failure.
func dropClaudeInit(stdout []byte) []byte {
	var kept [][]byte
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "system" && event.Subtype == "init" {
			continue
		}
		kept = append(kept, line)
	}
	return bytes.Join(kept, []byte("\n"))
}

func (r ClaudeResult) evidence(st Status) evidence.Detail {
	d := runnerEvidence(st)
	d.Source = "claude_result"
	d.Category = r.Subtype
	d.Code = r.TerminalReason
	if r.Refused() {
		d.Code = "refusal"
	}
	if evidence.Code(d.Code) == "unavailable" {
		d.Code = r.Subtype
	}
	switch v := r.APIErrorStatus.(type) {
	case float64:
		if v >= 100 && v <= 599 && v == float64(int(v)) {
			d.HTTPStatus = evidence.Number(int(v))
		}
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			d.HTTPStatus = evidence.Number(n)
		} else if evidence.Code(v) != "unavailable" {
			d.Code = v
		}
	}
	return evidence.Clean(d)
}

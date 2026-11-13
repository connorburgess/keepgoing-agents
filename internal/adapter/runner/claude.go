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
	Argv:         func(set Settings, session, _ string) []string { return claudeArgv(set, session) },
	Read: func(turn Status) Reading {
		r, ok := ParseClaudeResult(turn.Stdout)
		if !ok {
			return Reading{Class: decide.ErrUnknown}
		}
		return Reading{Session: r.SessionID, Completed: !r.IsError && turn.ExitCode == 0, Class: r.Class(), Evidence: r.evidence(turn)}
	},
	Probe: ProbeSpec{
		// stream-json carries the same final result object as json, plus the
		// usage events the probe reads. Print mode requires --verbose with it.
		Command: func(p ProbeRequest) ([]string, string) {
			argv := []string{p.CLI, "-p", p.Prompt, "--output-format", "stream-json", "--verbose", "--no-session-persistence",
				"--restricted", "--strict-mcp-config", "--tools", "", "--max-turns", "1"}
			if p.Model != "" {
				argv = append(argv, "--model", p.Model)
			}
			return argv, ""
		},
		Judge:       judgeClaudeProbe,
		Trim:        dropClaudeInit,
		Usage:       ClaudeUsageReadings,
		UsageSource: "claude_rate_limit_event",
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
}

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

// claudeSettings reads the loop's claude settings.
func claudeSettings(a config.Agent) (Settings, bool) {
	c := a.Claude
	if c == nil {
		return Settings{}, false
	}
	set := Settings{CLI: c.CLI, Workdir: c.Workdir, Model: c.Model, Env: c.Env, ExtraArgs: c.ExtraArgs, Options: map[string]string{}}
	if c.Effort != "" {
		set.Options["effort"] = c.Effort
	}
	if c.PermissionMode != "" {
		set.Options["permission_mode"] = c.PermissionMode
	}
	if c.MaxBudgetUSD > 0 {
		set.Options["max_budget_usd"] = strconv.FormatFloat(c.MaxBudgetUSD, 'f', -1, 64)
	}
	return set, true
}

// claudeArgv builds the claude argv for one turn.
func claudeArgv(set Settings, session string) []string {
	args := []string{set.CLI, "-p", "--output-format", "json"}
	if set.Model != "" {
		args = append(args, "--model", set.Model)
	}
	for _, o := range []struct{ key, flag string }{{"effort", "--effort"}, {"permission_mode", "--permission-mode"}, {"max_budget_usd", "--max-budget-usd"}} {
		if v := set.Options[o.key]; v != "" {
			args = append(args, o.flag, v)
		}
	}
	if session != "" {
		args = append(args, "--resume", session)
	}
	return append(args, set.ExtraArgs...)
}

// ClaudeUsageReadings reads the quota windows from rate_limit_event messages
// in Claude Code stream-json output. Later events override earlier ones per
// window. The event is not a documented contract, so anything unexpected
// (no event, another shape) yields no window for that part and never an
// error. Only utilization is required: a reset time or status that is
// missing or of another type is left out and the window kept.
func ClaudeUsageReadings(stdout []byte) []UsageReading {
	var windows []UsageReading
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Info struct {
				Windows map[string]json.RawMessage `json:"unifiedWindows"`
			} `json:"rate_limit_info"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "rate_limit_event" {
			continue
		}
		for key, raw := range event.Info.Windows {
			var w struct {
				Utilization json.RawMessage `json:"utilization"`
				ResetsAt    json.RawMessage `json:"resetsAt"`
				Status      json.RawMessage `json:"status"`
			}
			var used, resets float64
			if json.Unmarshal(raw, &w) != nil || json.Unmarshal(w.Utilization, &used) != nil || string(w.Utilization) == "null" {
				continue
			}
			window := UsageReading{Key: key, Used: used}
			// Epoch seconds; refuse values that are not a plausible time.
			if json.Unmarshal(w.ResetsAt, &resets) == nil && resets > 0 && resets < 1e11 {
				t := time.Unix(int64(resets), 0).UTC()
				window.ResetsAt = &t
			}
			var status string
			if json.Unmarshal(w.Status, &status) == nil {
				window.Status = status
			}
			windows = append(windows, window)
		}
	}
	return windows
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

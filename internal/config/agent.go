package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Agent describes how to reach one agent session. The primary loop and
// every fallback candidate use the same shape.
type Agent struct {
	Adapter  string   `yaml:"adapter"`
	Provider string   `yaml:"provider,omitempty"` // claude-code, codex, or any label
	Group    string   `yaml:"group,omitempty"`    // account or quota pool; defaults to provider
	BB       *BB      `yaml:"bb,omitempty"`
	Process  *Process `yaml:"process,omitempty"`
	Claude   *Claude  `yaml:"claude,omitempty"`
	Codex    *Codex   `yaml:"codex,omitempty"`
	Cursor   *Cursor  `yaml:"cursor,omitempty"`
	// Custom is an agent defined in configuration.
	Custom *Custom `yaml:"custom,omitempty"`
	// ACP is an agent reached over the Agent Client Protocol.
	ACP *ACP `yaml:"acp,omitempty"`
}

// BB configures the BB thread adapter. Model and Reasoning, when set, are
// applied to the thread before the first message after a handoff.
type BB struct {
	// Thread is an existing thread. A fallback candidate may instead set
	// NewThread, and then has no thread until a handoff creates one.
	Thread string `yaml:"thread,omitempty"`
	// NewThread opts a fallback candidate in to "start a new BB thread on this
	// provider and model" at handoff. It is the only case in which keepgoing
	// creates a thread. Project and Environment, when unset, are taken from the
	// conversation the new thread continues.
	NewThread   bool   `yaml:"new_thread,omitempty"`
	Project     string `yaml:"project,omitempty"`
	Environment string `yaml:"environment,omitempty"`
	// PermissionMode is the permission mode of a thread created for
	// NewThread: accept-edits (the default), auto or full. It is always
	// passed to BB, so the new thread never inherits a project default.
	PermissionMode string `yaml:"permission_mode,omitempty"`
	CLI            string `yaml:"cli,omitempty"`
	Model          string `yaml:"model,omitempty"`
	Reasoning      string `yaml:"reasoning,omitempty"`
	// RetrySignalTTL bounds how long a provider retry notice on an errored
	// thread suppresses error recovery. Zero uses the adapter default (15m).
	RetrySignalTTL Duration `yaml:"retry_signal_ttl,omitempty"`
}

// DefaultNewThreadPermission is the permission mode of a thread keepgoing
// creates when the candidate sets none: the most restrictive BB offers.
const DefaultNewThreadPermission = "accept-edits"

// NewThreadPermission is the permission mode a created thread gets.
func (b BB) NewThreadPermission() string {
	if b.PermissionMode == "" {
		return DefaultNewThreadPermission
	}
	return b.PermissionMode
}

// Process configures the generic process adapter. Each command is argv.
type Process struct {
	StatusCommand []string          `yaml:"status_command"`
	ResumeCommand []string          `yaml:"resume_command"`
	StopCommand   []string          `yaml:"stop_command,omitempty"`
	ExitCodes     map[int]string    `yaml:"exit_codes,omitempty"`
	Env           map[string]string `yaml:"env,omitempty"`
	Timeout       Duration          `yaml:"timeout,omitempty"`
}

// Claude configures a headless Claude Code session that keepgoing runs
// itself, one turn at a time, resuming the same session each turn.
type Claude struct {
	CLI            string            `yaml:"cli,omitempty"`
	Workdir        string            `yaml:"workdir"`
	Model          string            `yaml:"model,omitempty"`
	Effort         string            `yaml:"effort,omitempty"`
	PermissionMode string            `yaml:"permission_mode,omitempty"`
	MaxBudgetUSD   float64           `yaml:"max_budget_usd,omitempty"`
	ExtraArgs      []string          `yaml:"extra_args,omitempty"`
	Env            map[string]string `yaml:"env,omitempty"`
}

// Codex configures a headless Codex session run with `codex exec`.
type Codex struct {
	CLI              string            `yaml:"cli,omitempty"`
	Workdir          string            `yaml:"workdir"`
	Model            string            `yaml:"model,omitempty"`
	Sandbox          string            `yaml:"sandbox,omitempty"`
	SkipGitRepoCheck bool              `yaml:"skip_git_repo_check,omitempty"`
	ExtraArgs        []string          `yaml:"extra_args,omitempty"`
	Env              map[string]string `yaml:"env,omitempty"`
}

// Cursor configures a primary-only Cursor CLI print session. Permissions remain
// those configured by the user in Cursor; no approval-bypass flags are added.
type Cursor struct {
	CLI     string            `yaml:"cli,omitempty"`
	Workdir string            `yaml:"workdir"`
	Model   string            `yaml:"model,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	// FreshStartVersions lists further CLI versions, beyond the inspected ones,
	// for which the operator has confirmed that the CLI writes its first stream
	// records before it submits a prompt. See docs/cursor.md.
	FreshStartVersions []string `yaml:"fresh_start_versions,omitempty"`
}

// CursorVersionPattern is the only version token shape accepted from the CLI
// or from configuration.
var CursorVersionPattern = regexp.MustCompile(`^[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[0-9a-f]{7,40}$`)

// Adapters lists the supported adapter names.
var Adapters = []string{"bb", "process", "claude", "codex", "cursor", CustomAdapter, ACPAdapter}

func (a *Agent) applyDefaults() {
	if a.Cursor != nil && a.Cursor.CLI == "" {
		a.Cursor.CLI = "agent"
	}
	if a.Provider == "" {
		switch a.Adapter {
		case "claude":
			a.Provider = "claude-code"
		case "codex":
			a.Provider = "codex"
		case "cursor":
			a.Provider = "cursor"
		case CustomAdapter:
			if a.Custom != nil {
				a.Provider = a.Custom.Name
			}
		case ACPAdapter:
			if a.ACP != nil {
				a.Provider = a.ACP.Name
			}
		}
	}
	if a.Group == "" {
		a.Group = a.Provider
	}
	if a.Custom != nil {
		a.Custom.applyDefaults()
	}
	if a.BB != nil && a.BB.CLI == "" {
		a.BB.CLI = DefaultBBCLI
	}
	if a.Process != nil && a.Process.Timeout.Duration == 0 {
		a.Process.Timeout.Duration = DefaultProcessTimeout
	}
	if a.Claude != nil && a.Claude.CLI == "" {
		a.Claude.CLI = "claude"
	}
	if a.Codex != nil && a.Codex.CLI == "" {
		a.Codex.CLI = "codex"
	}
}

// validate reports problems with field names prefixed by where.
func (a Agent) validate(where string) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(where+format, args...))
	}
	set := map[string]bool{"bb": a.BB != nil, "process": a.Process != nil, "claude": a.Claude != nil, "codex": a.Codex != nil, "cursor": a.Cursor != nil, CustomAdapter: a.Custom != nil, ACPAdapter: a.ACP != nil}
	known := false
	for _, n := range Adapters {
		if a.Adapter == n {
			known = true
		}
	}
	if !known {
		add("adapter must be one of %s", strings.Join(Adapters, ", "))
		return errs
	}
	if !set[a.Adapter] {
		add("%s is required for the %s adapter", a.Adapter, a.Adapter)
	}
	for name, on := range set {
		if on && name != a.Adapter {
			add("%s is set but the adapter is %s", name, a.Adapter)
		}
	}
	switch {
	case a.Adapter == "bb" && a.BB != nil:
		switch {
		case a.BB.NewThread && a.BB.Thread != "":
			add("bb.thread and bb.new_thread cannot both be set")
		case a.BB.NewThread && a.Provider == "":
			add("bb.new_thread needs provider: the new thread is created on that BB provider")
		case !a.BB.NewThread && a.BB.Thread == "":
			add("bb.thread is required (or bb.new_thread for a fallback that starts a new thread at handoff)")
		case !a.BB.NewThread && (a.BB.Project != "" || a.BB.Environment != "" || a.BB.PermissionMode != ""):
			add("bb.project, bb.environment and bb.permission_mode apply only with bb.new_thread")
		case a.BB.NewThread && a.BB.PermissionMode != "" && a.BB.PermissionMode != "accept-edits" && a.BB.PermissionMode != "auto" && a.BB.PermissionMode != "full":
			add("bb.permission_mode must be accept-edits, auto or full")
		}
		if d := a.BB.RetrySignalTTL.Duration; d != 0 && d < time.Minute {
			add("bb.retry_signal_ttl must be at least 1m")
		}
	case a.Adapter == "process" && a.Process != nil:
		if len(a.Process.StatusCommand) == 0 {
			add("process.status_command is required")
		}
		if len(a.Process.ResumeCommand) == 0 {
			add("process.resume_command is required")
		}
		for code, class := range a.Process.ExitCodes {
			if !ValidErrorClass(class) {
				add("process.exit_codes[%d]: unknown error class %q", code, class)
			}
		}
	case a.Adapter == "cursor" && a.Cursor != nil:
		if a.Cursor.Workdir == "" {
			add("cursor.workdir is required")
		}
		if len(a.Cursor.FreshStartVersions) > 16 {
			add("cursor.fresh_start_versions: at most 16 versions")
		}
		for _, v := range a.Cursor.FreshStartVersions {
			if !CursorVersionPattern.MatchString(v) {
				add("cursor.fresh_start_versions: each entry must be a Cursor CLI version such as 2030.01.02-abcdef0")
				break
			}
		}
	case a.Adapter == "claude" && a.Claude != nil:
		if a.Claude.Workdir == "" {
			add("claude.workdir is required")
		}
	case a.Adapter == "codex" && a.Codex != nil:
		if a.Codex.Workdir == "" {
			add("codex.workdir is required")
		}
	case a.Adapter == CustomAdapter && a.Custom != nil:
		a.Custom.validate(add)
	case a.Adapter == ACPAdapter && a.ACP != nil:
		a.ACP.validate(add)
	}
	return errs
}

// Model returns the model this agent targets, if configured.
func (a Agent) Model() string {
	switch {
	case a.BB != nil:
		return a.BB.Model
	case a.Cursor != nil:
		return a.Cursor.Model
	case a.Claude != nil:
		return a.Claude.Model
	case a.Codex != nil:
		return a.Codex.Model
	case a.Custom != nil:
		return a.Custom.Values["model"]
	}
	return ""
}

// Describe is a short human label such as "bb thr_x (claude-fable-5-1)".
func (a Agent) Describe() string {
	s := a.Adapter
	switch {
	case a.BB != nil && a.BB.NewThread:
		s += " new thread"
	case a.BB != nil:
		s += " " + a.BB.Thread
	case a.Cursor != nil:
		s += " " + a.Cursor.Workdir
	case a.Claude != nil:
		s += " " + a.Claude.Workdir
	case a.Codex != nil:
		s += " " + a.Codex.Workdir
	case a.Custom != nil:
		s += " " + a.Custom.Name + " " + a.Custom.Workdir
	case a.ACP != nil:
		s += " " + a.ACP.Name + " " + a.ACP.Workdir
	}
	if m := a.Model(); m != "" {
		s += " (" + m + ")"
	}
	return s
}

func (a *Agent) resolvePaths(base string) {
	if a.Custom != nil {
		a.Custom.resolvePaths(base)
	}
	if a.ACP != nil {
		a.ACP.resolvePaths(base)
	}
	if a.Cursor != nil {
		a.Cursor.Workdir = resolve(base, a.Cursor.Workdir)
		a.Cursor.CLI = resolveExec(base, a.Cursor.CLI)
	}
	if a.BB != nil {
		a.BB.CLI = resolveExec(base, a.BB.CLI)
	}
	if a.Process != nil {
		resolveArgv(base, a.Process.StatusCommand)
		resolveArgv(base, a.Process.ResumeCommand)
		resolveArgv(base, a.Process.StopCommand)
	}
	if a.Claude != nil {
		a.Claude.Workdir = resolve(base, a.Claude.Workdir)
		a.Claude.CLI = resolveExec(base, a.Claude.CLI)
	}
	if a.Codex != nil {
		a.Codex.Workdir = resolve(base, a.Codex.Workdir)
		a.Codex.CLI = resolveExec(base, a.Codex.CLI)
	}
}

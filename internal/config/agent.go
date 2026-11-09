package config

import (
	"fmt"
	"strings"
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
}

// BB configures the BB thread adapter. Model and Reasoning, when set, are
// applied to the thread before the first message after a handoff.
type BB struct {
	Thread    string `yaml:"thread"`
	CLI       string `yaml:"cli,omitempty"`
	Model     string `yaml:"model,omitempty"`
	Reasoning string `yaml:"reasoning,omitempty"`
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
	CLI            string   `yaml:"cli,omitempty"`
	Workdir        string   `yaml:"workdir"`
	Model          string   `yaml:"model,omitempty"`
	Effort         string   `yaml:"effort,omitempty"`
	PermissionMode string   `yaml:"permission_mode,omitempty"`
	MaxBudgetUSD   float64  `yaml:"max_budget_usd,omitempty"`
	ExtraArgs      []string `yaml:"extra_args,omitempty"`
}

// Codex configures a headless Codex session run with `codex exec`.
type Codex struct {
	CLI              string   `yaml:"cli,omitempty"`
	Workdir          string   `yaml:"workdir"`
	Model            string   `yaml:"model,omitempty"`
	Sandbox          string   `yaml:"sandbox,omitempty"`
	SkipGitRepoCheck bool     `yaml:"skip_git_repo_check,omitempty"`
	ExtraArgs        []string `yaml:"extra_args,omitempty"`
}

// Adapters lists the supported adapter names.
var Adapters = []string{"bb", "process", "claude", "codex"}

func (a *Agent) applyDefaults() {
	if a.Provider == "" {
		switch a.Adapter {
		case "claude":
			a.Provider = "claude-code"
		case "codex":
			a.Provider = "codex"
		}
	}
	if a.Group == "" {
		a.Group = a.Provider
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
	set := map[string]bool{"bb": a.BB != nil, "process": a.Process != nil, "claude": a.Claude != nil, "codex": a.Codex != nil}
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
		if a.BB.Thread == "" {
			add("bb.thread is required")
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
	case a.Adapter == "claude" && a.Claude != nil:
		if a.Claude.Workdir == "" {
			add("claude.workdir is required")
		}
	case a.Adapter == "codex" && a.Codex != nil:
		if a.Codex.Workdir == "" {
			add("codex.workdir is required")
		}
	}
	return errs
}

// Model returns the model this agent targets, if configured.
func (a Agent) Model() string {
	switch {
	case a.BB != nil:
		return a.BB.Model
	case a.Claude != nil:
		return a.Claude.Model
	case a.Codex != nil:
		return a.Codex.Model
	}
	return ""
}

// Describe is a short human label such as "bb thr_x (claude-fable-5-1)".
func (a Agent) Describe() string {
	s := a.Adapter
	switch {
	case a.BB != nil:
		s += " " + a.BB.Thread
	case a.Claude != nil:
		s += " " + a.Claude.Workdir
	case a.Codex != nil:
		s += " " + a.Codex.Workdir
	}
	if m := a.Model(); m != "" {
		s += " (" + m + ")"
	}
	return s
}

func (a *Agent) resolvePaths(base string) {
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

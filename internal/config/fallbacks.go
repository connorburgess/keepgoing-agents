package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Fallbacks is the fallbacks.yaml document: an ordered list of candidates
// the loop may hand off to, plus the rules for trusting them.
type Fallbacks struct {
	Version    int         `yaml:"version"`
	Redundancy Redundancy  `yaml:"redundancy"`
	Probe      ProbePolicy `yaml:"probe"`
	Candidates []Candidate `yaml:"candidates"`
	source     sourceSnapshot
}

// Redundancy says how many confirmed fallbacks must exist before a handoff
// is allowed, and what counts as distinct.
type Redundancy struct {
	MinHealthy int    `yaml:"min_healthy,omitempty"`
	Span       string `yaml:"span,omitempty"` // provider, group, or none
}

// ProbePolicy governs scheduled probes.
type ProbePolicy struct {
	Every    Duration `yaml:"every,omitempty"`     // how often a scheduled probe runs per candidate
	TTL      Duration `yaml:"ttl,omitempty"`       // how long a passing probe counts as healthy
	DailyCap int      `yaml:"daily_cap,omitempty"` // scheduled probes per candidate per UTC day; 0 = default
	Timeout  Duration `yaml:"timeout,omitempty"`
	Disabled bool     `yaml:"scheduled_disabled,omitempty"` // only exact-target probes before a handoff
}

// Candidate is one fallback.
type Candidate struct {
	ID       string `yaml:"id"`
	Agent    `yaml:",inline"`
	Probe    Probe `yaml:"probe"`
	Disabled bool  `yaml:"disabled,omitempty"`
}

// Probe says how to confirm a candidate works.
type Probe struct {
	Kind      string            `yaml:"kind,omitempty"`  // claude, codex, or command
	Model     string            `yaml:"model,omitempty"` // cheap model for scheduled probes
	Command   []string          `yaml:"command,omitempty"`
	ExitCodes map[int]string    `yaml:"exit_codes,omitempty"`
	CLI       string            `yaml:"cli,omitempty"`
	Workdir   string            `yaml:"workdir,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"` // for example CLAUDE_CONFIG_DIR or CODEX_HOME for another account
}

// Fallback defaults.
const (
	DefaultMinHealthy   = 1
	DefaultProbeEvery   = 4 * time.Hour
	DefaultProbeTTL     = 6 * time.Hour
	DefaultProbeCap     = 6
	DefaultProbeTimeout = 90 * time.Second
)

// LoadFallbacks reads and validates a fallbacks file.
func LoadFallbacks(path string) (*Fallbacks, error) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fb, err := ParseFallbacks(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	fb.source = snapshot(path, raw)
	base := filepath.Dir(path)
	for i := range fb.Candidates {
		c := &fb.Candidates[i]
		c.Agent.resolvePaths(base)
		if c.Probe.Workdir != "" {
			c.Probe.Workdir = resolve(base, c.Probe.Workdir)
		}
		c.Probe.CLI = resolveExec(base, c.Probe.CLI)
		resolveArgv(base, c.Probe.Command)
	}
	return fb, nil
}

// ParseFallbacks decodes and validates fallbacks YAML.
func ParseFallbacks(raw []byte) (*Fallbacks, error) {
	var fb Fallbacks
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&fb); err != nil {
		return nil, err
	}
	fb.applyDefaults()
	if err := fb.Validate(); err != nil {
		return nil, err
	}
	return &fb, nil
}

func (f *Fallbacks) applyDefaults() {
	if f.Redundancy.MinHealthy == 0 {
		f.Redundancy.MinHealthy = DefaultMinHealthy
	}
	if f.Redundancy.Span == "" {
		f.Redundancy.Span = "provider"
	}
	setDur(&f.Probe.Every, DefaultProbeEvery)
	setDur(&f.Probe.TTL, DefaultProbeTTL)
	setDur(&f.Probe.Timeout, DefaultProbeTimeout)
	if f.Probe.DailyCap == 0 {
		f.Probe.DailyCap = DefaultProbeCap
	}
	for i := range f.Candidates {
		c := &f.Candidates[i]
		c.Agent.applyDefaults()
		if c.Probe.Kind == "" {
			switch c.Provider {
			case "claude-code":
				c.Probe.Kind = "claude"
			case "codex":
				c.Probe.Kind = "codex"
			}
		}
	}
}

// Validate reports every problem in the fallbacks file.
func (f *Fallbacks) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if f.Version != 1 {
		add("version must be 1")
	}
	if f.Redundancy.MinHealthy < 0 {
		add("redundancy.min_healthy must not be negative")
	}
	switch f.Redundancy.Span {
	case "provider", "group", "none":
	default:
		add("redundancy.span must be provider, group, or none")
	}
	if f.Probe.TTL.Duration < f.Probe.Every.Duration && !f.Probe.Disabled {
		add("probe.ttl must be at least probe.every, or scheduled probes can never keep a candidate healthy")
	}
	if f.Probe.DailyCap < 0 {
		add("probe.daily_cap must not be negative")
	}
	if len(f.Candidates) == 0 {
		add("candidates must list at least one fallback")
	}
	seen := map[string]bool{}
	for i, c := range f.Candidates {
		where := fmt.Sprintf("candidates[%d].", i)
		if !nameRE.MatchString(c.ID) {
			add("%sid must be lowercase letters, digits, and hyphens", where)
		} else if c.ID == "primary" {
			add("%sid \"primary\" is reserved", where)
		} else if seen[c.ID] {
			add("%sid %q is repeated", where, c.ID)
		}
		seen[c.ID] = true
		if c.Provider == "" {
			add("%sprovider is required (for example claude-code or codex); redundancy counts by it", where)
		}
		errs = append(errs, c.Agent.validate(where)...)
		if c.Adapter == "cursor" {
			add("%sCursor fallback candidates are not supported; exact account/model probes are unverified", where)
		}
		switch c.Probe.Kind {
		case "claude", "codex":
		case "command":
			if len(c.Probe.Command) == 0 {
				add("%sprobe.command is required for a command probe", where)
			}
			for code, class := range c.Probe.ExitCodes {
				if !ValidErrorClass(class) {
					add("%sprobe.exit_codes[%d]: unknown error class %q", where, code, class)
				}
			}
		case "":
			add("%sprobe.kind is required for provider %q (claude, codex, or command)", where, c.Provider)
		default:
			add("%sprobe.kind must be claude, codex, or command", where)
		}
	}
	return errors.Join(errs...)
}

// Find returns the candidate with the given id.
func (f *Fallbacks) Find(id string) (Candidate, bool) {
	if f == nil {
		return Candidate{}, false
	}
	for _, c := range f.Candidates {
		if c.ID == id {
			return c, true
		}
	}
	return Candidate{}, false
}

// PrimaryCandidate presents the loop's own agent as a probe-able candidate,
// so the primary can be confirmed before a switch-back.
func (l Loop) PrimaryCandidate() Candidate {
	return Candidate{ID: "primary", Agent: l.Agent, Probe: l.Probe}
}

// ProbeModel is the model a probe should use for a tier: the target model
// for an exact probe, the cheap probe model for a scheduled one.
func (c Candidate) ProbeModel(exact bool) string {
	if exact || c.Probe.Model == "" {
		return c.Model()
	}
	return c.Probe.Model
}

// ProbeWorkdir is where a probe runs.
func (c Candidate) ProbeWorkdir() string {
	switch {
	case c.Probe.Workdir != "":
		return c.Probe.Workdir
	case c.Claude != nil:
		return c.Claude.Workdir
	case c.Codex != nil:
		return c.Codex.Workdir
	}
	return ""
}

// ProbeCLI is the executable a claude or codex probe runs.
func (c Candidate) ProbeCLI() string {
	switch {
	case c.Probe.CLI != "":
		return c.Probe.CLI
	case c.Claude != nil && c.Probe.Kind == "claude":
		return c.Claude.CLI
	case c.Codex != nil && c.Probe.Kind == "codex":
		return c.Codex.CLI
	}
	return c.Probe.Kind
}

// ProbeEnv inherits the headless target's environment. Explicit probe values
// override inherited values without changing the target's configuration.
func (c Candidate) ProbeEnv() map[string]string {
	env := make(map[string]string)
	var inherited map[string]string
	switch c.Adapter {
	case "claude":
		if c.Claude != nil {
			inherited = c.Claude.Env
		}
	case "codex":
		if c.Codex != nil {
			inherited = c.Codex.Env
		}
	}
	for k, v := range inherited {
		env[k] = v
	}
	for k, v := range c.Probe.Env {
		env[k] = v
	}
	return env
}

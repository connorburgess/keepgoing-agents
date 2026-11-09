// Package config loads and validates keepgoing.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from strings such as "30m".
type Duration struct{ time.Duration }

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// MarshalYAML writes the duration back as a string.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// Config is the top-level keepgoing.yaml document.
type Config struct {
	Version int  `yaml:"version"`
	Loop    Loop `yaml:"loop"`
}

// Loop describes one supervised agent loop.
type Loop struct {
	Name       string     `yaml:"name"`
	StateDir   string     `yaml:"state_dir"`
	Adapter    string     `yaml:"adapter"`
	BB         *BB        `yaml:"bb,omitempty"`
	Process    *Process   `yaml:"process,omitempty"`
	Prompt     string     `yaml:"prompt,omitempty"`
	PromptFile string     `yaml:"prompt_file,omitempty"`
	Progress   Progress   `yaml:"progress"`
	Window     *Window    `yaml:"window,omitempty"`
	Errors     ErrorRules `yaml:"errors"`
}

// BB configures the BB thread adapter.
type BB struct {
	Thread string `yaml:"thread"`
	CLI    string `yaml:"cli,omitempty"`
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

// Progress controls stall detection.
type Progress struct {
	Repositories    []string `yaml:"repositories,omitempty"`
	StallAfter      Duration `yaml:"stall_after,omitempty"`
	MinWakeInterval Duration `yaml:"min_wake_interval,omitempty"`
}

// Window restricts when the supervisor may act. Days map a weekday
// abbreviation (Mon..Sun) to a [start, end] pair of HH:MM times. An end
// earlier than its start runs past midnight into the next day.
type Window struct {
	TZ   string              `yaml:"tz"`
	Days map[string][]string `yaml:"days"`
}

// ErrorRules bound how the supervisor reacts to a session in error.
type ErrorRules struct {
	MaxReprompts int      `yaml:"max_reprompts,omitempty"`
	Backoff      Duration `yaml:"backoff,omitempty"`
}

// Defaults applied when a field is omitted.
const (
	DefaultStallAfter      = 30 * time.Minute
	DefaultMinWakeInterval = 60 * time.Minute
	DefaultMaxReprompts    = 3
	DefaultBackoff         = 10 * time.Minute
	DefaultProcessTimeout  = 20 * time.Second
	DefaultBBCLI           = "bb"
)

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	timeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	days   = map[string]bool{"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true}
)

// Load reads, defaults, and validates a config file. Relative paths inside
// the file resolve against the file's directory.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	base := filepath.Dir(path)
	cfg.Loop.StateDir = resolve(base, cfg.Loop.StateDir)
	if cfg.Loop.PromptFile != "" {
		cfg.Loop.PromptFile = resolve(base, cfg.Loop.PromptFile)
	}
	for i, r := range cfg.Loop.Progress.Repositories {
		cfg.Loop.Progress.Repositories[i] = resolve(base, r)
	}
	return cfg, nil
}

// Parse decodes and validates YAML bytes without touching the filesystem.
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	l := &c.Loop
	if l.Progress.StallAfter.Duration == 0 {
		l.Progress.StallAfter.Duration = DefaultStallAfter
	}
	if l.Progress.MinWakeInterval.Duration == 0 {
		l.Progress.MinWakeInterval.Duration = DefaultMinWakeInterval
	}
	if l.Errors.MaxReprompts == 0 {
		l.Errors.MaxReprompts = DefaultMaxReprompts
	}
	if l.Errors.Backoff.Duration == 0 {
		l.Errors.Backoff.Duration = DefaultBackoff
	}
	if l.BB != nil && l.BB.CLI == "" {
		l.BB.CLI = DefaultBBCLI
	}
	if l.Process != nil && l.Process.Timeout.Duration == 0 {
		l.Process.Timeout.Duration = DefaultProcessTimeout
	}
}

// Validate reports every problem it can find, joined into one error.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Version != 1 {
		add("version must be 1")
	}
	l := c.Loop
	if !nameRE.MatchString(l.Name) {
		add("loop.name must be lowercase letters, digits, and hyphens")
	}
	if l.StateDir == "" {
		add("loop.state_dir is required")
	}
	switch l.Adapter {
	case "bb":
		if l.BB == nil || l.BB.Thread == "" {
			add("loop.bb.thread is required for the bb adapter")
		}
		if l.Process != nil {
			add("loop.process is set but the adapter is bb")
		}
	case "process":
		if l.Process == nil {
			add("loop.process is required for the process adapter")
		} else {
			if len(l.Process.StatusCommand) == 0 {
				add("loop.process.status_command is required")
			}
			if len(l.Process.ResumeCommand) == 0 {
				add("loop.process.resume_command is required")
			}
			for code, class := range l.Process.ExitCodes {
				if !ValidErrorClass(class) {
					add("loop.process.exit_codes[%d]: unknown error class %q", code, class)
				}
			}
		}
		if l.BB != nil {
			add("loop.bb is set but the adapter is process")
		}
	default:
		add("loop.adapter must be bb or process")
	}
	if l.Prompt == "" && l.PromptFile == "" {
		add("one of loop.prompt or loop.prompt_file is required")
	}
	if l.Prompt != "" && l.PromptFile != "" {
		add("set only one of loop.prompt and loop.prompt_file")
	}
	if l.Progress.StallAfter.Duration < time.Minute {
		add("loop.progress.stall_after must be at least 1m")
	}
	if l.Progress.MinWakeInterval.Duration < time.Minute {
		add("loop.progress.min_wake_interval must be at least 1m")
	}
	if l.Errors.MaxReprompts < 1 {
		add("loop.errors.max_reprompts must be at least 1")
	}
	if w := l.Window; w != nil {
		if _, err := time.LoadLocation(w.TZ); err != nil {
			add("loop.window.tz: %v", err)
		}
		if len(w.Days) == 0 {
			add("loop.window.days must list at least one day")
		}
		for day, span := range w.Days {
			if !days[day] {
				add("loop.window.days: unknown day %q (use Mon..Sun)", day)
			}
			if len(span) != 2 || !timeRE.MatchString(span[0]) || !timeRE.MatchString(span[1]) {
				add("loop.window.days.%s must be [\"HH:MM\", \"HH:MM\"]", day)
			}
		}
	}
	return errors.Join(errs...)
}

// ErrorClasses lists the structured error classes an adapter may report.
var ErrorClasses = []string{"none", "rate_limit", "auth", "overloaded", "network", "unknown"}

// ValidErrorClass reports whether s is a known error class.
func ValidErrorClass(s string) bool {
	for _, c := range ErrorClasses {
		if c == s {
			return true
		}
	}
	return false
}

// PromptText returns the configured prompt, reading prompt_file if set.
func (l Loop) PromptText() (string, error) {
	if l.PromptFile == "" {
		return l.Prompt, nil
	}
	b, err := os.ReadFile(l.PromptFile)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("%s is empty", l.PromptFile)
	}
	return s, nil
}

func resolve(base, p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

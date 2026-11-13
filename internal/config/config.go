// Package config loads and validates keepgoing.yaml and fallbacks.yaml.
package config

import (
	"bytes"
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

	// FallbackSet is loaded from Loop.Fallbacks by Load; nil when unset.
	FallbackSet *Fallbacks `yaml:"-"`
	source      sourceSnapshot
}

// Loop describes one supervised agent loop.
type Loop struct {
	Name     string `yaml:"name"`
	StateDir string `yaml:"state_dir"`
	Agent    `yaml:",inline"`

	Prompt      string `yaml:"prompt,omitempty"`
	PromptFile  string `yaml:"prompt_file,omitempty"`
	PromptGoal  bool   `yaml:"prompt_goal,omitempty"`
	HandoffFile string `yaml:"handoff_file,omitempty"`

	Progress  Progress   `yaml:"progress"`
	Window    *Window    `yaml:"window,omitempty"`
	Errors    ErrorRules `yaml:"errors"`
	Fallbacks string     `yaml:"fallbacks,omitempty"`
	Report    *Report    `yaml:"report,omitempty"`
	Yield     *Yield     `yaml:"yield,omitempty"`
	Daemon    Daemon     `yaml:"daemon"`
	// TickTimeout bounds the optional work of one check (fallback probes), so
	// a check records its result and exits well inside an external
	// scheduler's own timeout.
	TickTimeout Duration `yaml:"tick_timeout,omitempty"`
	// tickTimeoutDefaulted: the file did not set tick_timeout.
	tickTimeoutDefaulted bool
	Limits               Limits `yaml:"limits,omitempty"`

	// SwitchBack returns the loop to its primary after a handoff, once the
	// primary's limit has reset and an exact probe of it passes. Off by default.
	SwitchBack SwitchBack `yaml:"switch_back"`
	// Probe says how to confirm the primary works; used by switch-back.
	Probe Probe `yaml:"probe"`
}

// Limits bound future supervisor work, not the lifetime of an agent turn.
type Limits struct {
	NoProgressTimeout Duration `yaml:"no_progress_timeout,omitempty"`
	Deadline          string   `yaml:"deadline,omitempty"`
	MaxSends          int64    `yaml:"max_sends,omitempty"`
}

// DeadlineReached uses the host clock. Configuration validation rejects invalid
// timestamps; an absent deadline preserves existing behavior.
func (l Limits) DeadlineReached(now time.Time) bool {
	deadline, err := time.Parse(time.RFC3339, l.Deadline)
	return err == nil && !now.Before(deadline)
}

// Configured names the loop-wide limits that are set, in the order the decision
// engine applies them. An empty result means nothing bounds repeated automatic
// work across turns; the per-turn nudge and re-prompt caps are not listed
// because they reset. It reports configuration only, not whether a limit has
// been reached or its accounting is readable.
func (l Limits) Configured() []string {
	names := []string{}
	if l.Deadline != "" {
		names = append(names, "deadline")
	}
	if l.MaxSends > 0 {
		names = append(names, "max_sends")
	}
	if l.NoProgressTimeout.Duration > 0 {
		names = append(names, "no_progress_timeout")
	}
	return names
}

// UnboundedWarning is shown by validate and status when no loop-wide limit is
// configured. It is advice: nothing is refused and no default is applied.
const UnboundedWarning = "no loop-wide limit is configured (loop.limits deadline, max_sends or no_progress_timeout); automatic sends repeat until complete, stop, pause or a hold"

// SwitchBack controls the automatic return to the primary.
type SwitchBack struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// MinAfter is the earliest a switch-back is tried after a handoff when the
	// provider reported no reset time.
	MinAfter Duration `yaml:"min_after,omitempty"`
	// RetryEvery spaces out attempts after a failed probe of the primary.
	RetryEvery Duration `yaml:"retry_every,omitempty"`
}

// Progress controls stall detection.
type Progress struct {
	Repositories    []string `yaml:"repositories,omitempty"`
	StallAfter      Duration `yaml:"stall_after,omitempty"`
	MinWakeInterval Duration `yaml:"min_wake_interval,omitempty"`
	// MaxNudges is how many nudges a stalled session gets before the loop
	// asks a person. Progress or ending the turn resets the count.
	MaxNudges int `yaml:"max_nudges,omitempty"`
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
	MaxReprompts      int      `yaml:"max_reprompts,omitempty"`
	Backoff           Duration `yaml:"backoff,omitempty"`
	FailoverOn        []string `yaml:"failover_on,omitempty"`
	WaitIfResetWithin Duration `yaml:"wait_if_reset_within,omitempty"`
	// ReportWaitAfter: when a limit is being waited out with no handoff
	// possible and its reported reset is further away than this, report once
	// that a person may be needed. It never holds the loop or changes a decision.
	ReportWaitAfter Duration `yaml:"report_wait_after,omitempty"`
}

// Report configures signed webhook reports.
type Report struct {
	URLEnv     string   `yaml:"url_env,omitempty"`
	SecretEnv  string   `yaml:"secret_env,omitempty"`
	SecretFile string   `yaml:"secret_file,omitempty"`
	Source     string   `yaml:"source,omitempty"`
	Scheme     string   `yaml:"scheme,omitempty"` // standard-webhooks or bearer
	Format     string   `yaml:"format,omitempty"` // standard or text
	Events     []string `yaml:"events,omitempty"`
	Timeout    Duration `yaml:"timeout,omitempty"`
}

// Yield lets a tick step aside for other work. When BusyCommand exits 0 the
// tick does nothing and exits 75.
type Yield struct {
	BusyCommand []string `yaml:"busy_command"`
	Timeout     Duration `yaml:"timeout,omitempty"`
}

// Daemon configures `keepgoing daemon`.
type Daemon struct {
	Interval    Duration `yaml:"interval,omitempty"`
	ControlPoll Duration `yaml:"control_poll,omitempty"`
}

// Defaults applied when a field is omitted.
const (
	DefaultStallAfter        = 30 * time.Minute
	DefaultMinWakeInterval   = 60 * time.Minute
	DefaultMaxReprompts      = 3
	DefaultMaxNudges         = 3
	DefaultBackoff           = 10 * time.Minute
	DefaultWaitIfResetWithin = 30 * time.Minute
	DefaultReportWaitAfter   = 6 * time.Hour
	DefaultProcessTimeout    = 20 * time.Second
	DefaultBBCLI             = "bb"
	DefaultDaemonInterval    = 5 * time.Minute
	DefaultTickTimeout       = 3 * time.Minute
	DefaultControlPoll       = 2 * time.Second
	DefaultReportTimeout     = 20 * time.Second
	DefaultYieldTimeout      = 10 * time.Second
	DefaultSwitchBackAfter   = time.Hour
	DefaultSwitchBackRetry   = 30 * time.Minute
)

// ReportEvents lists the events a report may subscribe to.
var ReportEvents = []string{"wake", "nudge", "reprompt", "handoff", "degraded", "recovered", "await_user", "complete", "stop", "error", "probe_failed", "switch_back", "limit_wait", "child_error", "test"}

// DefaultReportEvents are sent when report.events is empty. limit_wait and
// child_error are among them because, like await_user, they say a person may
// be needed; an explicit list must name them (or all) to receive them.
var DefaultReportEvents = []string{"handoff", "switch_back", "degraded", "recovered", "await_user", "limit_wait", "child_error", "complete", "stop", "error"}

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	timeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	days   = map[string]bool{"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true}
)

// Load reads, defaults, and validates a config file and its fallbacks file.
// Relative paths inside a file resolve against that file's directory.
func Load(path string) (*Config, error) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs // so state_dir and every relative path become absolute
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.source = snapshot(path, raw)
	base := filepath.Dir(path)
	l := &cfg.Loop
	l.StateDir = resolve(base, l.StateDir)
	for _, p := range []*string{&l.PromptFile, &l.HandoffFile, &l.Fallbacks} {
		if *p != "" {
			*p = resolve(base, *p)
		}
	}
	if l.Report != nil && l.Report.SecretFile != "" {
		l.Report.SecretFile = resolve(base, l.Report.SecretFile)
	}
	for i, r := range l.Progress.Repositories {
		l.Progress.Repositories[i] = resolve(base, r)
	}
	l.Agent.resolvePaths(base)
	l.Probe.CLI = resolveExec(base, l.Probe.CLI)
	resolveArgv(base, l.Probe.Command)
	if l.Probe.Workdir != "" {
		l.Probe.Workdir = resolve(base, l.Probe.Workdir)
	}
	if l.Yield != nil {
		resolveArgv(base, l.Yield.BusyCommand)
	}
	if l.Fallbacks != "" {
		fb, err := LoadFallbacks(l.Fallbacks)
		if err != nil {
			return nil, err
		}
		cfg.FallbackSet = fb
		if err := cfg.Loop.fitTickTimeout(fb.Probe.Timeout.Duration); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// TickTimeoutProbeMargin is what a check needs besides one scheduled probe:
// a brief observation, and the time kept back for deciding, sending and
// reporting. tick_timeout must exceed fallbacks probe.timeout by this much,
// or no scheduled probe could ever be started.
const TickTimeoutProbeMargin = 90 * time.Second

// TickTimeoutSchedulerNote is the longest tick_timeout whose worst case still
// ends inside a five-minute scheduler timeout.
const TickTimeoutSchedulerNote = 4 * time.Minute

// fitTickTimeout makes room for one whole scheduled probe. An omitted
// tick_timeout grows to fit, so an existing file with a long probe timeout
// keeps loading; an explicit one that is too short is an error.
func (l *Loop) fitTickTimeout(probeTimeout time.Duration) error {
	need := probeTimeout + TickTimeoutProbeMargin
	if l.TickTimeout.Duration >= need {
		return nil
	}
	if !l.tickTimeoutDefaulted {
		return fmt.Errorf("loop.tick_timeout (%s) must be at least fallbacks probe.timeout plus %s (%s), or scheduled probes could never run", l.TickTimeout.Duration, TickTimeoutProbeMargin, need)
	}
	l.TickTimeout.Duration = need
	return nil
}

// TickTimeoutWarning is non-empty when a check's worst case may outlast a
// five-minute scheduler timeout. The daemon and managed services have no
// such timeout.
// TickTimeoutGrown reports whether an omitted tick_timeout was raised above
// its default to fit the fallback probe timeout.
func (l Loop) TickTimeoutGrown() bool {
	return l.tickTimeoutDefaulted && l.TickTimeout.Duration > DefaultTickTimeout
}

func (l Loop) TickTimeoutWarning() string {
	if l.TickTimeout.Duration <= TickTimeoutSchedulerNote {
		return ""
	}
	return fmt.Sprintf("loop.tick_timeout is %s; with an external scheduler, give its own timeout at least %s more than that, or a slow check is killed before it records anything", l.TickTimeout.Duration, 90*time.Second)
}

// Parse decodes and validates YAML bytes without touching the filesystem.
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
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
	l.Agent.applyDefaults()
	if l.Group == "" {
		l.Group = "primary"
	}
	setDur(&l.Progress.StallAfter, DefaultStallAfter)
	setDur(&l.Progress.MinWakeInterval, DefaultMinWakeInterval)
	if l.Progress.MaxNudges == 0 {
		l.Progress.MaxNudges = DefaultMaxNudges
	}
	if l.Errors.MaxReprompts == 0 {
		l.Errors.MaxReprompts = DefaultMaxReprompts
	}
	setDur(&l.Errors.Backoff, DefaultBackoff)
	setDur(&l.Errors.WaitIfResetWithin, DefaultWaitIfResetWithin)
	setDur(&l.Errors.ReportWaitAfter, DefaultReportWaitAfter)
	if l.Errors.FailoverOn == nil {
		l.Errors.FailoverOn = []string{"rate_limit"}
	}
	setDur(&l.SwitchBack.MinAfter, DefaultSwitchBackAfter)
	setDur(&l.SwitchBack.RetryEvery, DefaultSwitchBackRetry)
	if l.Probe.Kind == "" {
		switch l.Provider {
		case "claude-code":
			l.Probe.Kind = "claude"
		case "codex":
			l.Probe.Kind = "codex"
		}
	}
	setDur(&l.Daemon.Interval, DefaultDaemonInterval)
	l.tickTimeoutDefaulted = l.TickTimeout.Duration == 0
	setDur(&l.TickTimeout, DefaultTickTimeout)
	setDur(&l.Daemon.ControlPoll, DefaultControlPoll)
	if r := l.Report; r != nil {
		if r.URLEnv == "" {
			r.URLEnv = "KEEPGOING_WEBHOOK_URL"
		}
		if r.SecretEnv == "" {
			r.SecretEnv = "KEEPGOING_WEBHOOK_SECRET"
		}
		if r.Source == "" {
			r.Source = l.Name
		}
		if r.Scheme == "" {
			r.Scheme = "standard-webhooks"
		}
		if r.Format == "" {
			r.Format = "standard"
		}
		if len(r.Events) == 0 {
			r.Events = DefaultReportEvents
		}
		setDur(&r.Timeout, DefaultReportTimeout)
	}
	if y := l.Yield; y != nil {
		setDur(&y.Timeout, DefaultYieldTimeout)
	}
}

func setDur(d *Duration, v time.Duration) {
	if d.Duration == 0 {
		d.Duration = v
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
	if d := l.Limits.NoProgressTimeout.Duration; d != 0 {
		if d < time.Minute {
			add("loop.limits.no_progress_timeout must be zero or at least 1m")
		}
		if len(l.Progress.Repositories) == 0 {
			add("loop.limits.no_progress_timeout requires progress.repositories")
		}
	}
	if l.Limits.MaxSends < 0 {
		add("loop.limits.max_sends must be nonnegative")
	}
	if l.Limits.Deadline != "" {
		if _, err := time.Parse(time.RFC3339, l.Limits.Deadline); err != nil {
			add("loop.limits.deadline must be an RFC3339 timestamp with a timezone")
		}
	}
	errs = append(errs, l.Agent.validate("loop.")...)
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
	if l.Progress.MaxNudges < 1 {
		add("loop.progress.max_nudges must be at least 1")
	}
	if l.Errors.MaxReprompts < 1 {
		add("loop.errors.max_reprompts must be at least 1")
	}
	if l.Errors.ReportWaitAfter.Duration < 0 {
		add("loop.errors.report_wait_after must not be negative")
	}
	for _, class := range l.Errors.FailoverOn {
		if !ValidErrorClass(class) || class == "none" || class == "unknown" {
			add("loop.errors.failover_on: %q is not allowed (use rate_limit, auth, overloaded, or network)", class)
		}
	}
	if l.TickTimeout.Duration < 30*time.Second {
		add("loop.tick_timeout must be at least 30s")
	}
	if l.Daemon.Interval.Duration < 10*time.Second {
		add("loop.daemon.interval must be at least 10s")
	}
	if l.Daemon.ControlPoll.Duration < 100*time.Millisecond {
		add("loop.daemon.control_poll must be at least 100ms")
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
			} else if span[0] == span[1] {
				add("loop.window.days.%s: start and end are equal; use [\"00:00\", \"23:59\"] for a whole day", day)
			}
		}
	}
	if r := l.Report; r != nil {
		if r.Scheme != "standard-webhooks" && r.Scheme != "bearer" {
			add("loop.report.scheme must be standard-webhooks or bearer")
		}
		if r.Format != "standard" && r.Format != "text" {
			add("loop.report.format must be standard or text")
		}
		for _, e := range r.Events {
			if e != "all" && !contains(ReportEvents, e) {
				add("loop.report.events: unknown event %q", e)
			}
		}
	}
	if l.Adapter == "cursor" && (l.Fallbacks != "" || l.SwitchBack.Enabled) {
		add("Cursor currently supports standalone primary loops only; fallbacks and switch-back are unverified")
	}
	if l.SwitchBack.Enabled {
		switch l.Probe.Kind {
		case "claude", "codex":
		case "command":
			if len(l.Probe.Command) == 0 {
				add("loop.probe.command is required for a command probe")
			}
		case "":
			add("loop.switch_back needs a way to probe the primary: set loop.provider to claude-code or codex, or set loop.probe")
		default:
			add("loop.probe.kind must be claude, codex, or command")
		}
		if l.Fallbacks == "" {
			add("loop.switch_back needs loop.fallbacks; without fallbacks the loop never leaves the primary")
		}
	}
	if y := l.Yield; y != nil && len(y.BusyCommand) == 0 {
		add("loop.yield.busy_command is required when yield is set")
	}
	return errors.Join(errs...)
}

// ErrorClasses lists the structured error classes an adapter may report.
var ErrorClasses = []string{"none", "rate_limit", "auth", "overloaded", "network", "unknown"}

// ValidErrorClass reports whether s is a known error class.
func ValidErrorClass(s string) bool { return contains(ErrorClasses, s) }

func contains(list []string, s string) bool {
	for _, c := range list {
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
	return readNonEmpty(l.PromptFile)
}

// HandoffText returns the handoff file's contents, or "" when no handoff
// file is configured or it does not exist.
func (l Loop) HandoffText() (string, error) {
	if l.HandoffFile == "" {
		return "", nil
	}
	if _, err := os.Stat(l.HandoffFile); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return readNonEmpty(l.HandoffFile)
}

// ReportsEvent reports whether the report subscribes to event.
func (r *Report) ReportsEvent(event string) bool {
	if r == nil {
		return false
	}
	return contains(r.Events, "all") || contains(r.Events, event)
}

func readNonEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return s, nil
}

// resolveExec makes a relative executable path such as ./status.sh absolute
// against base. A bare name such as bb is left for PATH lookup.
func resolveExec(base, p string) string {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~/") || !strings.ContainsAny(p, `/\`) {
		if strings.HasPrefix(p, "~/") {
			return resolve(base, p)
		}
		return p
	}
	return resolve(base, p)
}

func resolveArgv(base string, argv []string) {
	if len(argv) > 0 {
		argv[0] = resolveExec(base, argv[0])
	}
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

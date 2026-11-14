// Package config loads and validates a loop's configuration file
// (looptender.yaml, or keepgoing.yaml from before the rename) and
// fallbacks.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
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

// Config is the top-level document of a loop's configuration file.
type Config struct {
	Version int  `yaml:"version"`
	Loop    Loop `yaml:"loop"`

	// FallbackSet is loaded from Loop.Fallbacks by Load; nil when unset.
	FallbackSet *Fallbacks `yaml:"-"`
	// RouteList says where the loop's other routes came from. Load derives it.
	RouteList    RouteStatus `yaml:"-"`
	routesSource sourceSnapshot
	source       sourceSnapshot
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

	Progress Progress   `yaml:"progress"`
	Window   *Window    `yaml:"window,omitempty"`
	Errors   ErrorRules `yaml:"errors"`
	// Refusal sets when a streak of refused turns asks a person instead of
	// continuing.
	Refusal   RefusalRules `yaml:"refusal,omitempty"`
	Fallbacks string       `yaml:"fallbacks,omitempty"`
	// Routes is "standard" for a loop that follows the person's standard
	// route list. It excludes Fallbacks, which is a list of the loop's own.
	Routes string `yaml:"routes,omitempty"`
	// RoutesAllow opts the loop in to kinds of standard route that are off by
	// default. The only value is new_conversation.
	RoutesAllow []string `yaml:"routes_allow,omitempty"`
	Report      *Report  `yaml:"report,omitempty"`
	Yield       *Yield   `yaml:"yield,omitempty"`
	Daemon      Daemon   `yaml:"daemon"`
	Network     Network  `yaml:"network,omitempty"`
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
	// Timing is usage-aware timing for the loop's first route. Off by
	// default. Until the step decision exists (roadmap U4) it only collects
	// usage readings of the first route's allowance; it never changes routes.
	Timing *Timing `yaml:"timing,omitempty"`
}

// Timing collects usage readings of the first route's allowance by probing
// it on a schedule, including while the loop runs on it. Each probe is a small
// paid request; Enabled is the owner's choice and is off by default.
type Timing struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// Windows names the provider's usage windows timing acts on, by their
	// opaque keys (for example seven_day).
	Windows []string `yaml:"windows,omitempty"`
	// NearReset is how close to a named window's reported reset counts as
	// near; one probe is made when that period begins.
	NearReset Duration `yaml:"near_reset,omitempty"`
	// ProbeEvery is the interval between timing probes of the first route.
	ProbeEvery Duration `yaml:"probe_every,omitempty"`
}

// TimingOn reports whether usage-aware timing is turned on for the loop.
func (l Loop) TimingOn() bool { return l.Timing != nil && l.Timing.Enabled }

// Limits bound future supervisor work, not the lifetime of an agent turn.
type Limits struct {
	NoProgressTimeout Duration `yaml:"no_progress_timeout,omitempty"`
	Deadline          string   `yaml:"deadline,omitempty"`
	MaxSends          int64    `yaml:"max_sends,omitempty"`
	// MaxNewThreads caps the BB threads this loop may create for handoffs to
	// candidates with bb.new_thread. Zero means the default. It is not a
	// loop-wide work bound and is not listed by Configured.
	MaxNewThreads int `yaml:"max_new_threads,omitempty"`
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
	// FlagAfterSends and FlagAfterErrors raise the panel's no-progress flag:
	// at that many sends, or that many checks that found an error or retried
	// the turn, with no recorded change to the repositories. Unset means the
	// default; 0 turns that fact's flag off. The flag only informs a person.
	FlagAfterSends  *FlagCount `yaml:"flag_after_sends,omitempty"`
	FlagAfterErrors *FlagCount `yaml:"flag_after_errors,omitempty"`
}

// FlagCount is a whole number of events. YAML would otherwise read 2.5 as 2
// without saying so; anything but an integer is refused.
type FlagCount int

func (c *FlagCount) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		return fmt.Errorf("line %d: %q is not a whole number", n.Line, n.Value)
	}
	var v int
	if err := n.Decode(&v); err != nil {
		return err
	}
	*c = FlagCount(v)
	return nil
}

// Defaults of the no-progress flag's thresholds.
const (
	DefaultFlagAfterSends  = 6
	DefaultFlagAfterErrors = 4
	maxFlagAfter           = 1000
)

// SendsFlag is the send count that raises the no-progress flag; 0 is off.
func (p Progress) SendsFlag() int {
	if p.FlagAfterSends == nil {
		return DefaultFlagAfterSends
	}
	return int(*p.FlagAfterSends)
}

// ErrorsFlag is the error-or-retry count that raises the no-progress flag;
// 0 is off.
func (p Progress) ErrorsFlag() int {
	if p.FlagAfterErrors == nil {
		return DefaultFlagAfterErrors
	}
	return int(*p.FlagAfterErrors)
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

// Network configures the connectivity check made before work that needs the
// network: an error re-prompt, a handoff and fallback probes. With Check off
// nothing is checked and that work is attempted as before.
type Network struct {
	// Check is "on" (the default) or "off".
	Check string `yaml:"check,omitempty"`
	// URL answers an HTTP GET when the network is up. Any response counts.
	URL string `yaml:"url,omitempty"`
	// Command exits 0 when the network is up. It replaces the URL.
	Command []string `yaml:"command,omitempty"`
	Timeout Duration `yaml:"timeout,omitempty"`
	// VerifyAfter: once the check has failed this long, and again at this
	// interval, the provider is tested directly with one probe and a report
	// is attempted, so a check that is wrong cannot hold a loop in silence.
	VerifyAfter Duration `yaml:"verify_after,omitempty"`
}

// Enabled reports whether connectivity is checked. A loop built in code
// without defaults has no check.
func (n Network) Enabled() bool { return n.Check == "on" }

// NetworkCheckTimeout is the time one connectivity check may use, or zero
// when it is not made. The check is a mandatory step of a check's time
// accounting, so it may use only what tick_timeout has beyond MinTickTimeout:
// a file with the smallest tick_timeout still loads and simply has no room
// for it.
func (l Loop) NetworkCheckTimeout() time.Duration {
	if !l.Network.Enabled() {
		return 0
	}
	room := l.TickTimeout.Duration - MinTickTimeout
	if room < time.Second {
		return 0
	}
	return min(l.Network.Timeout.Duration, room)
}

// NetworkCheckWarning is non-empty when the check is on but cannot be made,
// or is given less time than configured.
func (l Loop) NetworkCheckWarning() string {
	if !l.Network.Enabled() {
		return ""
	}
	got, want := l.NetworkCheckTimeout(), l.Network.Timeout.Duration
	switch {
	case got == 0:
		return fmt.Sprintf("loop.tick_timeout (%s) leaves no room for the connectivity check, so it is not made and nothing waits for the network; set tick_timeout to at least %s", l.TickTimeout.Duration, MinTickTimeout+want)
	case got < want:
		return fmt.Sprintf("loop.tick_timeout (%s) leaves %s for the connectivity check instead of network.timeout (%s); set tick_timeout to at least %s", l.TickTimeout.Duration, got, want, MinTickTimeout+want)
	}
	return ""
}

// DefaultNetworkTimeout bounds one connectivity check, and
// DefaultNetworkVerifyAfter how long a failing check is waited on before the
// provider is tested directly and a report attempted.
const (
	DefaultNetworkTimeout     = 8 * time.Second
	DefaultNetworkVerifyAfter = time.Hour
)

// Daemon configures `keepgoing daemon`.
type Daemon struct {
	Interval    Duration `yaml:"interval,omitempty"`
	ControlPoll Duration `yaml:"control_poll,omitempty"`
}

// Defaults applied when a field is omitted.
const (
	DefaultStallAfter      = 30 * time.Minute
	DefaultMinWakeInterval = 60 * time.Minute
	DefaultMaxReprompts    = 3
	DefaultMaxNewThreads   = 3
	// MaxNewThreadsCeiling is the most loop.limits.max_new_threads may be, and
	// MinNewThreadInterval the least time between two threads a loop creates.
	// Together: at most ten threads per loop ever, and at most one an hour.
	MaxNewThreadsCeiling     = 10
	MinNewThreadInterval     = time.Hour
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
	DefaultTimingNearReset   = 6 * time.Hour
	DefaultTimingProbeEvery  = 12 * time.Hour
)

// ReportEvents lists the events a report may subscribe to.
var ReportEvents = []string{"wake", "nudge", "reprompt", "handoff", "degraded", "recovered", "await_user", "complete", "stop", "error", "probe_failed", "switch_back", "network_wait", "limit_wait", "child_error", "child_recovery", "check_gap", "hung_turn", "refusal_streak", "test"}

// DefaultReportEvents are sent when report.events is empty. limit_wait and
// child_error are among them because, like await_user, they say a person may
// be needed; an explicit list must name them (or all) to receive them.
var DefaultReportEvents = []string{"handoff", "switch_back", "degraded", "recovered", "await_user", "limit_wait", "network_wait", "child_error", "child_recovery", "refusal_streak", "complete", "stop", "error"}

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	timeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	// windowKeyRE is a provider's usage window key as readings store it.
	windowKeyRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	days        = map[string]bool{"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true}
)

// Load reads, defaults, and validates a config file and its fallbacks file.
// Relative paths inside a file resolve against that file's directory.
func Load(path string) (*Config, error) {
	if path == ambiguousDefault {
		return nil, ErrDefaultAmbiguous
	}
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
	resolveArgv(base, l.Network.Command)
	if l.Yield != nil {
		resolveArgv(base, l.Yield.BusyCommand)
	}
	if l.Fallbacks != "" {
		fb, err := LoadFallbacks(l.Fallbacks)
		if err != nil {
			return nil, err
		}
		cfg.FallbackSet = fb
		cfg.RouteList = RouteStatus{Source: "own"}
		if err := cfg.Loop.fitTickTimeout(fb.Probe.Timeout.Duration); err != nil {
			return nil, err
		}
	} else if l.FollowsStandard() {
		cfg.loadStandardRoutes()
	}
	return cfg, nil
}

// TickTimeoutProbeMargin is what a check needs besides one scheduled probe:
// a brief observation, and the time kept back for deciding, sending and
// reporting. tick_timeout must exceed fallbacks probe.timeout by this much,
// or no scheduled probe could ever be started.
const TickTimeoutProbeMargin = 90 * time.Second

// MinTickTimeout is the least tick_timeout. A check's hard limit is
// tick_timeout plus TickTail, and the steps that cannot be skipped (yield,
// observation, fingerprint, send and three brief reports) can take 250s.
const MinTickTimeout = 160 * time.Second

// TickTail is added to tick_timeout to give a check's hard limit.
const TickTail = 90 * time.Second

// TickTimeoutSchedulerNote is the longest tick_timeout whose worst case still
// ends inside a five-minute scheduler timeout.
const TickTimeoutSchedulerNote = 3*time.Minute + 15*time.Second

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
	return fmt.Sprintf("loop.tick_timeout is %s, so a check may take up to %s; an external scheduler's own timeout must be longer than that, or a slow check is killed before it records anything", l.TickTimeout.Duration, l.TickTimeout.Duration+TickTail)
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
	if l.Limits.MaxNewThreads == 0 {
		l.Limits.MaxNewThreads = DefaultMaxNewThreads
	}
	setDur(&l.Errors.Backoff, DefaultBackoff)
	setDur(&l.Errors.WaitIfResetWithin, DefaultWaitIfResetWithin)
	setDur(&l.Errors.ReportWaitAfter, DefaultReportWaitAfter)
	if l.Errors.FailoverOn == nil {
		l.Errors.FailoverOn = []string{"rate_limit"}
	}
	setDur(&l.SwitchBack.MinAfter, DefaultSwitchBackAfter)
	setDur(&l.SwitchBack.RetryEvery, DefaultSwitchBackRetry)
	if l.Timing != nil {
		setDur(&l.Timing.NearReset, DefaultTimingNearReset)
		setDur(&l.Timing.ProbeEvery, DefaultTimingProbeEvery)
	}
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
			r.URLEnv = envvar.WebhookURL.Name() // report also reads the earlier name
		}
		if r.SecretEnv == "" {
			r.SecretEnv = envvar.WebhookSecret.Name()
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
	if l.Network.Check == "" {
		l.Network.Check = "on"
	}
	setDur(&l.Network.Timeout, DefaultNetworkTimeout)
	setDur(&l.Network.VerifyAfter, DefaultNetworkVerifyAfter)
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
	if l.Limits.MaxNewThreads < 0 || l.Limits.MaxNewThreads > MaxNewThreadsCeiling {
		add("loop.limits.max_new_threads must be between 0 and %d (zero means the default)", MaxNewThreadsCeiling)
	}
	if l.BB != nil && l.BB.NewThread {
		add("loop.bb.new_thread is for fallback candidates only: the primary needs an existing thread")
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
	for _, f := range []struct {
		name string
		v    *FlagCount
	}{{"flag_after_sends", l.Progress.FlagAfterSends}, {"flag_after_errors", l.Progress.FlagAfterErrors}} {
		if f.v != nil && (*f.v < 0 || *f.v > maxFlagAfter) {
			add("loop.progress.%s must be from 0 (off) to %d", f.name, maxFlagAfter)
		}
	}
	if a := l.Refusal.AskAfter; a != nil && (*a < 0 || *a > 1000) {
		add("loop.refusal.ask_after must be 0 (never ask) to 1000")
	}
	if l.Errors.MaxReprompts < 1 {
		add("loop.errors.max_reprompts must be at least 1")
	}
	if l.Errors.ReportWaitAfter.Duration < 0 {
		add("loop.errors.report_wait_after must not be negative")
	}
	for _, class := range l.Errors.FailoverOn {
		if DeclinedClass(class) {
			add("loop.errors.failover_on: %q is not allowed: a provider refusal or restriction is not a limit and never causes a handoff", class)
		} else if !ValidErrorClass(class) || class == "none" || class == "unknown" {
			add("loop.errors.failover_on: %q is not allowed (use rate_limit, auth, overloaded, or network)", class)
		}
	}
	if l.TickTimeout.Duration < MinTickTimeout {
		add("loop.tick_timeout must be at least %s", MinTickTimeout)
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
	switch l.Routes {
	case "":
	case RoutesStandard:
		if l.Fallbacks != "" {
			add("loop.routes: a loop follows the standard list or has its own loop.fallbacks, not both")
		}
		if l.Adapter != "bb" && l.Adapter != "claude" && l.Adapter != "codex" {
			add("loop.routes: the standard list covers bb, claude and codex loops; give a %s loop its own list", l.Adapter)
		}
	default:
		add("loop.routes must be %q or absent", RoutesStandard)
	}
	for _, kind := range l.RoutesAllow {
		if kind != EnterNewConversation {
			add("loop.routes_allow: the only value is %s", EnterNewConversation)
		}
	}
	if len(l.RoutesAllow) > 0 && !l.FollowsStandard() {
		add("loop.routes_allow applies only with loop.routes: %s", RoutesStandard)
	}
	if l.Adapter == "cursor" && (l.HasRouteList() || l.SwitchBack.Enabled) {
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
		if !l.HasRouteList() {
			add("loop.switch_back needs loop.fallbacks; without fallbacks the loop never leaves the primary")
		}
	}
	if l.TimingOn() {
		tm := l.Timing
		if l.Adapter == "cursor" {
			add("loop.timing is not supported for Cursor loops")
		}
		switch l.Probe.Kind {
		case "claude", "codex":
		case "command":
			if len(l.Probe.Command) == 0 {
				add("loop.probe.command is required for a command probe")
			}
		case "":
			add("loop.timing needs a way to probe the first route: set loop.provider to claude-code or codex, or set loop.probe")
		default:
			add("loop.probe.kind must be claude, codex, or command")
		}
		if !l.HasRouteList() {
			add("loop.timing needs loop.fallbacks or loop.routes: standard: that list's probe policy (daily_cap, timeout) applies to the first route's timing probes")
		}
		if len(tm.Windows) == 0 {
			add("loop.timing.windows must name at least one usage window, for example seven_day")
		}
		seen := map[string]bool{}
		for _, k := range tm.Windows {
			if !windowKeyRE.MatchString(k) {
				add("loop.timing.windows: %q is not a window key (letters, digits, '_', '-' and '.', at most 64)", k)
			} else if seen[k] {
				add("loop.timing.windows: %q is listed twice", k)
			}
			seen[k] = true
		}
		if d := tm.NearReset.Duration; d < time.Minute || d > 7*24*time.Hour {
			add("loop.timing.near_reset must be between 1m and 168h")
		}
		if d := tm.ProbeEvery.Duration; d < time.Hour {
			add("loop.timing.probe_every must be at least 1h")
		}
	}
	switch l.Network.Check {
	case "on", "off":
	default:
		add("loop.network.check must be on or off")
	}
	if l.Network.URL != "" && len(l.Network.Command) > 0 {
		add("loop.network: set url or command, not both")
	}
	if raw := l.Network.URL; raw != "" {
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			add("loop.network.url must be an http or https URL with a host")
		}
	}
	if c := l.Network.Command; len(c) > 0 && strings.TrimSpace(c[0]) == "" {
		add("loop.network.command must name a program")
	}
	if d := l.Network.VerifyAfter.Duration; d < time.Minute {
		add("loop.network.verify_after must be at least 1m")
	}
	if d := l.Network.Timeout.Duration; d < time.Second || d > 30*time.Second {
		add("loop.network.timeout must be between 1s and 30s")
	}
	if y := l.Yield; y != nil && len(y.BusyCommand) == 0 {
		add("loop.yield.busy_command is required when yield is set")
	}
	return errors.Join(errs...)
}

// ErrorClasses lists the structured error classes an adapter may report.
var ErrorClasses = []string{"none", "rate_limit", "auth", "restricted", "refused", "overloaded", "network", "unknown"}

// DeclinedCode says which structured codes keepgoing itself reads as a
// provider denying permission ("restricted") or a model or provider declining
// the turn ("refused"), and "" for every other code. It is the one list both
// the classifier and the validation of agent definitions use, so a
// definition cannot relabel such a code as a limit.
//
// "policy" is BB's typed category for a provider's policy block, and the two
// Codex identifiers are what BB's Codex provider maps to it.
// "oauth_org_not_allowed" is Claude Code's code for an organisation that may
// not use the login.
func DeclinedCode(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "403", "restricted", "permission_denied", "permission_error", "forbidden", "oauth_org_not_allowed":
		return "restricted"
	case "refused", "refusal", "policy", "cyberpolicy", "misalignmentpolicyviolation":
		return "refused"
	}
	return ""
}

// DeclinedClass reports whether a class is a restriction or a refusal: a
// decision against the request, which never causes a handoff.
func DeclinedClass(class string) bool { return class == "restricted" || class == "refused" }

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

// DefaultRefusalAskAfter is how many consecutive refused turns, with no turn
// recovered by a continue in between, make a loop ask a person how to go on.
const DefaultRefusalAskAfter = 10

// RefusalRules configures the answer to refused turns. A refused turn gets a
// plain continue in the same session, spaced by the streak; after AskAfter
// refusals in a row the loop stops continuing and asks a person.
type RefusalRules struct {
	// AskAfter: nil means DefaultRefusalAskAfter, 0 means never ask.
	AskAfter *int `yaml:"ask_after,omitempty"`
}

// AskAfterCount is the threshold in force: 0 means never ask.
func (r RefusalRules) AskAfterCount() int {
	if r.AskAfter == nil {
		return DefaultRefusalAskAfter
	}
	return *r.AskAfter
}

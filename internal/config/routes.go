package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RoutesStandard is the only value of loop.routes: the loop follows the
// person's standard route list instead of a fallbacks file of its own.
const RoutesStandard = "standard"

// RoutesFileName is the standard route list, kept once per user outside every
// loop's directory.
const RoutesFileName = "routes.yaml"

// RoutesFileEnv names an absolute path that replaces the default location. It
// exists for tests and for people who keep configuration elsewhere.
const RoutesFileEnv = "KEEPGOING_ROUTES_FILE"

// MaxStandardRoutes bounds the standard list.
const MaxStandardRoutes = 64

// How a standard route is entered.
const (
	EnterSameConversation = "same_conversation" // this loop's conversation, on another model
	EnterNewConversation  = "new_conversation"  // a conversation started at the handoff
	EnterHeadless         = "headless"          // a headless agent in the loop's working directory
)

// Routes is the routes.yaml document: the rules for trusting routes, as in a
// fallbacks file, and an ordered list of route templates. A template never
// names a conversation, a project or a directory; Resolve supplies those from
// the loop that follows the list.
type Routes struct {
	Version    int         `yaml:"version"`
	Redundancy Redundancy  `yaml:"redundancy,omitempty"`
	Probe      ProbePolicy `yaml:"probe,omitempty"`
	Routes     []Route     `yaml:"routes"`
	source     sourceSnapshot
}

// Route is one template in the standard list.
type Route struct {
	ID       string `yaml:"id"`
	Provider string `yaml:"provider"` // claude-code or codex
	Model    string `yaml:"model"`
	Effort   string `yaml:"effort,omitempty"`
	Group    string `yaml:"group"` // the usage limit this route draws on
	Enter    string `yaml:"enter"`
	// PermissionMode applies to a conversation started for a
	// new_conversation route.
	PermissionMode string `yaml:"permission_mode,omitempty"`
	// AccountDir is an existing account directory for a headless route.
	AccountDir string     `yaml:"account_dir,omitempty"`
	Probe      RouteProbe `yaml:"probe,omitempty"`
	Disabled   bool       `yaml:"disabled,omitempty"`
}

// RouteProbe says how a route is confirmed. The kind follows the provider.
type RouteProbe struct {
	Model      string `yaml:"model,omitempty"`       // cheaper model for scheduled checks
	AccountDir string `yaml:"account_dir,omitempty"` // an existing login for the check
}

// RouteSkip says why one standard route does not apply to a loop.
type RouteSkip struct {
	ID     string
	Reason string
}

// RouteStatus describes where a loaded loop's routes came from. It is derived
// by Load and never written to YAML.
type RouteStatus struct {
	// Source is "own" for a fallbacks file, "standard" for a loop that follows
	// the standard list, and "" for a loop with no other routes configured.
	Source string
	// Unreadable is true when the loop follows the standard list and that list
	// is missing, unreadable or invalid. The loop then has no other routes.
	Unreadable bool
	// Skipped lists standard routes that do not apply to this loop.
	Skipped []RouteSkip
	// Warnings are conditions that do not stop supervision.
	Warnings []string
}

// StandardRoutesUnreadable is the fixed text for a following loop whose list
// cannot be used.
const StandardRoutesUnreadable = "standard route list unreadable: no other routes available"

// FollowsStandard reports whether the loop follows the standard route list.
func (l Loop) FollowsStandard() bool { return l.Routes == RoutesStandard }

// HasRouteList reports whether the loop has other routes configured, by a
// fallbacks file of its own or by following the standard list.
func (l Loop) HasRouteList() bool { return l.Fallbacks != "" || l.FollowsStandard() }

// AllowsNewConversation reports whether a standard new_conversation route may
// apply to this loop.
func (l Loop) AllowsNewConversation() bool { return contains(l.RoutesAllow, EnterNewConversation) }

// StandardRoutesPath is where the standard list lives: the path in
// KEEPGOING_ROUTES_FILE when set, otherwise keepgoing/routes.yaml in the user
// configuration directory.
func StandardRoutesPath() (string, error) {
	if p := os.Getenv(RoutesFileEnv); p != "" {
		if !filepath.IsAbs(p) {
			return "", errors.New(RoutesFileEnv + " must be an absolute path")
		}
		return filepath.Clean(p), nil
	}
	base, err := os.UserConfigDir()
	if err != nil || !filepath.IsAbs(base) {
		return "", errors.New("user configuration directory is unavailable")
	}
	return filepath.Join(base, "keepgoing", RoutesFileName), nil
}

// LoadRoutes reads and validates a standard route list.
func LoadRoutes(path string) (*Routes, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("standard route list must be a regular file of at most 1 MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := ParseRoutes(raw)
	if err != nil {
		return nil, err
	}
	r.source = snapshot(path, raw)
	return r, nil
}

// ParseRoutes decodes and validates standard route list YAML.
func ParseRoutes(raw []byte) (*Routes, error) {
	var r Routes
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	r.applyDefaults()
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Routes) policy() *Fallbacks {
	f := &Fallbacks{Version: 1, Redundancy: r.Redundancy, Probe: r.Probe}
	f.applyDefaults()
	return f
}

func (r *Routes) applyDefaults() {
	p := r.policy()
	r.Redundancy, r.Probe = p.Redundancy, p.Probe
}

var routePermissionModes = []string{"accept-edits", "auto", "full"}

// Validate reports every problem in the standard list.
func (r *Routes) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if r.Version != 1 {
		add("version must be 1")
	}
	if r.Redundancy.MinHealthy < 0 {
		add("redundancy.min_healthy must not be negative")
	}
	switch r.Redundancy.Span {
	case "provider", "group", "none":
	default:
		add("redundancy.span must be provider, group, or none")
	}
	if r.Probe.TTL.Duration < r.Probe.Every.Duration && !r.Probe.Disabled {
		add("probe.ttl must be at least probe.every, or scheduled probes can never keep a route healthy")
	}
	if r.Probe.DailyCap < 0 {
		add("probe.daily_cap must not be negative")
	}
	if len(r.Routes) > MaxStandardRoutes {
		add("routes: at most %d routes", MaxStandardRoutes)
	}
	seen := map[string]bool{}
	for i, rt := range r.Routes {
		where := fmt.Sprintf("routes[%d].", i)
		if !nameRE.MatchString(rt.ID) {
			add("%sid must be lowercase letters, digits, and hyphens", where)
		} else if rt.ID == "primary" {
			add("%sid \"primary\" is reserved", where)
		} else if seen[rt.ID] {
			add("%sid %q is repeated", where, rt.ID)
		}
		seen[rt.ID] = true
		if rt.Provider != "claude-code" && rt.Provider != "codex" {
			add("%sprovider must be claude-code or codex; other tools need a loop's own list", where)
		}
		if strings.TrimSpace(rt.Model) == "" || routeText(rt.Model) {
			add("%smodel is required and must be a plain name", where)
		}
		if routeText(rt.Effort) {
			add("%seffort must be a plain name", where)
		}
		if strings.TrimSpace(rt.Group) == "" || routeText(rt.Group) {
			add("%sgroup is required: the usage limit this route draws on", where)
		}
		switch rt.Enter {
		case EnterSameConversation, EnterNewConversation, EnterHeadless:
		default:
			add("%senter must be %s, %s or %s", where, EnterSameConversation, EnterNewConversation, EnterHeadless)
		}
		if rt.PermissionMode != "" && (rt.Enter != EnterNewConversation || !contains(routePermissionModes, rt.PermissionMode)) {
			add("%spermission_mode is accept-edits, auto or full, and applies only to a %s route", where, EnterNewConversation)
		}
		if rt.AccountDir != "" && (rt.Enter != EnterHeadless || !filepath.IsAbs(rt.AccountDir)) {
			add("%saccount_dir must be an absolute path and applies only to a %s route", where, EnterHeadless)
		}
		if rt.Probe.AccountDir != "" && !filepath.IsAbs(rt.Probe.AccountDir) {
			add("%sprobe.account_dir must be an absolute path", where)
		}
		if routeText(rt.Probe.Model) {
			add("%sprobe.model must be a plain name", where)
		}
	}
	return errors.Join(errs...)
}

// routeText reports a value that could not be a model, effort or group name:
// one with control characters or longer than 128 bytes.
func routeText(s string) bool {
	if len(s) > 128 {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Find returns the route with the given id.
func (r *Routes) Find(id string) (Route, bool) {
	if r == nil {
		return Route{}, false
	}
	for _, rt := range r.Routes {
		if rt.ID == id {
			return rt, true
		}
	}
	return Route{}, false
}

func accountEnv(provider string) string {
	if provider == "codex" {
		return "CODEX_HOME"
	}
	return "CLAUDE_CONFIG_DIR"
}

// Resolve turns the standard list into the fallbacks of one loop, in the
// list's order. A template that cannot apply to the loop is skipped with a
// reason; nothing is guessed. The result is nil when no route applies.
//
// For a loop that supervises a BB thread, a same_conversation route is the
// loop's own thread on the route's model and effort, and a new_conversation
// route starts a conversation at the handoff when the loop allows it. For a
// headless Claude Code or Codex loop, a headless route is that agent in the
// loop's working directory. Other tools are left to a loop's own list.
func (r *Routes) Resolve(l Loop) (*Fallbacks, []RouteSkip) {
	fb := &Fallbacks{Version: 1, Redundancy: r.Redundancy, Probe: r.Probe, source: r.source}
	var skipped []RouteSkip
	skip := func(rt Route, reason string) { skipped = append(skipped, RouteSkip{ID: rt.ID, Reason: reason}) }
	for _, rt := range r.Routes {
		if rt.Disabled {
			skip(rt, "switched off in the standard list")
			continue
		}
		if rt.Group == l.Agent.Group {
			skip(rt, "draws on the same usage limit as this loop's first route")
			continue
		}
		c := Candidate{ID: rt.ID}
		c.Agent = Agent{Provider: rt.Provider, Group: rt.Group}
		c.Probe = Probe{Model: rt.Probe.Model}
		if rt.Probe.AccountDir != "" {
			c.Probe.Env = map[string]string{accountEnv(rt.Provider): rt.Probe.AccountDir}
		}
		switch {
		case l.Adapter == "bb" && l.BB != nil:
			switch rt.Enter {
			case EnterHeadless:
				skip(rt, "a headless route; this loop supervises a conversation")
				continue
			case EnterSameConversation:
				if rt.Provider != l.Provider {
					skip(rt, "another provider than this loop's conversation; a conversation has one provider")
					continue
				}
				if strings.EqualFold(rt.Model, l.BB.Model) {
					skip(rt, "the model this loop's conversation already uses")
					continue
				}
				c.Adapter = "bb"
				c.BB = &BB{Thread: l.BB.Thread, CLI: l.BB.CLI, Model: rt.Model, Reasoning: rt.Effort}
			case EnterNewConversation:
				if !l.AllowsNewConversation() {
					skip(rt, "starts a new conversation, which this loop does not allow (loop.routes_allow)")
					continue
				}
				c.Adapter = "bb"
				c.BB = &BB{NewThread: true, CLI: l.BB.CLI, Model: rt.Model, Reasoning: rt.Effort, PermissionMode: rt.PermissionMode}
			}
		case l.Adapter == "claude" && l.Claude != nil, l.Adapter == "codex" && l.Codex != nil:
			if rt.Enter != EnterHeadless {
				skip(rt, "a conversation route; this loop supervises a headless agent")
				continue
			}
			workdir := ""
			if l.Claude != nil {
				workdir = l.Claude.Workdir
			} else {
				workdir = l.Codex.Workdir
			}
			env := map[string]string{}
			if rt.AccountDir != "" {
				env[accountEnv(rt.Provider)] = rt.AccountDir
			}
			if rt.Provider == "codex" {
				c.Adapter = "codex"
				c.Codex = &Codex{Workdir: workdir, Model: rt.Model, Env: env}
				if l.Codex != nil {
					// The same tool keeps the loop's own execution settings.
					c.Codex.CLI, c.Codex.Sandbox, c.Codex.SkipGitRepoCheck = l.Codex.CLI, l.Codex.Sandbox, l.Codex.SkipGitRepoCheck
					if rt.AccountDir == "" && strings.EqualFold(rt.Model, l.Codex.Model) {
						skip(rt, "the agent, model and account this loop already uses")
						continue
					}
				}
			} else {
				c.Adapter = "claude"
				c.Claude = &Claude{Workdir: workdir, Model: rt.Model, Effort: rt.Effort, Env: env}
				if l.Claude != nil {
					c.Claude.CLI, c.Claude.PermissionMode = l.Claude.CLI, l.Claude.PermissionMode
					if rt.AccountDir == "" && strings.EqualFold(rt.Model, l.Claude.Model) {
						skip(rt, "the agent, model and account this loop already uses")
						continue
					}
				}
			}
		default:
			skip(rt, "this loop's tool is outside the standard list; give the loop its own list")
			continue
		}
		fb.Candidates = append(fb.Candidates, c)
	}
	if len(fb.Candidates) == 0 {
		return nil, skipped
	}
	fb.applyDefaults()
	// A resolved route must be a valid fallback. One that is not is left out
	// with the reason instead of failing the loop.
	kept := fb.Candidates[:0]
	for _, c := range fb.Candidates {
		one := &Fallbacks{Version: 1, Redundancy: fb.Redundancy, Probe: fb.Probe, Candidates: []Candidate{c}}
		if err := one.Validate(); err != nil {
			skipped = append(skipped, RouteSkip{ID: c.ID, Reason: "does not fit this loop's configuration"})
			continue
		}
		kept = append(kept, c)
	}
	fb.Candidates = kept
	if len(fb.Candidates) == 0 {
		return nil, skipped
	}
	return fb, skipped
}

// loadStandardRoutes resolves the standard list for a loop that follows it.
// It never fails the load: an unusable list leaves the loop with no other
// routes and says so in the status.
func (c *Config) loadStandardRoutes() {
	c.RouteList = RouteStatus{Source: RoutesStandard}
	path, err := StandardRoutesPath()
	if err != nil {
		c.RouteList.Unreadable = true
		return
	}
	// Remember what was looked at, so a list that appears or changes later
	// makes the loaded configuration stale and is read again.
	c.routesSource = snapshotFile(path)
	r, err := LoadRoutes(path)
	if err != nil {
		c.RouteList.Unreadable = true
		return
	}
	c.routesSource = r.source
	fb, skipped := r.Resolve(c.Loop)
	c.RouteList.Skipped = skipped
	if fb == nil {
		return
	}
	if err := c.Loop.fitTickTimeout(fb.Probe.Timeout.Duration); err != nil {
		c.RouteList.Warnings = append(c.RouteList.Warnings, "loop.tick_timeout is shorter than the standard list's probe timeout allows, so scheduled checks of its routes cannot run")
	}
	c.FallbackSet = fb
}

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// RoutesStandard is the only value of loop.routes: the loop follows the
// person's standard route list instead of a fallbacks file of its own.
const RoutesStandard = "standard"

// RoutesFileName is the standard route list, kept once per user outside every
// loop's directory.
const RoutesFileName = "routes.yaml"

// RoutesFileEnv names an absolute path that replaces the default location.
// The variable's earlier name, KEEPGOING_ROUTES_FILE, is still read. It
// exists for tests and for people who keep configuration elsewhere.
const RoutesFileEnv = envvar.Prefix + string(envvar.RoutesFile)

// maxRoutePath bounds a path in a route, so the saved record of the route a
// loop is on always fits its limit.
const maxRoutePath = 4096

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
	// Problem is the fixed reason (see RoutesProblem), or "missing".
	Problem string
	// Invalid holds the validation errors of a list that was read but is not
	// valid, for validate to show. It names no path.
	Invalid string
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
// LOOPTENDER_ROUTES_FILE (or its earlier name, KEEPGOING_ROUTES_FILE) when
// set, otherwise routes.yaml in the tool's directory under the user
// configuration directory (looptender/, or keepgoing/ on a computer that
// already has one; see userdir).
// The two names set to different files are refused: loops that each honoured
// one would follow different lists.
func StandardRoutesPath() (string, error) {
	p, name, err := envvar.RoutesFile.Path()
	if err != nil {
		return "", routesRefusal(RoutesNamedTwice, err.Error())
	}
	if p != "" {
		if !filepath.IsAbs(p) {
			return "", errors.New(name + " must be an absolute path")
		}
		p = filepath.Clean(p)
		if insideCheckout(p) {
			return "", routesRefusal(RoutesInsideCheckout, name+" must not be inside a Git checkout: the list names private account directories")
		}
		return p, nil
	}
	p, err = userdir.Config(RoutesFileName)
	if err != nil {
		return "", errors.New("user configuration directory is unavailable")
	}
	return p, nil
}

// insideCheckout reports whether path or one of its parents holds a .git
// entry. A parent that cannot be inspected counts as a checkout.
func insideCheckout(path string) bool {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
		if parent := filepath.Dir(p); parent == p {
			return false
		}
	}
}

// LoadRoutes reads and validates a standard route list.
func LoadRoutes(path string) (*Routes, error) {
	raw, err := readPrivateFile(path)
	if err != nil {
		return nil, err
	}
	r, err := ParseRoutes(raw)
	if err != nil {
		return nil, &RoutesFileError{Code: RoutesInvalid, msg: err.Error()}
	}
	r.source = snapshot(path, raw)
	return r, nil
}

// Fixed reasons a standard list cannot be used. They name no path.
const (
	RoutesNotRegular     = "not_regular"
	RoutesTooLarge       = "too_large"
	RoutesNotOwned       = "not_owned"
	RoutesNotPrivate     = "not_private"
	RoutesInsideCheckout = "inside_checkout"
	RoutesNamedTwice     = "named_twice" // the variable's two names disagree
	RoutesUnreadableFile = "unreadable"
	RoutesInvalid        = "invalid"
)

// RoutesFileError is a refusal of the standard list with its fixed reason.
type RoutesFileError struct {
	Code string
	msg  string
}

func (e *RoutesFileError) Error() string { return e.msg }

func routesRefusal(code, msg string) error { return &RoutesFileError{Code: code, msg: msg} }

// RoutesProblem is the fixed reason for an error from StandardRoutesPath or
// LoadRoutes: one of the Routes* codes, or "" for a missing file.
func RoutesProblem(err error) string {
	var e *RoutesFileError
	switch {
	case err == nil, errors.Is(err, os.ErrNotExist):
		return ""
	case errors.As(err, &e):
		return e.Code
	}
	return RoutesUnreadableFile
}

// readPrivateFile reads the standard list the way the loop registry is read:
// a regular file of at most 1 MiB, owned by this user and not readable or
// writable by anyone else. A symbolic link is followed when this user owns
// it, as dotfile managers create them, wherever the file it names is; that
// file must pass the same checks. The type is checked before opening, and the
// open does not block, so a FIFO or device at the path cannot hang a check.
// The checks are repeated on the opened file, which is the one read.
func readPrivateFile(path string) ([]byte, error) {
	link, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if link.Mode()&os.ModeSymlink != 0 && !ownedByUser(link) {
		return nil, routesRefusal(RoutesNotOwned, "standard route list is a symbolic link owned by another user")
	}
	target, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := checkRoutesFile(target); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, routesRefusal(RoutesUnreadableFile, "standard route list could not be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, routesRefusal(RoutesUnreadableFile, "standard route list could not be inspected")
	}
	if err := checkRoutesFile(info); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, routesRefusal(RoutesUnreadableFile, "standard route list could not be read")
	}
	if len(raw) > 1<<20 {
		return nil, routesRefusal(RoutesTooLarge, "standard route list must be at most 1 MiB")
	}
	return raw, nil
}

func checkRoutesFile(info os.FileInfo) error {
	switch {
	case !info.Mode().IsRegular():
		return routesRefusal(RoutesNotRegular, "standard route list must be a regular file")
	case info.Size() > 1<<20:
		return routesRefusal(RoutesTooLarge, "standard route list must be at most 1 MiB")
	case !ownedByUser(info):
		return routesRefusal(RoutesNotOwned, "standard route list must be owned by this user")
	case !privateMode(info):
		return routesRefusal(RoutesNotPrivate, "standard route list must not be readable or writable by group or others (chmod 600)")
	}
	return nil
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
		if len(rt.AccountDir) > maxRoutePath || len(rt.Probe.AccountDir) > maxRoutePath {
			add("%saccount_dir and probe.account_dir are at most %d bytes", where, maxRoutePath)
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
	for _, rt := range r.Routes {
		c, reason := resolveRoute(l, rt, fb.Redundancy, fb.Probe)
		if reason != "" {
			skipped = append(skipped, RouteSkip{ID: rt.ID, Reason: reason})
			continue
		}
		fb.Candidates = append(fb.Candidates, c)
	}
	if len(fb.Candidates) == 0 {
		return nil, skipped
	}
	fb.applyDefaults()
	return fb, skipped
}

// resolveRoute is one template resolved for one loop, with the fallback
// defaults applied, or the reason it does not apply.
//
// For a headless route on the loop's own tool, the loop's execution settings
// carry over: executable, permission mode or sandbox, extra arguments,
// environment and budget. On the other tool only what means the same thing
// to both carries over: the working directory and the environment. The
// loop's extra arguments are specific to its tool and never carry over, and
// a route that would drop a budget the loop sets is left out.
func resolveRoute(l Loop, rt Route, red Redundancy, probe ProbePolicy) (Candidate, string) {
	if rt.Disabled {
		return Candidate{}, "switched off in the standard list"
	}
	if rt.Group == l.Agent.Group {
		return Candidate{}, "draws on the same usage limit as this loop's first route"
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
			return Candidate{}, "a headless route; this loop supervises a conversation"
		case EnterSameConversation:
			if rt.Provider != l.Provider {
				return Candidate{}, "another provider than this loop's conversation; a conversation has one provider"
			}
			if strings.EqualFold(rt.Model, l.BB.Model) {
				return Candidate{}, "the model this loop's conversation already uses"
			}
			c.Adapter = "bb"
			c.BB = &BB{Thread: l.BB.Thread, CLI: l.BB.CLI, Model: rt.Model, Reasoning: rt.Effort}
		case EnterNewConversation:
			if !l.AllowsNewConversation() {
				return Candidate{}, "starts a new conversation, which this loop does not allow (loop.routes_allow)"
			}
			c.Adapter = "bb"
			c.BB = &BB{NewThread: true, CLI: l.BB.CLI, Model: rt.Model, Reasoning: rt.Effort, PermissionMode: rt.PermissionMode}
		}
	case l.Adapter == "claude" && l.Claude != nil, l.Adapter == "codex" && l.Codex != nil:
		if rt.Enter != EnterHeadless {
			return Candidate{}, "a conversation route; this loop supervises a headless agent"
		}
		var workdir string
		var loopEnv map[string]string
		if l.Claude != nil {
			workdir, loopEnv = l.Claude.Workdir, l.Claude.Env
		} else {
			workdir, loopEnv = l.Codex.Workdir, l.Codex.Env
		}
		env := map[string]string{}
		for k, v := range loopEnv {
			env[k] = v
		}
		if rt.AccountDir != "" {
			env[accountEnv(rt.Provider)] = rt.AccountDir
		}
		if len(env) == 0 {
			env = nil
		}
		if rt.Provider == "codex" {
			if rt.Effort != "" {
				return Candidate{}, "sets an effort, and a headless Codex route has no effort setting; leave effort out of this route"
			}
			if l.Claude != nil && l.Claude.MaxBudgetUSD > 0 {
				return Candidate{}, "this loop sets claude.max_budget_usd, which a headless Codex agent cannot keep"
			}
			c.Adapter = "codex"
			c.Codex = &Codex{Workdir: workdir, Model: rt.Model, Env: env}
			if l.Codex != nil {
				c.Codex.CLI, c.Codex.Sandbox, c.Codex.SkipGitRepoCheck = l.Codex.CLI, l.Codex.Sandbox, l.Codex.SkipGitRepoCheck
				c.Codex.ExtraArgs = append([]string(nil), l.Codex.ExtraArgs...)
				if rt.AccountDir == "" && strings.EqualFold(rt.Model, l.Codex.Model) {
					return Candidate{}, "the agent, model and account this loop already uses"
				}
			}
		} else {
			c.Adapter = "claude"
			c.Claude = &Claude{Workdir: workdir, Model: rt.Model, Effort: rt.Effort, Env: env}
			if l.Claude != nil {
				c.Claude.CLI, c.Claude.PermissionMode, c.Claude.MaxBudgetUSD = l.Claude.CLI, l.Claude.PermissionMode, l.Claude.MaxBudgetUSD
				c.Claude.ExtraArgs = append([]string(nil), l.Claude.ExtraArgs...)
				if rt.AccountDir == "" && strings.EqualFold(rt.Model, l.Claude.Model) {
					return Candidate{}, "the agent, model and account this loop already uses"
				}
			}
		}
	default:
		return Candidate{}, "this loop's tool is outside the standard list; give the loop its own list"
	}
	one := &Fallbacks{Version: 1, Redundancy: red, Probe: probe, Candidates: []Candidate{c}}
	one.applyDefaults()
	// A resolved route must be a valid fallback; one that is not is left out
	// with the reason instead of failing the loop.
	if err := one.Validate(); err != nil {
		return Candidate{}, "does not fit this loop's configuration"
	}
	return one.Candidates[0], ""
}

// RouteOf is the template a resolved candidate came from, read back from the
// candidate and the loop: what the record of a route a loop is on holds. It
// carries no environment or executable of the loop's own.
func RouteOf(l Loop, c Candidate) (Route, bool) {
	rt := Route{ID: c.ID, Provider: c.Provider, Group: c.Group, Probe: RouteProbe{Model: c.Probe.Model}}
	if len(c.Probe.Env) > 1 {
		return Route{}, false
	}
	for k, v := range c.Probe.Env {
		if k != accountEnv(c.Provider) {
			return Route{}, false
		}
		rt.Probe.AccountDir = v
	}
	var loopEnv map[string]string
	switch {
	case l.Claude != nil:
		loopEnv = l.Claude.Env
	case l.Codex != nil:
		loopEnv = l.Codex.Env
	}
	account := func(env map[string]string) string {
		if v := env[accountEnv(c.Provider)]; v != loopEnv[accountEnv(c.Provider)] {
			return v
		}
		return ""
	}
	switch {
	case c.BB != nil && c.BB.NewThread:
		rt.Enter, rt.Model, rt.Effort, rt.PermissionMode = EnterNewConversation, c.BB.Model, c.BB.Reasoning, c.BB.PermissionMode
	case c.BB != nil:
		rt.Enter, rt.Model, rt.Effort = EnterSameConversation, c.BB.Model, c.BB.Reasoning
	case c.Claude != nil:
		rt.Enter, rt.Model, rt.Effort, rt.AccountDir = EnterHeadless, c.Claude.Model, c.Claude.Effort, account(c.Claude.Env)
	case c.Codex != nil:
		rt.Enter, rt.Model, rt.AccountDir = EnterHeadless, c.Codex.Model, account(c.Codex.Env)
	default:
		return Route{}, false
	}
	return rt, true
}

// ResolveSaved resolves the saved template of the route a loop is on, for the
// loop as it is configured now. The template must be a valid route. It is
// resolved as if it were switched on, and a new-conversation route as if the
// loop allowed one, because the loop is already on it; every other reason a
// route does not apply still refuses it.
func ResolveSaved(l Loop, rt Route, policy *Fallbacks) (Candidate, bool) {
	rt.Disabled = false
	one := &Routes{Version: 1, Routes: []Route{rt}}
	one.applyDefaults()
	if one.Validate() != nil {
		return Candidate{}, false
	}
	if rt.Enter == EnterNewConversation && !l.AllowsNewConversation() {
		l.RoutesAllow = append(append([]string(nil), l.RoutesAllow...), EnterNewConversation)
	}
	red, probe := one.Redundancy, one.Probe
	if policy != nil {
		red, probe = policy.Redundancy, policy.Probe
	}
	c, reason := resolveRoute(l, rt, red, probe)
	return c, reason == ""
}

// loadStandardRoutes resolves the standard list for a loop that follows it.
// It never fails the load: an unusable list leaves the loop with no other
// routes and says so in the status.
func (c *Config) loadStandardRoutes() {
	c.RouteList = RouteStatus{Source: RoutesStandard}
	path, err := StandardRoutesPath()
	if err != nil {
		c.RouteList.Unreadable, c.RouteList.Problem = true, RoutesProblem(err)
		return
	}
	// Remember what was looked at, so a list that appears or changes later
	// makes the loaded configuration stale and is read again.
	c.routesSource = snapshotFile(path)
	r, err := LoadRoutes(path)
	if err != nil {
		c.RouteList.Unreadable, c.RouteList.Problem = true, RoutesProblem(err)
		if c.RouteList.Problem == "" {
			c.RouteList.Problem = "missing"
		}
		if c.RouteList.Problem == RoutesInvalid {
			c.RouteList.Invalid = err.Error()
		}
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

// WithCurrentRoute returns the fallbacks a loop uses while it is on a route
// that is no longer in its list: the list as it is now (or none) plus that one
// route, so the loop can still observe, stop and finish on it. The route is
// the active target, which is never a handoff destination.
func WithCurrentRoute(list *Fallbacks, current Candidate) *Fallbacks {
	out := &Fallbacks{Version: 1}
	if list != nil {
		out.Redundancy, out.Probe, out.source = list.Redundancy, list.Probe, list.source
		for _, c := range list.Candidates {
			// A switched-off entry with the same id gives way to the saved route.
			if c.ID != current.ID {
				out.Candidates = append(out.Candidates, c)
			}
		}
	}
	out.Candidates = append(out.Candidates, current)
	out.applyDefaults()
	return out
}

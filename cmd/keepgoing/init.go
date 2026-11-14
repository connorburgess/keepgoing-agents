package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
	"gopkg.in/yaml.v3"
)

type initOptions struct {
	noProgressTimeout                              time.Duration
	maxSends                                       int64
	name, project, directory, task, taskFile       string
	deadline                                       string
	agent, thread, provider, account, group, model string
	routes                                         string // standard or own; "" is own
	fallbacks                                      []initFallback
	stall, wake, backoff, interval                 time.Duration
	nudges, reprompts, probeCap                    int
	nonInteractive, dryRun, withGoal, allowShare   bool
}

type initFallback struct {
	kind, group, account string
}

func initLoop(e env, args []string) int {
	return setupLoop(e, args, os.Stdin, exec.LookPath)
}

// setupLoop discovers executables, but never invokes an agent or reads its login.
func setupLoop(e env, args []string, input io.Reader, lookPath func(string) (string, error)) int {
	var o initOptions
	var asJSON bool
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.StringVar(&o.name, "name", "", "loop name: lowercase letters, digits, hyphens")
	fs.StringVar(&o.project, "project", "", "existing Git project with a commit (default: current directory)")
	fs.StringVar(&o.directory, "directory", "", "new private setup directory outside any checkout")
	fs.StringVar(&o.task, "task", "", "task text (use --task-file to keep it out of shell history)")
	fs.BoolVar(&o.withGoal, "with-goal", false, "record a goal referencing the private task snapshot and share its current revision in future prompts")
	fs.StringVar(&o.taskFile, "task-file", "", "import task text once; - reads up to 1 MiB from stdin with --non-interactive")
	fs.DurationVar(&o.noProgressTimeout, "no-progress-timeout", 0, "hold after this time without verified Git changes; zero disables")
	fs.Int64Var(&o.maxSends, "max-sends", 0, "total automatic send attempts while this limit is enabled; zero disables")
	fs.StringVar(&o.deadline, "deadline", "", "RFC3339 cutoff for future supervision; does not cancel running turns")
	fs.StringVar(&o.agent, "agent", "", "bb, claude, codex, or cursor; detects installed CLIs")
	fs.StringVar(&o.thread, "thread", "", "existing BB thread ID; required for bb")
	fs.StringVar(&o.provider, "provider", "", "existing BB thread provider label; required for bb")
	fs.StringVar(&o.account, "account-dir", "", "existing Claude Code or Codex account configuration directory")
	fs.StringVar(&o.group, "group", "", "primary quota pool label (default: provider)")
	fs.StringVar(&o.model, "model", "", "headless model (default: CLI configuration)")
	fs.StringVar(&o.routes, "routes", "", "standard: follow your standard route list for handoffs; own (the default without asking): only --fallback")
	fs.Func("fallback", "ordered headless fallback: ADAPTER[:GROUP][=ACCOUNT_DIR]; repeatable", func(s string) error {
		kindGroup, account, _ := strings.Cut(s, "=")
		kind, group, _ := strings.Cut(kindGroup, ":")
		o.fallbacks = append(o.fallbacks, initFallback{kind, group, account})
		return nil
	})
	fs.DurationVar(&o.stall, "stall-after", config.DefaultStallAfter, "time without Git progress before nudging")
	fs.DurationVar(&o.wake, "wake-every", config.DefaultMinWakeInterval, "minimum time since last send before a stall nudge; idle wakes follow check interval")
	fs.DurationVar(&o.backoff, "backoff", config.DefaultBackoff, "delay between error re-prompts")
	fs.DurationVar(&o.interval, "interval", config.DefaultDaemonInterval, "daemon tick interval")
	fs.IntVar(&o.nudges, "max-nudges", config.DefaultMaxNudges, "stall nudges before awaiting a person")
	fs.IntVar(&o.reprompts, "max-reprompts", config.DefaultMaxReprompts, "error re-prompts before awaiting a person")
	fs.IntVar(&o.probeCap, "probe-daily-cap", 2, "scheduled probes per fallback per UTC day; exact/manual probes are additional")
	fs.BoolVar(&o.nonInteractive, "non-interactive", false, "require missing inputs as flags; do not prompt")
	fs.BoolVar(&o.dryRun, "dry-run", false, "preview the resolved setup without writing files")
	fs.BoolVar(&o.allowShare, "allow-second-loop", false, "set up this loop even though another enabled loop supervises the same session")
	fs.BoolVar(&asJSON, "json", false, "print a versioned setup result; requires --non-interactive")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 {
		e.errf("init: unexpected positional arguments")
		return exitUsage
	}
	if (asJSON || o.taskFile == "-") && !o.nonInteractive {
		e.errf("init: --json and stdin task input require --non-interactive")
		return exitUsage
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if err := collectInit(e, &o, input, lookPath, visited); err != nil {
		e.errf("init: %v", err)
		return exitUsage
	}
	if o.taskFile == "-" {
		task, err := readInitTask(e.ctx, input)
		if err != nil {
			e.errf("init: %v", err)
			return exitUsage
		}
		o.task = task
	}
	files, cfg, err := planInit(&o, lookPath)
	if err != nil {
		e.errf("init: %v", err)
		return exitUsage
	}
	// Setting up a loop on a session another enabled loop supervises is
	// refused here, in the preview too, rather than at the first start.
	others := findOtherLoops(cfg)
	if msg := refuseSecondLoop(cfg, others, "init"); msg != "" && !o.allowShare {
		e.errf("%s", msg)
		if asJSON {
			printSecondLoopRefusal(e, secondLoopConflict(cfg, others))
		}
		return exitFailed
	}
	for _, w := range sharedLoopWarnings(cfg, others) {
		e.errf("warning: %s", w)
	}
	var goal *control.Goal
	if asJSON {
		if err := e.ctx.Err(); err != nil {
			e.errf("init: %v", err)
			return exitFailed
		}
		if !o.dryRun {
			if goal, err = writeInit(o.directory, files, setupGoalSpec(o)); err != nil {
				e.errf("init: %v", err)
				return exitFailed
			}
		}
		return printInitJSON(e, o, cfg, goal)
	}
	fmt.Fprintf(e.stdout, "Loop: %s\nTarget: %s\nProvider: %s; quota group: %s\nProject: %s\nConfiguration: %s\nState: %s\n", o.name, cfg.Loop.Agent.Describe(), cfg.Loop.Provider, cfg.Loop.Group, o.project, filepath.Join(o.directory, "keepgoing.yaml"), cfg.Loop.StateDir)
	fmt.Fprintln(e.stdout, initBindingNotice(o))
	if o.agent == "cursor" {
		fmt.Fprintln(e.stdout, initCursorNotice)
	}
	printInitAccount(e.stdout, cfg.Loop.Agent)
	fmt.Fprintf(e.stdout, "Limits: stall nudge spacing >= %s; stall %s; %d nudges; %d error re-prompts with %s backoff; check every %s\n", o.wake, o.stall, o.nudges, o.reprompts, o.backoff, o.interval)
	if o.noProgressTimeout > 0 {
		fmt.Fprintf(e.stdout, "No-progress timeout: %s without verified Git changes (includes downtime; resume does not reset)\n", o.noProgressTimeout)
	}
	if o.maxSends > 0 {
		fmt.Fprintf(e.stdout, "Send limit: %d total attempts (failed attempts count; resume does not reset)\n", o.maxSends)
	}
	if o.deadline != "" {
		fmt.Fprintf(e.stdout, "Deadline: %s (next check holds supervision; running turns are not cancelled)\n", o.deadline)
	}
	if cfg.Loop.FollowsStandard() {
		applies := candidateIDs(cfg.FallbackSet)
		fmt.Fprintf(e.stdout, "Routes: follows your standard route list; %d apply to this loop: %s. Changes to the list reach the loop at its next check.\n", len(applies), idList(applies))
		for _, s := range cfg.RouteList.Skipped {
			fmt.Fprintf(e.stdout, "  not used here: %s (%s)\n", s.ID, s.Reason)
		}
		fmt.Fprintln(e.stdout, "After enable: rate-limit handoffs only, exact probe required; probes follow the standard list's policy. Switch-back is off.")
	} else if cfg.FallbackSet == nil {
		fmt.Fprintln(e.stdout, "Fallbacks: none; automatic handoffs disabled.")
	} else {
		for i, c := range cfg.FallbackSet.Candidates {
			fmt.Fprintf(e.stdout, "Fallback %d: %s; quota group: %s\n", i+1, c.Agent.Describe(), c.Group)
			printInitAccount(e.stdout, c.Agent)
		}
		fmt.Fprintf(e.stdout, "After enable: rate-limit handoffs only, exact probe required; scheduled probes every 12h, cap %d per fallback per UTC day. Exact/manual probes are additional model calls. Switch-back is off.\n", o.probeCap)
	}
	fmt.Fprintln(e.stdout, "Setup leaves supervision disabled. No agents, probes, logins, or schedulers are started. Existing provider permissions are inherited; account identity and access have not been verified.")
	if o.dryRun {
		fmt.Fprintln(e.stdout, "Dry run: no files written.")
		return exitOK
	}
	if err := e.ctx.Err(); err != nil {
		e.errf("init: %v", err)
		return exitFailed
	}
	if goal, err = writeInit(o.directory, files, setupGoalSpec(o)); err != nil {
		e.errf("init: %v", err)
		return exitFailed
	}
	cfgPath := filepath.Join(o.directory, "keepgoing.yaml")
	if goal != nil {
		printGoal(e, goal.Summary(), false)
	}
	fmt.Fprintln(e.stdout, "Created private setup. Review the task and configuration, then run diagnostics:")
	fmt.Fprintf(e.stdout, "  %s doctor -c %s\n", initCommandPrefix(), commandQuote(cfgPath))
	fmt.Fprintln(e.stdout, "Then explicitly start supervision:")
	fmt.Fprintf(e.stdout, "  %s start --foreground -c %s\n", initCommandPrefix(), commandQuote(cfgPath))
	fmt.Fprintln(e.stdout, "Or choose --background to keep the daemon running after this terminal closes. Start validates local setup and enables the loop; existing holds remain. No service is installed.")
	if svc := serviceInstallCommand(cfgPath); svc != nil {
		fmt.Fprintln(e.stdout, "Or, instead of start, keep checks running across restarts with a service the operating system restarts, the recommended way (see Services in the keepgoing documentation). A service does not enable the loop, so enable it first:")
		fmt.Fprintf(e.stdout, "  %s enable -c %s\n", initCommandPrefix(), commandQuote(cfgPath))
		fmt.Fprintf(e.stdout, "  %s service install -c %s\n", initCommandPrefix(), commandQuote(cfgPath))
	}
	return exitOK
}

func collectInit(e env, o *initOptions, input io.Reader, lookPath func(string) (string, error), visited map[string]bool) error {
	if o.task != "" && o.taskFile != "" {
		return errors.New("choose --task or --task-file, not both")
	}
	if o.nonInteractive {
		if o.name == "" || o.agent == "" || (o.task == "" && o.taskFile == "") {
			return errors.New("--non-interactive requires --name, --agent, and --task or --task-file")
		}
		return nil
	}
	r := bufio.NewReader(input)
	ask := func(label, value string) (string, error) {
		if err := e.ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprintf(e.stdout, "%s [%s]: ", label, value)
		s, err := readInitLine(e.ctx, r)
		if err != nil {
			return "", fmt.Errorf("input ended; no setup written (use --non-interactive for scripts): %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			s = value
		}
		return s, nil
	}
	str := func(flag, label string, value *string, def string) error {
		if visited[flag] {
			return nil
		}
		s, err := ask(label, def)
		*value = s
		return err
	}
	var installed []string
	for _, a := range []string{"codex", "claude", "bb", "cursor"} {
		if _, err := lookPath(initCLIName(a)); err == nil {
			installed = append(installed, a)
		}
	}
	fmt.Fprintf(e.stdout, "Detected CLIs: %s\n", strings.Join(installed, ", "))
	if len(installed) == 0 {
		return errors.New("no supported CLI found on PATH (codex, claude, bb, or Cursor CLI agent)")
	}
	if err := str("name", "Loop name", &o.name, "my-loop"); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := str("project", "Project directory", &o.project, cwd); err != nil {
		return err
	}
	if o.task == "" && o.taskFile == "" {
		if o.taskFile, err = ask("Task file (blank to enter a task)", ""); err != nil {
			return err
		}
		if o.taskFile == "" {
			if o.task, err = ask("Task and completion criteria", ""); err != nil {
				return err
			}
		}
	}
	if !visited["with-goal"] {
		choice, err := ask("Record a goal and share its current revision with the agent? (yes/no)", "yes")
		if err != nil {
			return err
		}
		switch strings.ToLower(choice) {
		case "yes", "y":
			o.withGoal = true
		case "no", "n":
			o.withGoal = false
		default:
			return errors.New("goal choice must be yes or no")
		}
	}
	if err := str("agent", "Agent", &o.agent, installed[0]); err != nil {
		return err
	}
	if o.agent == "bb" {
		if err := str("thread", "Existing BB thread ID", &o.thread, ""); err != nil {
			return err
		}
		if err := str("provider", "Existing thread provider label", &o.provider, ""); err != nil {
			return err
		}
	} else {
		if o.agent != "cursor" {
			if err := str("account-dir", "Existing account directory (blank inherits current login)", &o.account, ""); err != nil {
				return err
			}
		} else {
			fmt.Fprintln(e.stdout, "Cursor uses the agent CLI and inherits its existing login. Separate account directories and fallbacks are unsupported.")
		}
		if err := str("model", "Model (blank inherits CLI configuration)", &o.model, ""); err != nil {
			return err
		}
	}
	provider := o.provider
	if o.agent == "claude" {
		provider = "claude-code"
	} else if o.agent == "codex" || o.agent == "cursor" {
		provider = o.agent
	}
	if err := str("group", "Primary quota pool label", &o.group, provider); err != nil {
		return err
	}
	// The offer to follow the standard list: yes unless declined, and only
	// when asked. A script follows it only with --routes standard.
	if !visited["routes"] && !visited["fallback"] && initCanFollow(o.agent) {
		if applies, total := initStandardOffer(o, lookPath); applies > 0 {
			verb := "apply"
			if applies == 1 {
				verb = "applies"
			}
			choice, err := ask(fmt.Sprintf("Follow your standard route list for handoffs? %d of its %d routes %s to this loop. (yes/no)", applies, total, verb), "yes")
			if err != nil {
				return err
			}
			switch strings.ToLower(choice) {
			case "yes", "y":
				o.routes = config.RoutesStandard
			case "no", "n":
				o.routes = "own"
			default:
				return errors.New("route list choice must be yes or no")
			}
		}
	}
	if !visited["fallback"] && o.agent != "cursor" && o.routes != config.RoutesStandard {
		fmt.Fprintln(e.stdout, "Optional fallbacks use existing logins. Use the same quota label for accounts sharing limits. Blank keeps a single agent.")
		for {
			a, err := ask("Next fallback agent (claude/codex, blank to finish)", "")
			if err != nil {
				return err
			}
			if a == "" {
				break
			}
			account, err := ask("Existing fallback account directory (blank inherits current login)", "")
			if err != nil {
				return err
			}
			pool := a
			if a == "claude" {
				pool = "claude-code"
			}
			group, err := ask("Fallback quota pool label", pool)
			if err != nil {
				return err
			}
			o.fallbacks = append(o.fallbacks, initFallback{a, group, account})
		}
	}
	for _, q := range []struct {
		flag, label string
		value       *time.Duration
	}{
		{"wake-every", "Minimum time since last send before a stall nudge", &o.wake}, {"stall-after", "Stall time before nudging", &o.stall}, {"backoff", "Error re-prompt delay", &o.backoff}, {"interval", "Check interval", &o.interval},
	} {
		if visited[q.flag] {
			continue
		}
		s, err := ask(q.label, q.value.String())
		if err != nil {
			return err
		}
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("%s: %w", q.flag, err)
		}
		*q.value = v
	}
	for _, q := range []struct {
		flag, label string
		value       *int
	}{
		{"max-nudges", "Maximum stall nudges", &o.nudges}, {"max-reprompts", "Maximum error re-prompts", &o.reprompts}, {"probe-daily-cap", "Scheduled probes per fallback per UTC day", &o.probeCap},
	} {
		if visited[q.flag] || (q.flag == "probe-daily-cap" && len(o.fallbacks) == 0) {
			continue
		}
		s, err := ask(q.label, fmt.Sprint(*q.value))
		if err != nil {
			return err
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("%s: expected an integer", q.flag)
		}
		*q.value = v
	}
	return str("directory", "New private setup directory (blank uses user configuration directory)", &o.directory, "")
}

func planInit(o *initOptions, lookPath func(string) (string, error)) (map[string][]byte, *config.Config, error) {
	fail := func(err error) (map[string][]byte, *config.Config, error) { return nil, nil, err }
	var err error
	if o.agent == "cursor" && len(o.fallbacks) > 0 {
		return fail(errors.New("cursor does not support --fallback; configure a primary-only loop"))
	}
	if o.project == "" {
		o.project, err = os.Getwd()
		if err != nil {
			return fail(err)
		}
	}
	o.project, err = existingDirectory(o.project)
	if err != nil {
		return fail(fmt.Errorf("project: %w", err))
	}
	if _, err := state.Fingerprint([]string{o.project}); err != nil {
		return fail(fmt.Errorf("project needs readable Git worktree metadata and an initial commit: %w", err))
	}
	if o.taskFile != "" && o.taskFile != "-" {
		var b []byte
		var err error
		if o.withGoal {
			b, err = readSetupGoalTask(o.taskFile)
		} else {
			b, err = os.ReadFile(o.taskFile)
		}
		if err != nil {
			return fail(err)
		}
		o.task = string(b)
	}
	o.task = strings.TrimSpace(o.task)
	if o.task == "" {
		return fail(errors.New("task must not be empty"))
	}
	// Validate names before using them as path components.
	if o.name == "" || len(o.name) > 64 || strings.Trim(o.name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || o.name[0] == '-' {
		return fail(errors.New("name must be 1-64 lowercase letters, digits, or hyphens, starting with a letter or digit"))
	}
	if o.directory == "" {
		o.directory, err = userdir.Config("loops", o.name)
		if err != nil {
			return fail(err)
		}
	}
	o.directory, err = canonicalFuturePath(o.directory)
	if err != nil {
		return fail(err)
	}
	if _, err := os.Lstat(o.directory); !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("setup directory must be new: %s", o.directory))
	}
	if pathWithin(o.project, o.directory) {
		return fail(errors.New("setup directory must be outside the project"))
	}
	for p := filepath.Dir(o.directory); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return fail(errors.New("setup directory must be outside every Git checkout"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if o.stall < time.Minute || o.wake < time.Minute || o.interval < 10*time.Second || o.backoff <= 0 {
		return fail(errors.New("stall-after and wake-every must be at least 1m, interval at least 10s, and backoff positive"))
	}
	if o.nudges < 1 || o.reprompts < 1 || o.probeCap < 1 {
		return fail(errors.New("max-nudges, max-reprompts, and probe-daily-cap must be positive"))
	}
	primary, err := initAgent(o.agent, o.account, o.group, o.model, o.project, lookPath)
	if err != nil {
		return fail(err)
	}
	if o.agent == "bb" {
		o.thread, o.provider = strings.TrimSpace(o.thread), strings.TrimSpace(o.provider)
		if o.thread == "" || o.provider == "" {
			return fail(errors.New("bb requires --thread and --provider; init does not create or move threads"))
		}
		if o.model != "" || o.account != "" {
			return fail(errors.New("bb uses the existing thread's model and login; --model and --account-dir are headless-only"))
		}
		primary.BB.Thread, primary.Provider = o.thread, o.provider
		if primary.Group == "" {
			primary.Group = o.provider
		}
	} else if o.thread != "" || o.provider != "" {
		return fail(errors.New("--thread and --provider are only used with bb"))
	}
	var standard *config.Routes
	switch o.routes {
	case "", "own":
	case config.RoutesStandard:
		if len(o.fallbacks) > 0 {
			return fail(errors.New("--routes standard and --fallback exclude each other: a loop follows the standard list or has its own"))
		}
		if !initCanFollow(o.agent) {
			return fail(errors.New("--routes standard covers bb, claude and codex loops"))
		}
		path, err := config.StandardRoutesPath()
		if err == nil {
			standard, err = config.LoadRoutes(path)
		}
		if err != nil {
			code := config.RoutesProblem(err)
			if code == "" {
				return fail(errors.New("--routes standard: no standard route list has been written yet; add a route first with keepgoing routes add"))
			}
			return fail(fmt.Errorf("--routes standard: the standard route list cannot be used (%s): %s", code, routesProblemText(code)))
		}
		if len(standard.Routes) == 0 {
			return fail(errors.New("--routes standard: the standard route list has no routes yet; add one first with keepgoing routes add"))
		}
	default:
		return fail(errors.New("--routes is standard or own"))
	}
	fallbacks := config.Fallbacks{Version: 1, Redundancy: config.Redundancy{MinHealthy: 1, Span: "group"}, Probe: config.ProbePolicy{Every: config.Duration{Duration: 12 * time.Hour}, TTL: config.Duration{Duration: 24 * time.Hour}, DailyCap: o.probeCap}}
	groups := map[string]bool{primary.Group: true}
	for i, spec := range o.fallbacks {
		if spec.kind != "claude" && spec.kind != "codex" {
			return fail(errors.New("fallback must be claude or codex, optionally :GROUP=ACCOUNT_DIR"))
		}
		a, err := initAgent(spec.kind, spec.account, spec.group, "", o.project, lookPath)
		if err != nil {
			return fail(err)
		}
		if groups[a.Group] {
			return fail(fmt.Errorf("fallback %d shares quota group %q with an earlier target; choose an independent account and label, or omit it", i+1, a.Group))
		}
		groups[a.Group] = true
		fallbacks.Candidates = append(fallbacks.Candidates, config.Candidate{ID: fmt.Sprintf("fallback-%d", i+1), Agent: a, Probe: config.Probe{Kind: spec.kind}})
	}
	// Keep the output small and explicit, especially failover_on: [] (the
	// config struct's omitempty would otherwise restore the default on reload).
	loop := map[string]any{"name": o.name, "state_dir": filepath.Join(o.directory, "state"), "adapter": primary.Adapter, "provider": primary.Provider, "group": primary.Group, "prompt_file": "prompt.md",
		"progress": config.Progress{Repositories: []string{o.project}, StallAfter: config.Duration{Duration: o.stall}, MinWakeInterval: config.Duration{Duration: o.wake}, MaxNudges: o.nudges},
		"errors":   map[string]any{"max_reprompts": o.reprompts, "backoff": o.backoff.String(), "failover_on": []string{}}, "daemon": config.Daemon{Interval: config.Duration{Duration: o.interval}}}
	if o.deadline != "" || o.maxSends != 0 || o.noProgressTimeout != 0 {
		loop["limits"] = config.Limits{Deadline: o.deadline, MaxSends: o.maxSends, NoProgressTimeout: config.Duration{Duration: o.noProgressTimeout}}
	}
	switch {
	case primary.BB != nil:
		loop["bb"] = primary.BB
	case primary.Claude != nil:
		loop["claude"] = primary.Claude
	case primary.Codex != nil:
		loop["codex"] = primary.Codex
	case primary.Cursor != nil:
		loop["cursor"] = primary.Cursor
	}
	files := map[string][]byte{}
	if len(fallbacks.Candidates) > 0 {
		loop["fallbacks"] = "fallbacks.yaml"
		loop["errors"].(map[string]any)["failover_on"] = []string{"rate_limit"}
		b, err := yaml.Marshal(fallbacks)
		if err != nil {
			return fail(err)
		}
		if _, err := config.ParseFallbacks(b); err != nil {
			return fail(err)
		}
		files["fallbacks.yaml"] = b
	}
	if standard != nil {
		loop["routes"] = config.RoutesStandard
		loop["errors"].(map[string]any)["failover_on"] = []string{"rate_limit"}
	}
	if o.withGoal {
		if err := validateSetupGoal(*o); err != nil {
			return fail(err)
		}
		loop["prompt_goal"] = true
	}
	b, err := yaml.Marshal(map[string]any{"version": 1, "loop": loop})
	if err != nil {
		return fail(err)
	}
	cfg, err := config.Parse(b)
	if err != nil {
		return fail(err)
	}
	if len(fallbacks.Candidates) > 0 {
		cfg.FallbackSet, _ = config.ParseFallbacks(files["fallbacks.yaml"])
	}
	if standard != nil {
		cfg.FallbackSet, cfg.RouteList.Skipped = standard.Resolve(cfg.Loop)
		cfg.RouteList.Source = config.RoutesStandard
	}
	files["keepgoing.yaml"] = b
	files["prompt.md"] = []byte(initPrompt(o.task, filepath.Join(o.directory, "keepgoing.yaml")))
	if o.withGoal {
		files["task.md"] = []byte(o.task + "\n")
		files["prompt.md"] = []byte(initGoalPrompt(filepath.Join(o.directory, "keepgoing.yaml")))
	}
	return files, cfg, nil
}

func initAgent(kind, account, group, model, project string, lookPath func(string) (string, error)) (config.Agent, error) {
	a := config.Agent{Adapter: kind, Group: strings.TrimSpace(group)}
	if strings.Trim(a.Group, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.") != "" {
		return a, errors.New("quota group labels may contain only letters, digits, hyphens, underscores, and dots")
	}
	if kind != "bb" && kind != "claude" && kind != "codex" && kind != "cursor" {
		return a, errors.New("agent must be bb, claude, codex, or cursor")
	}
	if kind == "cursor" && account != "" {
		return a, errors.New("cursor does not support --account-dir; existing Cursor CLI login and environment are inherited")
	}
	cli, err := lookPath(initCLIName(kind))
	if err != nil {
		if kind == "cursor" {
			return a, errors.New("Cursor CLI executable \"agent\" not found on PATH; the desktop cursor launcher is not supported")
		}
		return a, fmt.Errorf("%s CLI not found on PATH", kind)
	}
	cli, err = filepath.Abs(cli)
	if err != nil {
		return a, err
	}
	var env map[string]string
	if account != "" {
		if kind == "bb" {
			return a, errors.New("account-dir is only supported for headless agents")
		}
		account, err = existingDirectory(account)
		if err != nil {
			return a, fmt.Errorf("account directory: %w", err)
		}
		key := "CODEX_HOME"
		if kind == "claude" {
			key = "CLAUDE_CONFIG_DIR"
		}
		env = map[string]string{key: account}
	}
	switch kind {
	case "bb":
		a.BB = &config.BB{CLI: cli}
	case "claude":
		a.Provider = "claude-code"
		a.Claude = &config.Claude{CLI: cli, Workdir: project, Model: model, Env: env}
	case "codex":
		a.Provider = "codex"
		a.Codex = &config.Codex{CLI: cli, Workdir: project, Model: model, Env: env}
	case "cursor":
		a.Provider = "cursor"
		a.Cursor = &config.Cursor{CLI: cli, Workdir: project, Model: model}
	}
	if a.Group == "" {
		a.Group = a.Provider
	}
	return a, nil
}

// Cursor's desktop launcher is named cursor; its headless CLI is agent.
// initCanFollow reports whether a loop of this agent can follow the
// standard route list.
func initCanFollow(agent string) bool { return agent == "bb" || agent == "claude" || agent == "codex" }

// initStandardOffer is how many of the standard list's routes would apply
// to the loop being set up, and how many it has. Nothing is offered when
// none would apply, or when the loop cannot be described yet.
func initStandardOffer(o *initOptions, lookPath func(string) (string, error)) (int, int) {
	path, err := config.StandardRoutesPath()
	if err != nil {
		return 0, 0
	}
	r, err := config.LoadRoutes(path)
	if err != nil || len(r.Routes) == 0 {
		return 0, 0
	}
	a, err := initAgent(o.agent, o.account, o.group, o.model, o.project, lookPath)
	if err != nil {
		return 0, len(r.Routes)
	}
	if a.BB != nil {
		a.BB.Thread, a.Provider = strings.TrimSpace(o.thread), strings.TrimSpace(o.provider)
		if a.Group == "" {
			a.Group = a.Provider
		}
	}
	fb, _ := r.Resolve(config.Loop{Name: o.name, Agent: a})
	return len(candidateIDs(fb)), len(r.Routes)
}

// standardRoutesWritten is the number of routes in the standard list, when
// one is written and can be used.
func standardRoutesWritten() (int, bool) {
	path, err := config.StandardRoutesPath()
	if err != nil {
		return 0, false
	}
	r, err := config.LoadRoutes(path)
	if err != nil || len(r.Routes) == 0 {
		return 0, false
	}
	return len(r.Routes), true
}

func initCLIName(kind string) string {
	if kind == "cursor" {
		return "agent"
	}
	return kind
}

func existingDirectory(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", p)
	}
	return p, nil
}

// Resolve existing ancestors, so a symlink cannot hide a checkout destination.
func canonicalFuturePath(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(p); err == nil {
		return filepath.EvalSymlinks(p)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := canonicalFuturePath(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(p)), nil
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Reserve a new directory exclusively. Never overwrite even an empty existing
// setup, and never change permissions on existing directories.
func writeInit(dir string, files map[string][]byte, spec *control.GoalSpec) (*control.Goal, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	var created []string
	ok := false
	defer func() {
		if !ok {
			for i := len(created) - 1; i >= 0; i-- {
				_ = os.Remove(created[i])
			}
			_ = os.Remove(dir)
		}
	}()
	for name, b := range files {
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		created = append(created, p)
		_, err = f.Write(b)
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	var goal *control.Goal
	if spec != nil {
		stateDir := filepath.Join(dir, "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			return nil, err
		}
		// Only remove files owned by this new setup on failure; never recursively
		// delete an unexpected writer's files. A crash can leave a disabled partial setup.
		created = append(created, stateDir, filepath.Join(stateDir, "tick.lock"), filepath.Join(stateDir, "control.lock"), filepath.Join(stateDir, control.GoalName))
		l, err := lock.Acquire(stateDir)
		if err != nil {
			return nil, err
		}
		defer l.Release()
		cfg, err := config.Load(filepath.Join(dir, "keepgoing.yaml"))
		if err != nil || cfg.Loop.StateDir != stateDir || !cfg.Loop.PromptGoal {
			return nil, errors.New("goal setup configuration changed or unavailable")
		}
		if err := cfg.CheckSources(); err != nil {
			return nil, errors.New("goal setup configuration changed or unavailable")
		}
		goal, err = control.WriteGoal(stateDir, cfg.Loop.Name, "", *spec, "", 0)
		if err != nil {
			return nil, err
		}
	}
	ok = true
	return goal, nil
}

func keepgoingExecutable() string {
	p, err := os.Executable()
	if err != nil {
		return "keepgoing"
	}
	return p
}

// Single quoting avoids shell expansion of task-controlled paths. Windows
// instructions use PowerShell's literal-string syntax.
func commandQuote(s string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func initCommandPrefix() string {
	prefix := commandQuote(keepgoingExecutable())
	if runtime.GOOS == "windows" {
		prefix = "& " + prefix
	}
	return prefix
}

func initPrompt(task, cfgPath string) string {
	prefix := initCommandPrefix()
	return fmt.Sprintf(`Continue the authorized task below. Honor any newer user stop, pause, or steering first, and follow the project's instructions and permission requirements.

keepgoing owns this loop. A separate loop skill is not needed. Evaluate progress and verification yourself; the supervisor uses structured status and explicit control, not transcript wording.

Only when the ENTIRE task is completed and verified, run:
%s complete -c %s --note 'Task completed and verified'
Then report the result. Completing one step is not completion of the whole task.

When unfinished work remains but all remaining work needs a person, run:
%s await-user -c %s --note 'Human input required; see the conversation'
Then report what is needed. A blocked step does not block independent authorized work.

Task:
%s
`, prefix, commandQuote(cfgPath), prefix, commandQuote(cfgPath), task)
}

func printInitAccount(w io.Writer, a config.Agent) {
	var values map[string]string
	if a.Claude != nil {
		values = a.Claude.Env
	}
	if a.Codex != nil {
		values = a.Codex.Env
	}
	for _, path := range values {
		fmt.Fprintf(w, "  Account directory: %s (existing configuration)\n", path)
	}
}

// Signal cancellation must also exit a wizard blocked waiting for terminal input.
func readInitLine(ctx context.Context, r *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() { line, err := r.ReadString('\n'); done <- result{line, err} }()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case v := <-done:
		return v.line, v.err
	}
}

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"gopkg.in/yaml.v3"
)

type fallbackAddOptions struct {
	cfgPath, agent, account, group, model, probeModel, id, workdir string
	thread, provider, reasoning, probeAccount                      string // BB candidates
	permission                                                     string // bb new thread: its permission mode
	threadTitle                                                    string // from a BB listing; display only
	modelListed, showIDs                                           bool
	newThread                                                      bool // bb: start a new thread at handoff instead of naming one
	position, probeCap                                             int
	enableHandoff, nonInteractive, dryRun, asJSON                  bool
	set                                                            map[string]bool // flags given on the command line
}

// fallbackAddPlan is the resolved change. Nothing in it has been written.
type fallbackAddPlan struct {
	cfg           *config.Config
	cfgPath       string
	fallbacksPath string
	createFile    bool
	candidate     config.Candidate
	written       bool               // the fallback was written
	historyFailed bool               // ...but not recorded in the loop's timeline
	order         []config.Candidate // resulting handoff order, new candidate included
	position      int                // 1-based position of the new candidate
	rules         *config.Fallbacks  // redundancy and probe policy that will apply
	wasLive       bool               // handoff could already happen before this change
	failoverOn    []string           // resulting errors.failover_on
	failoverWrite bool               // this command writes errors.failover_on
	failoverWas   string             // the setting being replaced, for the preview
	warnings      []string
	notes         []string
	bb            *fallbackAddBBDetail // set for a BB candidate
	fallbacksYAML []byte
	configYAML    []byte // nil when keepgoing.yaml is left untouched
}

// Success-only integration contract. Account-directory paths, environment
// values, executable paths and raw configuration never enter this report.
// Next commands are argv arrays and are never executed here.
type fallbackAddReport struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Kind          string               `json:"kind"`    // fallback_add
	Outcome       string               `json:"outcome"` // preview or added
	Loop          string               `json:"loop"`
	Candidate     fallbackAddCandidate `json:"candidate"`
	Primary       fallbackAddTarget    `json:"primary"`
	Order         []fallbackAddTarget  `json:"order"`
	Handoff       fallbackAddHandoff   `json:"handoff"`
	Redundancy    fallbackAddRule      `json:"redundancy"`
	Files         fallbackAddFiles     `json:"files"`
	Next          fallbackAddNext      `json:"next"`
	Warnings      []string             `json:"warnings"`
	Notes         []string             `json:"notes"`
	Limitations   []string             `json:"limitations"`
}

type fallbackAddCandidate struct {
	ID         string `json:"id"`
	Adapter    string `json:"adapter"`
	Provider   string `json:"provider"`
	Group      string `json:"group"`
	Model      string `json:"model"`      // empty: the CLI's configured default, unresolved
	Account    string `json:"account"`    // inherited, explicit_directory or chosen_by_bb
	ProbeKind  string `json:"probeKind"`  // claude or codex
	ProbeModel string `json:"probeModel"` // scheduled probe model; empty uses the target model
	Position   int    `json:"position"`
	// BB candidates only. Thread is same_as_primary, same_as_fallback or other;
	// ThreadID is present only with --show-ids.
	Thread       string `json:"thread,omitempty"`
	ThreadID     string `json:"threadId,omitempty"`
	Reasoning    string `json:"reasoning,omitempty"`
	ModelSource  string `json:"modelSource,omitempty"`  // bb_list or unchecked
	ProbeAccount string `json:"probeAccount,omitempty"` // inherited or explicit_directory
}

type fallbackAddTarget struct {
	Position int    `json:"position"` // 0 is the primary
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Group    string `json:"group"`
	Model    string `json:"model,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	New      bool   `json:"new,omitempty"`
}

// Automatic means a handoff can happen after this change: fallbacks exist and
// failoverOn is not empty. Changed compares that with the loop before it.
type fallbackAddHandoff struct {
	Automatic         bool     `json:"automatic"`
	WasAutomatic      bool     `json:"wasAutomatic"`
	Changed           bool     `json:"changed"`
	FailoverOn        []string `json:"failoverOn"`
	FailoverOnWritten bool     `json:"failoverOnWritten"`
}

type fallbackAddRule struct {
	MinHealthy int    `json:"minHealthy"`
	Span       string `json:"span"`
	Reachable  bool   `json:"reachable"` // enough distinct candidates exist; says nothing about health
}

type fallbackAddFiles struct {
	FallbacksPath    string `json:"fallbacksPath"`
	FallbacksCreated bool   `json:"fallbacksCreated"`
	ConfigPath       string `json:"configPath"`
	ConfigChanged    bool   `json:"configChanged"`
}

type fallbackAddNext struct {
	PreviewProbe []string `json:"previewProbe"`
	ExactProbe   []string `json:"exactProbe"`
	List         []string `json:"list"`
}

var fallbackAddLimitations = []string{
	"Nothing was run: no agent, model call, probe, login, account creation or credential copy.",
	"The account's identity, login, model access and quota independence have not been verified.",
	"The exact probe is a real model request that uses the account's quota and may cost money.",
	"A running daemon uses the new order after it reloads its configuration.",
}

const fallbackAddUsage = `Add a fallback to an existing loop: another provider, account or model.

Usage:
  keepgoing fallbacks add -c looptender.yaml                 guided questions, preview, then confirm
  keepgoing fallbacks add -c looptender.yaml --non-interactive --agent codex [flags]

The command looks up installed agent CLIs without running them, previews the
fallback and the handoff order, and writes only after confirmation (or with
--non-interactive and no --dry-run). It never logs in, creates accounts, copies
credentials, calls a model, or sends a message to a thread.

A fallback can be a headless claude or codex session, or an existing BB thread
(--agent bb --thread ID --provider claude-code|codex), including this loop's
own conversation on another model. For a BB thread the guided questions can,
after you agree, run two read-only listings (bb thread list, bb provider
models) to offer pick lists; flags never run bb.

Flags:
`

func fallbacksAddCommand(e env, args []string) int {
	return fallbacksAdd(e, args, os.Stdin, exec.LookPath, runBBLookup)
}

// fallbacksAdd discovers executables, but never invokes an agent or reads its
// login. The only command it may run is a read-only bb listing, through bb,
// after the wizard's user agrees; scripts never trigger one.
func fallbacksAdd(e env, args []string, input io.Reader, lookPath func(string) (string, error), bb bbLookup) int {
	o := fallbackAddOptions{set: map[string]bool{}}
	fs := flag.NewFlagSet("fallbacks add", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprint(e.stderr, fallbackAddUsage)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cfgPath, "c", "", configFlagHelp("path to the loop's config file"))
	fs.StringVar(&o.agent, "agent", "", "claude or codex (headless), or bb (an existing BB thread)")
	fs.StringVar(&o.thread, "thread", "", "bb: existing thread ID; may be the primary's thread with another model")
	fs.BoolVar(&o.newThread, "new-thread", false, "bb: instead of --thread, let a handoff start a new BB thread on this provider and model")
	fs.StringVar(&o.permission, "permission-mode", "", "bb --new-thread: the new thread's permission mode: accept-edits (default), auto or full")
	fs.StringVar(&o.provider, "provider", "", "bb: the thread's provider, claude-code or codex")
	fs.StringVar(&o.reasoning, "reasoning", "", "bb: reasoning level set at handoff: low, medium, high, xhigh or max (default: unchanged)")
	fs.StringVar(&o.probeAccount, "probe-account-dir", "", "bb: account directory the local probe CLI uses (default: its current login)")
	fs.BoolVar(&o.showIDs, "show-ids", false, "bb: print thread IDs in listings, the preview and JSON")
	fs.StringVar(&o.account, "account-dir", "", "existing account configuration directory (default: the CLI's current login)")
	fs.StringVar(&o.group, "group", "", "quota group label; accounts sharing limits must share it (default: account directory name, else provider)")
	fs.StringVar(&o.model, "model", "", "model for this fallback's turns (default: CLI configuration)")
	fs.StringVar(&o.probeModel, "probe-model", "", "cheaper model for scheduled health probes (default: the target model)")
	fs.StringVar(&o.id, "id", "", "candidate id: lowercase letters, digits, hyphens (default: derived from agent and group)")
	fs.StringVar(&o.workdir, "workdir", "", "project directory the fallback works in (default: the primary's)")
	fs.IntVar(&o.position, "position", 0, "1-based place in the handoff order (default: last)")
	fs.IntVar(&o.probeCap, "probe-daily-cap", 2, "scheduled probes per fallback per UTC day; only when this creates the fallbacks file")
	fs.BoolVar(&o.enableHandoff, "enable-handoff", false, "let this loop hand off automatically (default: handoff stays off)")
	fs.BoolVar(&o.nonInteractive, "non-interactive", false, "take every answer from flags; write without asking unless --dry-run")
	fs.BoolVar(&o.dryRun, "dry-run", false, "preview only; write nothing")
	fs.BoolVar(&o.asJSON, "json", false, "print a versioned result; requires --non-interactive")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	fail := func(code int, format string, a ...any) int {
		e.errf("fallbacks add: "+format, a...)
		return code
	}
	refuse := func(err error) int {
		e.errf("fallbacks add: refused, nothing written.\n  %v", err)
		return exitUsage
	}
	if fs.NArg() != 0 {
		return fail(exitUsage, "unexpected positional arguments")
	}
	if o.asJSON && !o.nonInteractive {
		return fail(exitUsage, "--json requires --non-interactive")
	}
	if o.nonInteractive && o.agent == "" {
		return fail(exitUsage, "--non-interactive requires --agent")
	}
	fs.Visit(func(f *flag.Flag) { o.set[f.Name] = true })
	// Relative paths in a config resolve against the directory it was opened
	// from, so a symlinked path would describe a different loop than its target.
	if !configDefault(e, "fallbacks add", &o.cfgPath) {
		return exitUsage
	}
	if info, err := os.Lstat(o.cfgPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return refuse(errors.New("The config path is a symbolic link. Pass the real configuration file instead."))
	}
	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		e.errf("config: %v", err)
		return exitUsage
	}
	if cfg.Loop.Adapter == "cursor" {
		return refuse(errors.New("A Cursor primary supports standalone loops only; fallbacks are unverified."))
	}
	if cfg.Loop.FollowsStandard() {
		return refuse(errors.New(followsStandardRefusal("add")))
	}
	if cfg.Loop.Fallbacks != "" {
		if info, err := os.Lstat(cfg.Loop.Fallbacks); err != nil || !info.Mode().IsRegular() {
			return refuse(errors.New("The loop's fallbacks file is a symbolic link or not a regular file.\n  Point loop.fallbacks at the real file instead."))
		}
	}
	// Advisory: the same checks run again under the tick lock before writing.
	if ref := ownListAuthority(cfg.Loop.StateDir, os.Getenv("BB_THREAD_ID")); ref != nil {
		e.errf("fallbacks add: refused, nothing written.\n  %s", ref.Text)
		return exitUsage
	}
	if code, err := fallbackAddAllowed(cfg); err != nil {
		e.errf("fallbacks add: refused, nothing written.\n  %v", err)
		return code
	}
	w := &fallbackAddWizard{e: e, cfg: cfg, o: &o, lookPath: lookPath, bb: bb}
	if !o.nonInteractive {
		w.r = bufio.NewReader(input)
		if err := w.collect(); err != nil {
			e.errf("\nfallbacks add: stopped, nothing written.\n  %v", err)
			return exitUsage
		}
	}
	plan, err := planFallbackAdd(cfg, o, lookPath)
	if err != nil {
		return refuse(err)
	}
	if o.asJSON {
		if !o.dryRun {
			if code, err := writeFallbackAdd(plan); err != nil {
				return fail(code, "%v", err)
			}
		}
		if err := json.NewEncoder(e.stdout).Encode(fallbackAddReportOf(plan, o.dryRun)); err != nil {
			return fail(exitFailed, "could not write JSON result; files may already be changed")
		}
		return exitOK
	}
	printFallbackAddPreview(e.stdout, plan)
	if o.dryRun {
		fmt.Fprintln(e.stdout, "\nDry run: nothing written.")
		return exitOK
	}
	if !o.nonInteractive {
		fmt.Fprintln(e.stdout)
		yes, err := w.askYesNo("Write this fallback? (yes/no)", "no")
		if err != nil {
			return fail(exitUsage, "%v", err)
		}
		if !yes {
			fmt.Fprintln(e.stdout, "\nCancelled: nothing written.")
			return exitOK
		}
	}
	if err := e.ctx.Err(); err != nil {
		return fail(exitFailed, "%v", err)
	}
	if code, err := writeFallbackAdd(plan); err != nil {
		return fail(code, "%v", err)
	}
	printFallbackAddNext(e.stdout, plan)
	if plan.historyFailed {
		fmt.Fprintln(e.stdout, "Note: the fallback was added, but could not be recorded in the loop's history (keepgoing timeline).")
	}
	return exitOK
}

// fallbackAddAllowed applies the guards retarget and manual probes use: the
// calling BB thread must own the loop, and no BB transfer may be pending.
func fallbackAddAllowed(cfg *config.Config) (int, error) {
	if err := checkOwnership(cfg.Loop.StateDir, nil); err != nil {
		code := exitFailed
		if errors.Is(err, lock.ErrHeld) {
			code = exitSkipped
		}
		return code, errors.New("Ownership: " + strings.Replace(err.Error(), "; ", ";\n  ", 1))
	}
	if err := checkTransferSources(cfg, false); err != nil {
		return exitSkipped, fmt.Errorf("%w; reload and retry", err)
	}
	return exitOK, nil
}

// fallbackAddLogin is one target's account, as far as configuration shows it.
type fallbackAddLogin struct {
	owner, kind, dir, model string
}

func fallbackAddAccountKey(kind string) string {
	if spec, ok := runner.SpecFor(kind); ok {
		return spec.AccountEnv
	}
	return ""
}

// fallbackAddLogins lists the headless logins already in the loop. A BB or
// process target's account is not visible in configuration and is omitted.
func fallbackAddLogins(cfg *config.Config) []fallbackAddLogin {
	var out []fallbackAddLogin
	add := func(owner string, a config.Agent) {
		// Only built-in headless agents have an account directory variable.
		if _, builtin := runner.SpecFor(a.Adapter); !builtin {
			return
		}
		_, set, ok := runner.Resolve(a)
		if !ok {
			return
		}
		env := set.Env
		dir := env[fallbackAddAccountKey(a.Adapter)]
		if dir != "" {
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				dir = real
			}
			dir = filepath.Clean(dir)
		}
		out = append(out, fallbackAddLogin{owner, a.Adapter, dir, a.Model()})
	}
	add("the primary", cfg.Loop.Agent)
	if cfg.FallbackSet != nil {
		for _, c := range cfg.FallbackSet.Candidates {
			add(fmt.Sprintf("fallback %q", c.ID), c.Agent)
		}
	}
	return out
}

// fallbackAddSameLogin refuses a candidate that is the same login and model
// as an existing target, and warns when only the model differs.
func fallbackAddSameLogin(cfg *config.Config, kind, dir, model string) (warning string, err error) {
	for _, l := range fallbackAddLogins(cfg) {
		if l.kind != kind || l.dir != dir {
			continue
		}
		if model == "" || model == l.model {
			return "", fmt.Errorf("This is the same login as %s with the same model, so it\n  shares that limit and cannot take over. Use another account directory,\n  or a different model that has its own limit.", l.owner)
		}
		return fmt.Sprintf("Same login as %s. This helps only if model %s has its own limit; an account-wide limit stops both.", l.owner, model), nil
	}
	if cfg.Loop.Adapter == "bb" && dir == "" {
		return "The primary BB thread may use this same login; keepgoing cannot tell.", nil
	}
	return "", nil
}

// fallbackAddAccount expands a leading ~ and requires an existing directory.
func fallbackAddAccount(account string) (string, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return "", nil
	}
	if account == "~" || strings.HasPrefix(account, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("Account directory: the home directory is unknown; give a full path.")
		}
		account = filepath.Join(home, strings.TrimPrefix(account, "~"))
	}
	dir, err := existingDirectory(account)
	if err != nil {
		return "", errors.New("Account directory: not an existing directory. Sign in to it first, or leave it blank.")
	}
	return dir, nil
}

func fallbackAddGroup(cfg *config.Config, group string) error {
	if group == "" || strings.Trim(group, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.") != "" {
		return errors.New("Quota group labels may contain only letters, digits, hyphens,\n  underscores and dots.")
	}
	if group == cfg.Loop.Group {
		return fmt.Errorf("Quota group %q is the primary's. A fallback on the same\n  limit cannot take over: choose an independent account and its own group.", group)
	}
	if cfg.FallbackSet != nil {
		for _, c := range cfg.FallbackSet.Candidates {
			if c.Group == group {
				return fmt.Errorf("Quota group %q is already used by fallback %q.\n  Candidates sharing a limit cannot back each other up: choose an\n  independent account and its own group.", group, c.ID)
			}
		}
	}
	return nil
}

func fallbackAddModelName(what, v string) error {
	if strings.HasPrefix(v, "-") || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || bbUnprintable(r) }) || !utf8.ValidString(v) {
		return fmt.Errorf("The %s must be a single name without spaces or a leading hyphen.", what)
	}
	return nil
}

func fallbackAddPosition(cfg *config.Config, s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if max := fallbackAddExisting(cfg) + 1; err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("The position must be a number between 1 and %d.", max)
	}
	return n, nil
}

type fallbackAddWizard struct {
	e        env
	cfg      *config.Config
	o        *fallbackAddOptions
	r        *bufio.Reader
	lookPath func(string) (string, error)
	bb       bbLookup
}

func (w *fallbackAddWizard) ask(label, def string) (string, error) {
	if err := w.e.ctx.Err(); err != nil {
		return "", err
	}
	if def == "" {
		fmt.Fprintf(w.e.stdout, "  %s: ", label)
	} else {
		fmt.Fprintf(w.e.stdout, "  %s [%s]: ", label, def)
	}
	s, err := readInitLine(w.e.ctx, w.r)
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		fmt.Fprintln(w.e.stdout)
		return "", fmt.Errorf("input ended (use --non-interactive for scripts): %w", err)
	}
	if s = strings.TrimSpace(s); s == "" {
		s = def
	}
	return s, nil
}

// askValid repeats a question until check accepts the answer. A value given
// as a flag is checked once and never asked.
func (w *fallbackAddWizard) askValid(flagName, label, def string, value *string, check func(string) error) error {
	if w.o.set[flagName] {
		return check(*value)
	}
	for {
		s, err := w.ask(label, def)
		if err != nil {
			return err
		}
		if err := check(s); err != nil {
			fmt.Fprintf(w.e.stdout, "  ! %v\n", err)
			continue
		}
		*value = s
		return nil
	}
}

func (w *fallbackAddWizard) askYesNo(label, def string) (bool, error) {
	for {
		s, err := w.ask(label, def)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "yes", "y":
			return true, nil
		case "no", "n":
			return false, nil
		}
		fmt.Fprintln(w.e.stdout, "  ! Please answer yes or no.")
	}
}

func (w *fallbackAddWizard) collect() error {
	out, cfg, o := w.e.stdout, w.cfg, w.o
	fmt.Fprintf(out, "Add a fallback to loop %q\n\n", cfg.Loop.Name)
	printFallbackAddCurrent(out, cfg)

	fmt.Fprintln(out, "\nAgent CLIs found on PATH (looked up only; nothing was run):")
	var usable []string
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	for _, a := range []struct{ kind, note string }{
		{"claude", "can be a fallback"},
		{"codex", "can be a fallback"},
		{"bb", "an existing thread can be one"},
		{"cursor", "cannot be a fallback yet"},
	} {
		p, err := w.lookPath(initCLIName(a.kind))
		if err != nil {
			fmt.Fprintf(tw, "  %s\tnot found\t\n", a.kind)
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", a.kind, p, a.note)
		if a.kind != "cursor" {
			usable = append(usable, a.kind)
		}
	}
	tw.Flush()
	if len(usable) == 0 {
		return errors.New("No claude, codex or bb CLI was found on PATH. Install and sign in to one first.")
	}

	def := usable[0]
	for _, a := range usable { // prefer a provider other than the primary's
		if a != cfg.Loop.Adapter && a != "bb" {
			def = a
			break
		}
	}
	fmt.Fprintln(out, "\nStep 1 of 4: Provider")
	err := w.askValid("agent", "Which agent takes over? ("+strings.Join(usable, "/")+")", def, &o.agent, func(s string) error {
		if err := fallbackAddAgentSupported(s); err != nil {
			return err
		}
		if _, err := w.lookPath(initCLIName(s)); err != nil {
			return fmt.Errorf("The %s CLI was not found on PATH.", s)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if o.agent == "bb" {
		if err := w.collectBB(); err != nil {
			return err
		}
		return w.collectHandoff()
	}

	fmt.Fprintln(out, "\nStep 2 of 4: Account")
	fmt.Fprintln(out, "  Give a directory you have already signed in to, or leave blank to use")
	fmt.Fprintln(out, "  the CLI's current login. Nothing is created or copied.")
	err = w.askValid("account-dir", "Account directory", "", &o.account, func(s string) error {
		_, err := fallbackAddAccount(s)
		return err
	})
	if err != nil {
		return err
	}
	dir, _ := fallbackAddAccount(o.account)
	sameLogin := false
	for _, l := range fallbackAddLogins(cfg) {
		if l.kind == o.agent && l.dir == dir {
			sameLogin = true
			fmt.Fprintf(out, "  Note: this is the same login as %s. It can only help with a\n  different model that has its own limit; you will choose the model next.\n", l.owner)
			break
		}
	}
	fmt.Fprintln(out, "  A quota group names one usage limit. Accounts that share a limit must share")
	fmt.Fprintf(out, "  a group; a fallback must not use the primary's group (%s).\n", cfg.Loop.Group)
	groupDef := fallbackAddFreeGroup(cfg, fallbackAddDefaultGroup(o.agent, dir))
	if err := w.askValid("group", "Quota group", groupDef, &o.group, func(s string) error { return fallbackAddGroup(cfg, s) }); err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 3 of 4: Model")
	if sameLogin {
		fmt.Fprintln(out, "  Name a model that differs from the one already used on this login.")
	} else {
		fmt.Fprintln(out, "  Leave blank to use the CLI's configured model. Names are not checked here.")
	}
	err = w.askValid("model", "Model", "", &o.model, func(s string) error {
		if err := fallbackAddModelName("model", s); err != nil {
			return err
		}
		_, err := fallbackAddSameLogin(cfg, o.agent, dir, s)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "  Scheduled health probes can use a cheaper model; blank uses the same one.")
	if err := w.askValid("probe-model", "Probe model", "", &o.probeModel, func(s string) error { return fallbackAddModelName("probe model", s) }); err != nil {
		return err
	}

	return w.collectHandoff()
}

func (w *fallbackAddWizard) collectHandoff() error {
	out, cfg, o := w.e.stdout, w.cfg, w.o
	var err error
	fmt.Fprintln(out, "\nStep 4 of 4: Handoff")
	if n := fallbackAddExisting(cfg); n > 0 && !o.set["position"] {
		var answer string
		err := w.askValid("position", fmt.Sprintf("Place in the handoff order (1 = tried first, %d = last)", n+1), strconv.Itoa(n+1), &answer, func(s string) error {
			_, err := fallbackAddPosition(cfg, s)
			return err
		})
		if err != nil {
			return err
		}
		o.position, _ = fallbackAddPosition(cfg, answer)
	}
	on := cfg.Loop.Errors.FailoverOn
	switch {
	case cfg.FallbackSet != nil && len(on) > 0:
		fmt.Fprintf(out, "  Automatic handoff is already on for: %s.\n", strings.Join(on, ", "))
	case o.set["enable-handoff"]:
		fmt.Fprintln(out, "  Automatic handoff will be turned on (--enable-handoff).")
	default:
		fmt.Fprintln(out, "  Automatic handoff is off for this loop. Turning it on lets keepgoing move")
		fmt.Fprintln(out, "  the loop to a fallback when the active agent reports a rate limit.")
		if len(on) > 0 {
			fmt.Fprintf(out, "  This loop's errors.failover_on is %s, so answering no writes an\n  explicit empty setting to keep handoff off.\n", strings.Join(on, ", "))
		}
		if o.enableHandoff, err = w.askYesNo("Turn on automatic handoff for rate limits? (yes/no)", "no"); err != nil {
			return err
		}
	}
	fmt.Fprintln(out)
	return nil
}

func fallbackAddExisting(cfg *config.Config) int {
	if cfg.FallbackSet == nil {
		return 0
	}
	return len(cfg.FallbackSet.Candidates)
}

// fallbackAddDefaultGroup labels a separate account directory by its name, so
// several accounts of one provider do not collide on the provider label.
func fallbackAddDefaultGroup(agent, account string) string {
	if account != "" {
		if g := fallbackAddSlug(filepath.Base(filepath.Clean(account))); g != "" {
			return g
		}
	}
	if spec, ok := runner.SpecFor(agent); ok {
		return spec.Provider
	}
	return agent
}

// fallbackAddFreeGroup makes a suggested label one the wizard can accept.
func fallbackAddFreeGroup(cfg *config.Config, base string) string {
	g := base
	for n := 2; fallbackAddGroup(cfg, g) != nil && n < 100; n++ {
		g = fmt.Sprintf("%s-%d", base, n)
	}
	return g
}

func fallbackAddAgentSupported(agent string) error {
	switch agent {
	case "claude", "codex", "bb":
		return nil
	case "cursor":
		return errors.New("Cursor cannot be a fallback yet: its account and model probes are unverified.")
	case "process":
		return errors.New("Guided add supports claude, codex and bb only.\n  A process candidate still needs a hand-written entry; see docs/fallbacks.md.")
	case "":
		return errors.New("Choose an agent: claude, codex or bb.")
	}
	return fmt.Errorf("Unknown agent %q. Choose claude, codex or bb.", agent)
}

// fallbackAddSlug maps a label to the candidate-id alphabet.
func fallbackAddSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func planFallbackAdd(cfg *config.Config, o fallbackAddOptions, lookPath func(string) (string, error)) (*fallbackAddPlan, error) {
	if err := fallbackAddAgentSupported(o.agent); err != nil {
		return nil, err
	}
	if err := fallbackAddModelName("model", o.model); err != nil {
		return nil, err
	}
	if err := fallbackAddModelName("probe model", o.probeModel); err != nil {
		return nil, err
	}
	agent, probe, warnings, err := fallbackAddTargetOf(cfg, o, lookPath)
	if err != nil {
		return nil, err
	}
	cfgPath, err := filepath.Abs(o.cfgPath)
	if err != nil {
		return nil, err
	}
	current := cfg.Loop.Errors.FailoverOn
	p := &fallbackAddPlan{cfg: cfg, cfgPath: cfgPath, fallbacksPath: cfg.Loop.Fallbacks, failoverOn: append([]string{}, current...)}
	p.warnings, p.bb = warnings, fallbackAddBBDetailOf(cfg, o, agent)
	var existing []config.Candidate
	if cfg.FallbackSet != nil {
		existing = cfg.FallbackSet.Candidates
		if o.set["probe-daily-cap"] {
			return nil, errors.New("--probe-daily-cap applies only when this command creates the fallbacks\n  file. Edit probe.daily_cap in the existing file instead.")
		}
	} else {
		p.createFile = true
		p.fallbacksPath = filepath.Join(filepath.Dir(p.cfgPath), "fallbacks.yaml")
		if _, err := os.Lstat(p.fallbacksPath); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s already exists but the loop does not use it.\n  Set loop.fallbacks or move the file.", p.fallbacksPath)
		}
		if o.probeCap < 1 {
			return nil, errors.New("--probe-daily-cap must be positive.")
		}
	}
	ids := map[string]bool{"primary": true}
	for _, c := range existing {
		ids[c.ID] = true
	}
	id := o.id
	if id == "" {
		base := fallbackAddSlug(o.agent + "-" + agent.Group)
		if o.agent != "bb" && (agent.Group == agent.Provider || strings.HasPrefix(fallbackAddSlug(agent.Group), o.agent)) {
			base = fallbackAddSlug(agent.Group)
		}
		if len(base) > 60 {
			base = strings.Trim(base[:60], "-")
		}
		id = base
		for n := 2; ids[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
	} else if len(id) > 64 || fallbackAddSlug(id) != id {
		return nil, errors.New("--id must be 1-64 lowercase letters, digits or hyphens, starting and\n  ending with a letter or digit.")
	} else if ids[id] {
		return nil, fmt.Errorf("The id %q is already in use.", id)
	}
	p.candidate = config.Candidate{ID: id, Agent: agent, Probe: probe}
	p.position = o.position
	if p.position == 0 {
		p.position = len(existing) + 1
	}
	if p.position < 1 || p.position > len(existing)+1 {
		return nil, fmt.Errorf("The position must be a number between 1 and %d.", len(existing)+1)
	}
	p.order = append(append(append([]config.Candidate{}, existing[:p.position-1]...), p.candidate), existing[p.position-1:]...)

	// Handoff is a choice of this command, never a side effect of adding the
	// first fallback: an absent errors.failover_on defaults to rate_limit.
	rawCfg, err := os.ReadFile(p.cfgPath)
	if err != nil {
		return nil, errors.New("The configuration is unreadable.")
	}
	keySet := yamlMapValue(yamlMapValue(yamlMapValue(yamlDocRoot(rawCfg), "loop"), "errors"), "failover_on") != nil
	p.wasLive = !p.createFile && len(current) > 0
	p.failoverWas = "default " + strings.Join(current, ", ")
	if keySet {
		p.failoverWas = strings.Join(current, ", ")
	}
	switch {
	case p.wasLive:
		if o.enableHandoff {
			p.notes = append(p.notes, "--enable-handoff had no effect: automatic handoff was already on.")
		}
	case o.enableHandoff:
		if len(current) == 0 {
			p.failoverOn = []string{"rate_limit"}
		}
		p.failoverWrite = !keySet || len(current) == 0 // make an inherited default explicit
	case len(current) > 0: // first fallback, and the current setting would start handoff
		p.failoverOn, p.failoverWrite = []string{}, true
	}

	if p.createFile {
		fb := config.Fallbacks{Version: 1, Redundancy: config.Redundancy{MinHealthy: 1, Span: "group"},
			Probe:      config.ProbePolicy{Every: config.Duration{Duration: 12 * time.Hour}, TTL: config.Duration{Duration: 24 * time.Hour}, DailyCap: o.probeCap},
			Candidates: []config.Candidate{p.candidate}}
		if p.fallbacksYAML, err = yaml.Marshal(fb); err != nil {
			return nil, err
		}
	} else {
		raw, err := os.ReadFile(p.fallbacksPath)
		if err != nil {
			return nil, errors.New("The fallbacks file is unreadable.")
		}
		if p.fallbacksYAML, err = insertCandidateYAML(raw, p.candidate, p.position-1); err != nil {
			return nil, err
		}
	}
	parsed, err := config.ParseFallbacks(p.fallbacksYAML)
	if err != nil {
		return nil, fmt.Errorf("The resulting fallbacks file would be invalid: %w", err)
	}
	if len(parsed.Candidates) != len(p.order) || parsed.Candidates[p.position-1].ID != id {
		return nil, errors.New("Could not place the candidate in the existing fallbacks file; edit it by hand.")
	}
	p.rules = parsed
	if p.createFile || p.failoverWrite {
		var set *[]string
		if p.failoverWrite {
			set = &p.failoverOn
		}
		if p.configYAML, err = editLoopYAML(rawCfg, p.createFile, set); err != nil {
			return nil, err
		}
		next, err := config.Parse(p.configYAML)
		if err != nil {
			return nil, fmt.Errorf("The resulting configuration would be invalid: %w", err)
		}
		if p.createFile && next.Loop.Fallbacks != "fallbacks.yaml" || strings.Join(next.Loop.Errors.FailoverOn, ",") != strings.Join(p.failoverOn, ",") {
			return nil, errors.New("Could not update the configuration file safely; edit it by hand.")
		}
	}
	return p, nil
}

// fallbackAddTargetOf resolves the candidate's agent and probe from the
// answers, with any warnings the preview must show.
func fallbackAddTargetOf(cfg *config.Config, o fallbackAddOptions, lookPath func(string) (string, error)) (config.Agent, config.Probe, []string, error) {
	fail := func(err error) (config.Agent, config.Probe, []string, error) {
		return config.Agent{}, config.Probe{}, nil, err
	}
	if o.agent == "bb" {
		return fallbackAddBBTarget(cfg, o, lookPath)
	}
	for _, name := range []string{"thread", "new-thread", "permission-mode", "provider", "reasoning", "probe-account-dir", "show-ids"} {
		if o.set[name] {
			return fail(fmt.Errorf("--%s applies only to --agent bb.", name))
		}
	}
	account, err := fallbackAddAccount(o.account)
	if err != nil {
		return fail(err)
	}
	group := strings.TrimSpace(o.group)
	if group == "" {
		group = fallbackAddDefaultGroup(o.agent, account)
	}
	if err := fallbackAddGroup(cfg, group); err != nil {
		return fail(err)
	}
	warning, err := fallbackAddSameLogin(cfg, o.agent, account, o.model)
	if err != nil {
		return fail(err)
	}
	workdir := o.workdir
	if workdir == "" {
		switch {
		case cfg.Loop.Claude != nil:
			workdir = cfg.Loop.Claude.Workdir
		case cfg.Loop.Codex != nil:
			workdir = cfg.Loop.Codex.Workdir
		case len(cfg.Loop.Progress.Repositories) > 0:
			workdir = cfg.Loop.Progress.Repositories[0]
		default:
			return fail(errors.New("The loop names no project directory; pass --workdir."))
		}
	}
	if workdir, err = existingDirectory(workdir); err != nil {
		return fail(errors.New("Project directory: not an existing directory; pass --workdir."))
	}
	agent, err := initAgent(o.agent, account, group, o.model, workdir, lookPath)
	if err != nil {
		return fail(err)
	}
	var warnings []string
	if warning != "" {
		warnings = append(warnings, warning)
	}
	return agent, config.Probe{Kind: o.agent, Model: o.probeModel}, warnings, nil
}

func yamlDocRoot(raw []byte) *yaml.Node {
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return yamlRoot(&doc)
}

// insertCandidateYAML edits the document tree so existing comments and
// candidates survive. Indentation may be normalized.
func insertCandidateYAML(raw []byte, c config.Candidate, at int) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, errors.New("The fallbacks file is not valid YAML.")
	}
	list := yamlMapValue(yamlRoot(&doc), "candidates")
	if list == nil || list.Kind != yaml.SequenceNode || at > len(list.Content) {
		return nil, errors.New("The fallbacks file has no editable candidates list; edit it by hand.")
	}
	var n yaml.Node
	if err := n.Encode(c); err != nil {
		return nil, err
	}
	list.Style = 0 // a flow-style list cannot hold a block candidate legibly
	list.Content = append(list.Content[:at], append([]*yaml.Node{&n}, list.Content[at:]...)...)
	return yaml.Marshal(&doc)
}

// editLoopYAML sets loop.fallbacks and/or errors.failover_on, keeping the
// rest of the document, including comments.
func editLoopYAML(raw []byte, setFallbacks bool, failoverOn *[]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, errors.New("The configuration is not valid YAML.")
	}
	loop := yamlMapValue(yamlRoot(&doc), "loop")
	if loop == nil || loop.Kind != yaml.MappingNode {
		return nil, errors.New("The configuration has no editable loop section; edit it by hand.")
	}
	if setFallbacks {
		yamlMapSet(loop, "fallbacks", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fallbacks.yaml"})
	}
	if failoverOn != nil {
		errs := yamlMapValue(loop, "errors")
		if errs == nil {
			errs = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			yamlMapSet(loop, "errors", errs)
		}
		if errs.Kind != yaml.MappingNode {
			return nil, errors.New("loop.errors is not editable; edit it by hand.")
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, class := range *failoverOn {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: class})
		}
		// Comments written beside the old value stay beside the new one.
		if old := yamlMapValue(errs, "failover_on"); old != nil {
			seq.HeadComment, seq.LineComment, seq.FootComment = old.HeadComment, old.LineComment, old.FootComment
		}
		yamlMapSet(errs, "failover_on", seq)
	}
	return yaml.Marshal(&doc)
}

func yamlRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	return nil
}

func yamlMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func yamlMapSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// fallbackAddReplace is swapped by tests to exercise write failures.
var fallbackAddReplace = replaceFile

// writeFallbackAdd holds the tick lock, so supervision never loads a
// half-applied change. It applies the ownership and transfer guards that
// retarget uses and refuses when the reviewed sources changed.
func writeFallbackAdd(p *fallbackAddPlan) (int, error) {
	l, err := lock.Acquire(p.cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		return exitSkipped, errors.New("a check is running; nothing written. Try again in a moment")
	}
	if err != nil {
		return exitFailed, fmt.Errorf("lock: %w", err)
	}
	defer l.Release()
	caller := os.Getenv("BB_THREAD_ID")
	if ref := ownListAuthority(p.cfg.Loop.StateDir, caller); ref != nil {
		return exitUsage, fmt.Errorf("%s Nothing written", ref.Text)
	}
	if code, err := fallbackAddAllowed(p.cfg); err != nil {
		return code, fmt.Errorf("%w; nothing written", err)
	}
	// Recorded once the files are written; a record that cannot be written
	// leaves the change standing and says so.
	defer func() {
		if p.written {
			p.historyFailed = recordRouteChange(p.cfg.Loop.StateDir, "add", p.candidate.ID, caller) != nil
		}
	}()
	if p.createFile {
		f, err := os.OpenFile(p.fallbacksPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return exitFailed, errors.New("could not create the fallbacks file; nothing written")
		}
		_, err = f.Write(p.fallbacksYAML)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = fallbackAddReplace(p.cfgPath, p.configYAML)
		}
		if err != nil {
			_ = os.Remove(p.fallbacksPath) // created above, by this command
			return exitFailed, errors.New("could not update the configuration file; nothing written")
		}
		p.written = true
		return exitOK, nil
	}
	if err := fallbackAddReplace(p.fallbacksPath, p.fallbacksYAML); err != nil {
		return exitFailed, errors.New("could not update the fallbacks file; nothing written")
	}
	p.written = true
	if p.configYAML != nil {
		if err := fallbackAddReplace(p.cfgPath, p.configYAML); err != nil {
			return exitFailed, errors.New("the fallback was added, but errors.failover_on could not be updated in the configuration file; automatic handoff is unchanged")
		}
	}
	return exitOK, nil
}

// replaceFile swaps in new contents through a synced private sibling file,
// keeping the original's permission bits. It follows the existing config
// replacement (config.BBTransferEdit.Commit): sync, then os.Rename. The
// control package's POSIX-rename helper is not reused because it exists for
// control snapshots, whose readers open with delete sharing; configuration is
// read with ordinary opens, so that helper would add its filesystem
// requirement here without making a concurrent Windows reader any safer. A
// failed rename leaves the original file untouched.
func replaceFile(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".fallbacks-add-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// fallbackAddReachable reports whether enough distinct candidates exist to
// satisfy the redundancy rule while the primary is active. Health is unknown.
func fallbackAddReachable(p *fallbackAddPlan) bool {
	distinct := map[string]bool{}
	for _, c := range p.order {
		if c.Disabled || c.Group == p.cfg.Loop.Group {
			continue
		}
		switch p.rules.Redundancy.Span {
		case "provider":
			distinct[c.Provider] = true
		case "group":
			distinct[c.Group] = true
		default:
			distinct[c.ID] = true
		}
	}
	return len(distinct) >= p.rules.Redundancy.MinHealthy
}

func fallbackAddArgv(p *fallbackAddPlan, rest ...string) []string {
	return append([]string{keepgoingExecutable(), "fallbacks"}, append(rest, "-c", p.cfgPath)...)
}

func fallbackAddReportOf(p *fallbackAddPlan, preview bool) fallbackAddReport {
	c, live := p.candidate, len(p.failoverOn) > 0
	r := fallbackAddReport{SchemaVersion: 1, Kind: "fallback_add", Outcome: "added", Loop: p.cfg.Loop.Name,
		Candidate: fallbackAddCandidate{ID: c.ID, Adapter: c.Adapter, Provider: c.Provider, Group: c.Group, Model: c.Model(),
			Account: initTargetOf(c.Agent).Account, ProbeKind: c.Probe.Kind, ProbeModel: c.Probe.Model, Position: p.position},
		Primary:    fallbackAddTarget{ID: "primary", Provider: p.cfg.Loop.Provider, Group: p.cfg.Loop.Group, Model: p.cfg.Loop.Agent.Model()},
		Handoff:    fallbackAddHandoff{Automatic: live, WasAutomatic: p.wasLive, Changed: live != p.wasLive, FailoverOn: p.failoverOn, FailoverOnWritten: p.failoverWrite},
		Redundancy: fallbackAddRule{MinHealthy: p.rules.Redundancy.MinHealthy, Span: p.rules.Redundancy.Span, Reachable: fallbackAddReachable(p)},
		Files:      fallbackAddFiles{FallbacksPath: p.fallbacksPath, FallbacksCreated: p.createFile, ConfigPath: p.cfgPath, ConfigChanged: p.configYAML != nil},
		Next: fallbackAddNext{
			PreviewProbe: fallbackAddArgv(p, "check", "--id", c.ID, "--exact", "--dry-run"),
			ExactProbe:   fallbackAddArgv(p, "check", "--id", c.ID, "--exact"),
			List:         fallbackAddArgv(p, "list"),
		},
		Warnings: append([]string{}, p.warnings...), Notes: append([]string{}, p.notes...),
		Limitations: fallbackAddLimitations,
	}
	if d := p.bb; d != nil {
		r.Candidate.Account = "chosen_by_bb"
		r.Candidate.Thread, r.Candidate.Reasoning = d.relation, c.BB.Reasoning
		r.Candidate.ModelSource, r.Candidate.ProbeAccount = "unchecked", "inherited"
		if d.modelListed {
			r.Candidate.ModelSource = "bb_list"
		}
		if d.probeAccount != "" {
			r.Candidate.ProbeAccount = "explicit_directory"
		}
		if d.showIDs {
			r.Candidate.ThreadID = d.thread
		}
	}
	if preview {
		r.Outcome = "preview"
	}
	for i, o := range p.order {
		r.Order = append(r.Order, fallbackAddTarget{Position: i + 1, ID: o.ID, Provider: o.Provider, Group: o.Group, Model: o.Model(), Disabled: o.Disabled, New: o.ID == c.ID})
	}
	return r
}

func fallbackAddModel(m string) string {
	if m == "" {
		return "CLI default"
	}
	return m
}

// fallbackAddAgentModel names an unset model by who decides it.
func fallbackAddAgentModel(a config.Agent) string {
	if a.Model() == "" && a.BB != nil {
		return "thread's own"
	}
	return fallbackAddModel(a.Model())
}

func printFallbackAddCurrent(w io.Writer, cfg *config.Config) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  Primary:\t%s, quota group %s, model %s\n", cfg.Loop.Provider, cfg.Loop.Group, fallbackAddAgentModel(cfg.Loop.Agent))
	if n := fallbackAddExisting(cfg); n == 0 {
		fmt.Fprintf(tw, "  Fallbacks:\tnone yet\n")
	} else {
		var ids []string
		for _, c := range cfg.FallbackSet.Candidates {
			ids = append(ids, c.ID)
		}
		fmt.Fprintf(tw, "  Fallbacks:\t%s\n", strings.Join(ids, ", "))
	}
	if on := cfg.Loop.Errors.FailoverOn; len(on) > 0 && cfg.FallbackSet != nil {
		fmt.Fprintf(tw, "  Handoff:\tautomatic on %s\n", strings.Join(on, ", "))
	} else {
		fmt.Fprintf(tw, "  Handoff:\toff\n")
	}
	tw.Flush()
}

func printFallbackAddPreview(w io.Writer, p *fallbackAddPlan) {
	c := p.candidate
	fmt.Fprintf(w, "Preview for loop %q (nothing written yet)\n\nNew fallback\n", p.cfg.Loop.Name)
	if p.bb != nil {
		printBBCandidate(w, p)
	} else {
		printHeadlessCandidate(w, c)
	}
	if len(p.warnings) > 0 {
		fmt.Fprintln(w)
	}
	for _, warning := range p.warnings {
		fallbackAddWrap(w, "  WARNING: ", "  ", warning)
	}

	fmt.Fprintln(w, "\nHandoff order")
	active := ""
	if st, err := state.Load(p.cfg.Loop.StateDir); err == nil {
		active = st.Target()
	}
	printHandoffOrder(w, p.cfg, p.order, active, map[string]string{c.ID: "<- new"})

	fmt.Fprintln(w, "\nRules")
	on := strings.Join(p.failoverOn, ", ")
	switch {
	case p.wasLive:
		fmt.Fprintf(w, "  Handoff:     automatic on %s (already on; unchanged)\n", on)
	case len(p.failoverOn) > 0:
		fmt.Fprintf(w, "  Handoff:     turned ON by this change, for: %s\n", on)
	case p.failoverWrite:
		fmt.Fprintln(w, "  Handoff:     OFF. The fallback is saved but never used automatically.")
		fmt.Fprintf(w, "               The loop's setting (%s) would start handoff with\n", p.failoverWas)
		fmt.Fprintln(w, "               the first fallback, so an explicit empty setting is written.")
	default:
		fmt.Fprintln(w, "  Handoff:     OFF. The fallback is saved but never used automatically.")
	}
	need := "at least 1 fallback that has passed its check"
	if n := p.rules.Redundancy.MinHealthy; n != 1 {
		need = fmt.Sprintf("at least %d fallbacks that have passed their checks", n) + map[string]string{"provider": ",\n               from different providers", "group": ",\n               on different quota groups"}[p.rules.Redundancy.Span]
	}
	fmt.Fprintf(w, "  Needs:       %s\n", need)
	if !fallbackAddReachable(p) {
		fmt.Fprintln(w, "               WARNING: these fallbacks cannot meet that; handoffs stay refused.")
	}
	policy := p.rules.Probe
	if policy.Disabled {
		fmt.Fprintln(w, "  Checks:      once before each handoff; no scheduled checks.")
		fmt.Fprintln(w, "               Each is a real model request.")
	} else {
		fmt.Fprintf(w, "  Checks:      every %s (at most %d a day per fallback) while the loop runs,\n", fallbackAddDuration(policy.Every.Duration), policy.DailyCap)
		fmt.Fprintln(w, "               and once before each handoff. Each is a real model request.")
	}

	fmt.Fprintln(w, "\nFiles")
	if p.createFile {
		fmt.Fprintf(w, "  create  %s\n", p.fallbacksPath)
	} else {
		fmt.Fprintf(w, "  update  %s\n", p.fallbacksPath)
	}
	if p.configYAML != nil {
		var keys []string
		if p.createFile {
			keys = append(keys, "loop.fallbacks")
		}
		if p.failoverWrite {
			keys = append(keys, "loop.errors.failover_on")
		}
		fmt.Fprintf(w, "  update  %s\n          (%s)\n", p.cfgPath, strings.Join(keys, ", "))
	}
	if len(p.notes) > 0 {
		fmt.Fprintln(w)
		for _, note := range p.notes {
			fallbackAddWrap(w, "Note: ", "", note)
		}
	}
	fmt.Fprintln(w, "\nThis command runs no agent and no model. It does not log in, create an")
	fmt.Fprintln(w, "account or copy credentials.")
}

// fallbackAddWrap prints text in lines of at most 78 columns.
func fallbackAddWrap(w io.Writer, first, indent, text string) {
	line := first
	for _, word := range strings.Fields(text) {
		if len(line)+len(word) > 78 && strings.TrimSpace(line) != "" {
			fmt.Fprintln(w, strings.TrimRight(line, " "))
			line = indent
		}
		line += word + " "
	}
	fmt.Fprintln(w, strings.TrimRight(line, " "))
}

func printHeadlessCandidate(w io.Writer, c config.Candidate) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  ID:\t%s\n", c.ID)
	fmt.Fprintf(tw, "  Agent:\t%s (provider %s)\n", c.Adapter, c.Provider)
	account := "the CLI's current login"
	for _, dir := range c.ProbeEnv() {
		account = dir
	}
	fmt.Fprintf(tw, "  Account:\t%s\n", account)
	fmt.Fprintf(tw, "  Login:\tnot checked; sign in to it yourself first\n")
	fmt.Fprintf(tw, "  Quota group:\t%s\n", c.Group)
	fmt.Fprintf(tw, "  Model:\t%s\n", fallbackAddModel(c.Model()))
	probeModel := "same as the model"
	if c.Probe.Model != "" {
		probeModel = c.Probe.Model
	}
	fmt.Fprintf(tw, "  Probe model:\t%s\n", probeModel)
	fmt.Fprintf(tw, "  Works in:\t%s\n", c.ProbeWorkdir())
	tw.Flush()
}

func fallbackAddDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return d.String()
}

func printFallbackAddNext(w io.Writer, p *fallbackAddPlan) {
	id, cfgPath, prefix := p.candidate.ID, commandQuote(p.cfgPath), initCommandPrefix()
	fmt.Fprintf(w, "\nAdded fallback %s at position %d of %d.\n\nNext steps\n", id, p.position, len(p.order))
	cont := lineContinuation()
	command := func(lines ...string) {
		fmt.Fprintf(w, "       %s fallbacks %s\n", prefix, strings.Join(append(lines, "-c "+cfgPath), " "+cont+"\n         "))
	}
	fmt.Fprintln(w, "  1. Preview the check. Free: no model call.")
	command("check", "--id "+id+" --exact --dry-run")
	if p.bb != nil {
		fmt.Fprintf(w, "  2. Check the local %s CLI's login and this model with one real\n", p.candidate.Probe.Kind)
		fmt.Fprintln(w, "     request. It uses that login's quota and may cost money. It does not")
		fmt.Fprintln(w, "     test the account BB uses for the thread.")
	} else {
		fmt.Fprintln(w, "  2. Confirm the login and model with one real request. It uses this")
		fmt.Fprintln(w, "     account's quota and may cost money.")
	}
	command("check", "--id "+id+" --exact")
	fmt.Fprintln(w, "  3. See recorded health and the handoff order.")
	command("list")
	fmt.Fprintln(w, "\nUntil a check passes, the fallback counts as not healthy and is not used.")
	if len(p.failoverOn) == 0 {
		fmt.Fprintln(w, "\nAutomatic handoff is still OFF. To turn it on:")
		fmt.Fprintf(w, "       %s fallbacks handoff on %s\n         -c %s\n", prefix, cont, cfgPath)
	}
	fmt.Fprintln(w, "\nA running daemon uses the new order after its next configuration reload.")
}

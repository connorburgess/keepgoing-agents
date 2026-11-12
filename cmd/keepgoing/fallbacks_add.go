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
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"gopkg.in/yaml.v3"
)

type fallbackAddOptions struct {
	cfgPath, agent, account, group, model, probeModel, id, workdir string
	position, probeCap                                             int
	enableHandoff, nonInteractive, dryRun, asJSON                  bool
}

// fallbackAddPlan is the resolved change. Nothing in it has been written.
type fallbackAddPlan struct {
	cfg           *config.Config
	cfgPath       string
	fallbacksPath string
	createFile    bool
	candidate     config.Candidate
	order         []config.Candidate // resulting handoff order, new candidate included
	position      int                // 1-based position of the new candidate
	rules         *config.Fallbacks  // redundancy and probe policy that will apply
	failoverOn    []string           // resulting errors.failover_on
	enableHandoff bool               // this command changes failover_on
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
	Limitations   []string             `json:"limitations"`
}

type fallbackAddCandidate struct {
	ID         string `json:"id"`
	Adapter    string `json:"adapter"`
	Provider   string `json:"provider"`
	Group      string `json:"group"`
	Model      string `json:"model"`      // empty: the CLI's configured default, unresolved
	Account    string `json:"account"`    // inherited or explicit_directory
	ProbeKind  string `json:"probeKind"`  // claude or codex
	ProbeModel string `json:"probeModel"` // scheduled probe model; empty uses the target model
	Position   int    `json:"position"`
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

type fallbackAddHandoff struct {
	Automatic  bool     `json:"automatic"`
	FailoverOn []string `json:"failoverOn"`
	Changed    bool     `json:"changed"`
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
  keepgoing fallbacks add -c keepgoing.yaml                 guided questions, preview, then confirm
  keepgoing fallbacks add -c keepgoing.yaml --non-interactive --agent codex [flags]

The command looks up installed agent CLIs without running them, previews the
candidate and the handoff order, and writes only after confirmation (or with
--non-interactive and no --dry-run). It never logs in, creates accounts, copies
credentials or calls a model.

Flags:
`

func fallbacksAddCommand(e env, args []string) int {
	return fallbacksAdd(e, args, os.Stdin, exec.LookPath)
}

// fallbacksAdd discovers executables, but never invokes an agent or reads its login.
func fallbacksAdd(e env, args []string, input io.Reader, lookPath func(string) (string, error)) int {
	var o fallbackAddOptions
	fs := flag.NewFlagSet("fallbacks add", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprint(e.stderr, fallbackAddUsage)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cfgPath, "c", "keepgoing.yaml", "path to the loop's config file")
	fs.StringVar(&o.agent, "agent", "", "claude or codex (headless); other agents are refused")
	fs.StringVar(&o.account, "account-dir", "", "existing account configuration directory (default: the CLI's current login)")
	fs.StringVar(&o.group, "group", "", "quota group label; accounts sharing limits must share it (default: account directory name, else provider)")
	fs.StringVar(&o.model, "model", "", "model for this fallback's turns (default: CLI configuration)")
	fs.StringVar(&o.probeModel, "probe-model", "", "cheaper model for scheduled health probes (default: the target model)")
	fs.StringVar(&o.id, "id", "", "candidate id: lowercase letters, digits, hyphens (default: derived from agent and group)")
	fs.StringVar(&o.workdir, "workdir", "", "project directory the fallback works in (default: the primary's)")
	fs.IntVar(&o.position, "position", 0, "1-based place in the handoff order (default: last)")
	fs.IntVar(&o.probeCap, "probe-daily-cap", 2, "scheduled probes per fallback per UTC day when a new fallbacks file is created")
	fs.BoolVar(&o.enableHandoff, "enable-handoff", false, "turn on automatic handoff for structured rate limits when it is off")
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
	if fs.NArg() != 0 {
		return fail(exitUsage, "unexpected positional arguments")
	}
	if o.asJSON && !o.nonInteractive {
		return fail(exitUsage, "--json requires --non-interactive")
	}
	if o.nonInteractive && o.agent == "" {
		return fail(exitUsage, "--non-interactive requires --agent")
	}
	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		e.errf("config: %v", err)
		return exitUsage
	}
	if cfg.Loop.Adapter == "cursor" {
		e.errf("fallbacks add: refused, nothing written.\n  A Cursor primary supports standalone loops only; fallbacks are unverified.")
		return exitUsage
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	w := &fallbackAddWizard{e: e, cfg: cfg, o: &o, visited: visited, lookPath: lookPath}
	if !o.nonInteractive {
		w.r = bufio.NewReader(input)
		if err := w.collect(); err != nil {
			e.errf("\nfallbacks add: stopped, nothing written.\n  %v", err)
			return exitUsage
		}
	}
	plan, err := planFallbackAdd(cfg, o, lookPath)
	if err != nil {
		e.errf("fallbacks add: refused, nothing written.\n  %v", err)
		return exitUsage
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
		answer, err := w.ask("Write this fallback? (yes/no)", "no")
		if err != nil {
			return fail(exitUsage, "%v", err)
		}
		if a := strings.ToLower(answer); a != "yes" && a != "y" {
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
	return exitOK
}

type fallbackAddWizard struct {
	e        env
	cfg      *config.Config
	o        *fallbackAddOptions
	r        *bufio.Reader
	visited  map[string]bool
	lookPath func(string) (string, error)
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
		return "", fmt.Errorf("input ended; nothing written (use --non-interactive for scripts): %w", err)
	}
	if s = strings.TrimSpace(s); s == "" {
		s = def
	}
	return s, nil
}

func (w *fallbackAddWizard) str(flagName, label string, value *string, def string) error {
	if w.visited[flagName] {
		return nil
	}
	s, err := w.ask(label, def)
	*value = s
	return err
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
		{"bb", "manual setup only"},
		{"cursor", "cannot be a fallback yet"},
	} {
		p, err := w.lookPath(initCLIName(a.kind))
		if err != nil {
			fmt.Fprintf(tw, "  %s\tnot found\t\n", a.kind)
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", a.kind, p, a.note)
		if a.kind == "claude" || a.kind == "codex" {
			usable = append(usable, a.kind)
		}
	}
	tw.Flush()
	if len(usable) == 0 {
		return errors.New("no claude or codex CLI found on PATH; install and sign in to one first")
	}

	def := usable[0]
	for _, a := range usable { // prefer a provider other than the primary's
		if a != cfg.Loop.Adapter {
			def = a
			break
		}
	}
	fmt.Fprintln(out, "\nStep 1 of 4: Provider")
	if err := w.str("agent", "Which agent takes over? ("+strings.Join(usable, "/")+")", &o.agent, def); err != nil {
		return err
	}
	if err := fallbackAddAgentSupported(o.agent); err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 2 of 4: Account")
	fmt.Fprintln(out, "  Give a directory you have already signed in to, or leave blank to use")
	fmt.Fprintln(out, "  the CLI's current login. Nothing is created or copied.")
	if err := w.str("account-dir", "Account directory", &o.account, ""); err != nil {
		return err
	}
	fmt.Fprintln(out, "  A quota group names one usage limit. Accounts that share a limit must share")
	fmt.Fprintf(out, "  a group; a fallback must not use the primary's group (%s).\n", cfg.Loop.Group)
	groupDef := fallbackAddDefaultGroup(o.agent, o.account)
	if err := w.str("group", "Quota group", &o.group, groupDef); err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 3 of 4: Model")
	fmt.Fprintln(out, "  Leave blank to use the CLI's configured model. Names are not checked here.")
	if err := w.str("model", "Model", &o.model, ""); err != nil {
		return err
	}
	fmt.Fprintln(out, "  Scheduled health probes can use a cheaper model; blank uses the same one.")
	if err := w.str("probe-model", "Probe model", &o.probeModel, ""); err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 4 of 4: Handoff")
	if n := fallbackAddExisting(cfg); n > 0 && !w.visited["position"] {
		s, err := w.ask(fmt.Sprintf("Place in the handoff order (1 = tried first, %d = last)", n+1), strconv.Itoa(n+1))
		if err != nil {
			return err
		}
		if o.position, err = strconv.Atoi(s); err != nil {
			return errors.New("position must be a number")
		}
	}
	if len(cfg.Loop.Errors.FailoverOn) > 0 {
		fmt.Fprintf(out, "  Automatic handoff is already on for: %s.\n", strings.Join(cfg.Loop.Errors.FailoverOn, ", "))
	} else if w.visited["enable-handoff"] {
		fmt.Fprintln(out, "  Automatic handoff follows --enable-handoff.")
	} else {
		fmt.Fprintln(out, "  Automatic handoff is off for this loop. Turning it on lets keepgoing move")
		fmt.Fprintln(out, "  the loop to a fallback when the active agent reports a rate limit.")
		s, err := w.ask("Turn on automatic handoff for rate limits? (yes/no)", "no")
		if err != nil {
			return err
		}
		switch strings.ToLower(s) {
		case "yes", "y":
			o.enableHandoff = true
		case "no", "n":
		default:
			return errors.New("handoff choice must be yes or no")
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
	if agent == "claude" {
		return "claude-code"
	}
	return agent
}

func fallbackAddAgentSupported(agent string) error {
	switch agent {
	case "claude", "codex":
		return nil
	case "cursor":
		return errors.New("Cursor cannot be a fallback yet: its account and model probes are unverified.")
	case "bb", "process":
		return fmt.Errorf("Guided add supports headless claude and codex only.\n  A %s candidate still needs a hand-written entry; see docs/fallbacks.md.", agent)
	case "":
		return errors.New("choose an agent: claude or codex")
	}
	return fmt.Errorf("unknown agent %q; choose claude or codex", agent)
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
	for name, v := range map[string]string{"model": o.model, "probe-model": o.probeModel} {
		if strings.HasPrefix(v, "-") || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return nil, fmt.Errorf("%s must be a single model name without spaces or a leading hyphen", name)
		}
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
			return nil, errors.New("the loop names no project directory; pass --workdir")
		}
	}
	workdir, err := existingDirectory(workdir)
	if err != nil {
		return nil, fmt.Errorf("project directory: %w", err)
	}
	if strings.TrimSpace(o.group) == "" {
		o.group = fallbackAddDefaultGroup(o.agent, o.account)
	}
	agent, err := initAgent(o.agent, o.account, o.group, o.model, workdir, lookPath)
	if err != nil {
		return nil, err
	}
	if agent.Group == cfg.Loop.Group {
		return nil, fmt.Errorf("Quota group %q is the primary's. A fallback on the same\n  limit cannot take over: choose an independent account and its own group.", agent.Group)
	}
	cfgPath, err := filepath.Abs(o.cfgPath)
	if err != nil {
		return nil, err
	}
	p := &fallbackAddPlan{cfg: cfg, cfgPath: cfgPath, fallbacksPath: cfg.Loop.Fallbacks, failoverOn: append([]string{}, cfg.Loop.Errors.FailoverOn...)}
	var existing []config.Candidate
	if cfg.FallbackSet != nil {
		existing = cfg.FallbackSet.Candidates
		p.rules = cfg.FallbackSet
	} else {
		p.createFile = true
		p.fallbacksPath = filepath.Join(filepath.Dir(p.cfgPath), "fallbacks.yaml")
		if _, err := os.Lstat(p.fallbacksPath); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s already exists but the loop does not use it; set loop.fallbacks or move the file", p.fallbacksPath)
		}
		if o.probeCap < 1 {
			return nil, errors.New("probe-daily-cap must be positive")
		}
	}
	ids := map[string]bool{"primary": true}
	for _, c := range existing {
		ids[c.ID] = true
		if c.Group == agent.Group {
			return nil, fmt.Errorf("Quota group %q is already used by fallback %q.\n  Candidates sharing a limit cannot back each other up: choose an\n  independent account and its own group.", agent.Group, c.ID)
		}
	}
	id := o.id
	if id == "" {
		base := fallbackAddSlug(o.agent + "-" + agent.Group)
		if agent.Group == agent.Provider || strings.HasPrefix(fallbackAddSlug(agent.Group), o.agent) {
			base = fallbackAddSlug(agent.Group)
		}
		if len(base) > 60 {
			base = strings.Trim(base[:60], "-")
		}
		id = base
		for n := 2; ids[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
	} else if ids[id] {
		return nil, fmt.Errorf("id %q is already in use", id)
	}
	p.candidate = config.Candidate{ID: id, Agent: agent, Probe: config.Probe{Kind: o.agent, Model: o.probeModel}}
	p.position = o.position
	if p.position == 0 {
		p.position = len(existing) + 1
	}
	if p.position < 1 || p.position > len(existing)+1 {
		return nil, fmt.Errorf("position must be between 1 and %d", len(existing)+1)
	}
	p.order = append(append(append([]config.Candidate{}, existing[:p.position-1]...), p.candidate), existing[p.position-1:]...)
	if o.enableHandoff && len(p.failoverOn) == 0 {
		p.enableHandoff = true
		p.failoverOn = []string{"rate_limit"}
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
			return nil, errors.New("fallbacks file is unreadable")
		}
		if p.fallbacksYAML, err = insertCandidateYAML(raw, p.candidate, p.position-1); err != nil {
			return nil, err
		}
	}
	parsed, err := config.ParseFallbacks(p.fallbacksYAML)
	if err != nil {
		return nil, fmt.Errorf("resulting fallbacks file would be invalid: %w", err)
	}
	if len(parsed.Candidates) != len(p.order) || parsed.Candidates[p.position-1].ID != id {
		return nil, errors.New("could not place the candidate in the existing fallbacks file; edit it by hand")
	}
	p.rules = parsed
	if p.createFile || p.enableHandoff {
		raw, err := os.ReadFile(p.cfgPath)
		if err != nil {
			return nil, errors.New("configuration is unreadable")
		}
		if p.configYAML, err = editLoopYAML(raw, p.createFile, p.enableHandoff); err != nil {
			return nil, err
		}
		next, err := config.Parse(p.configYAML)
		if err != nil {
			return nil, fmt.Errorf("resulting configuration would be invalid: %w", err)
		}
		if p.createFile && next.Loop.Fallbacks != "fallbacks.yaml" || p.enableHandoff && strings.Join(next.Loop.Errors.FailoverOn, ",") != "rate_limit" {
			return nil, errors.New("could not update keepgoing.yaml safely; edit it by hand")
		}
	}
	return p, nil
}

// insertCandidateYAML edits the document tree so existing comments and
// candidates survive. Indentation may be normalized.
func insertCandidateYAML(raw []byte, c config.Candidate, at int) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, errors.New("fallbacks file is not valid YAML")
	}
	list := yamlMapValue(yamlRoot(&doc), "candidates")
	if list == nil || list.Kind != yaml.SequenceNode || at > len(list.Content) {
		return nil, errors.New("fallbacks file has no editable candidates list; edit it by hand")
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
func editLoopYAML(raw []byte, setFallbacks, enableHandoff bool) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, errors.New("configuration is not valid YAML")
	}
	loop := yamlMapValue(yamlRoot(&doc), "loop")
	if loop == nil || loop.Kind != yaml.MappingNode {
		return nil, errors.New("configuration has no editable loop section; edit it by hand")
	}
	if setFallbacks {
		yamlMapSet(loop, "fallbacks", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fallbacks.yaml"})
	}
	if enableHandoff {
		errs := yamlMapValue(loop, "errors")
		if errs == nil {
			errs = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			yamlMapSet(loop, "errors", errs)
		}
		if errs.Kind != yaml.MappingNode {
			return nil, errors.New("loop.errors is not editable; edit it by hand")
		}
		yamlMapSet(errs, "failover_on", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle,
			Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "rate_limit"}}})
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

// writeFallbackAdd holds the tick lock, so supervision never loads a
// half-applied change, and refuses when the reviewed sources changed.
func writeFallbackAdd(p *fallbackAddPlan) (int, error) {
	l, err := lock.Acquire(p.cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		return exitSkipped, errors.New("a check is running; nothing written. Try again in a moment")
	}
	if err != nil {
		return exitFailed, fmt.Errorf("lock: %w", err)
	}
	defer l.Release()
	if err := p.cfg.CheckSources(); err != nil {
		return exitSkipped, fmt.Errorf("%w since the preview; nothing written. Run the command again", err)
	}
	if p.createFile {
		f, err := os.OpenFile(p.fallbacksPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return exitFailed, errors.New("could not create the fallbacks file; nothing written")
		}
		_, err = f.Write(p.fallbacksYAML)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = replaceFile(p.cfgPath, p.configYAML)
		}
		if err != nil {
			_ = os.Remove(p.fallbacksPath)
			return exitFailed, errors.New("could not update keepgoing.yaml; nothing written")
		}
		return exitOK, nil
	}
	if err := replaceFile(p.fallbacksPath, p.fallbacksYAML); err != nil {
		return exitFailed, errors.New("could not update the fallbacks file; nothing written")
	}
	if p.configYAML != nil {
		if err := replaceFile(p.cfgPath, p.configYAML); err != nil {
			return exitFailed, errors.New("the fallback was added, but automatic handoff could not be turned on in keepgoing.yaml")
		}
	}
	return exitOK, nil
}

// replaceFile swaps in new contents through a private sibling file, keeping
// the original's permission bits. A symlinked source is resolved first.
func replaceFile(path string, data []byte) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".fallbacks-add-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, info.Mode().Perm())
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
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
	c := p.candidate
	r := fallbackAddReport{SchemaVersion: 1, Kind: "fallback_add", Outcome: "added", Loop: p.cfg.Loop.Name,
		Candidate: fallbackAddCandidate{ID: c.ID, Adapter: c.Adapter, Provider: c.Provider, Group: c.Group, Model: c.Model(),
			Account: initTargetOf(c.Agent).Account, ProbeKind: c.Probe.Kind, ProbeModel: c.Probe.Model, Position: p.position},
		Primary:    fallbackAddTarget{ID: "primary", Provider: p.cfg.Loop.Provider, Group: p.cfg.Loop.Group, Model: p.cfg.Loop.Agent.Model()},
		Handoff:    fallbackAddHandoff{Automatic: len(p.failoverOn) > 0, FailoverOn: p.failoverOn, Changed: p.enableHandoff},
		Redundancy: fallbackAddRule{MinHealthy: p.rules.Redundancy.MinHealthy, Span: p.rules.Redundancy.Span, Reachable: fallbackAddReachable(p)},
		Files:      fallbackAddFiles{FallbacksPath: p.fallbacksPath, FallbacksCreated: p.createFile, ConfigPath: p.cfgPath, ConfigChanged: p.configYAML != nil},
		Next: fallbackAddNext{
			PreviewProbe: fallbackAddArgv(p, "check", "--id", c.ID, "--exact", "--dry-run"),
			ExactProbe:   fallbackAddArgv(p, "check", "--id", c.ID, "--exact"),
			List:         fallbackAddArgv(p, "list"),
		},
		Limitations: fallbackAddLimitations,
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

func printFallbackAddCurrent(w io.Writer, cfg *config.Config) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  Primary:\t%s, quota group %s, model %s\n", cfg.Loop.Provider, cfg.Loop.Group, fallbackAddModel(cfg.Loop.Agent.Model()))
	if n := fallbackAddExisting(cfg); n == 0 {
		fmt.Fprintf(tw, "  Fallbacks:\tnone yet\n")
	} else {
		var ids []string
		for _, c := range cfg.FallbackSet.Candidates {
			ids = append(ids, c.ID)
		}
		fmt.Fprintf(tw, "  Fallbacks:\t%s\n", strings.Join(ids, ", "))
	}
	if on := cfg.Loop.Errors.FailoverOn; len(on) > 0 {
		fmt.Fprintf(tw, "  Handoff:\tautomatic on %s\n", strings.Join(on, ", "))
	} else {
		fmt.Fprintf(tw, "  Handoff:\toff\n")
	}
	tw.Flush()
}

func printFallbackAddPreview(w io.Writer, p *fallbackAddPlan) {
	c := p.candidate
	fmt.Fprintf(w, "Preview for loop %q (nothing written yet)\n\nNew fallback\n", p.cfg.Loop.Name)
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
	fmt.Fprintf(tw, "  Probe model:\t%s\n", fallbackAddModel(c.ProbeModel(false)))
	fmt.Fprintf(tw, "  Works in:\t%s\n", c.ProbeWorkdir())
	tw.Flush()

	fmt.Fprintln(w, "\nHandoff order")
	tw = tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  \tID\tPROVIDER\tGROUP\tMODEL\t")
	fmt.Fprintf(tw, "  now\tprimary\t%s\t%s\t%s\t\n", p.cfg.Loop.Provider, p.cfg.Loop.Group, fallbackAddModel(p.cfg.Loop.Agent.Model()))
	for i, o := range p.order {
		note := ""
		switch {
		case o.ID == c.ID:
			note = "<- new"
		case o.Disabled:
			note = "disabled"
		}
		fmt.Fprintf(tw, "  %d.\t%s\t%s\t%s\t%s\t%s\n", i+1, o.ID, o.Provider, o.Group, fallbackAddModel(o.Model()), note)
	}
	tw.Flush()

	fmt.Fprintln(w, "\nRules")
	switch {
	case p.enableHandoff:
		fmt.Fprintln(w, "  Handoff:     turned ON by this change, for structured rate limits only")
	case len(p.failoverOn) > 0:
		fmt.Fprintf(w, "  Handoff:     automatic on %s (unchanged)\n", strings.Join(p.failoverOn, ", "))
	default:
		fmt.Fprintln(w, "  Handoff:     OFF. The fallback is saved but never used automatically.")
	}
	span := map[string]string{"provider": "counted by provider (accounts of one provider count once)", "group": "counted by quota group", "none": "each candidate counts"}[p.rules.Redundancy.Span]
	fmt.Fprintf(w, "  Redundancy:  needs %d healthy, %s\n", p.rules.Redundancy.MinHealthy, span)
	if !fallbackAddReachable(p) {
		fmt.Fprintln(w, "               WARNING: these candidates cannot meet that rule; handoffs stay refused.")
	}
	policy := p.rules.Probe
	if policy.Disabled {
		fmt.Fprintln(w, "  Probes:      scheduled probes off; one exact probe before each handoff")
	} else {
		fmt.Fprintf(w, "  Probes:      while the loop runs, every %s and at most %d a day per\n", fallbackAddDuration(policy.Every.Duration), policy.DailyCap)
		fmt.Fprintln(w, "               fallback, plus one exact probe before each handoff.")
		fmt.Fprintln(w, "               Every probe is a real model request.")
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
		if p.enableHandoff {
			keys = append(keys, "errors.failover_on")
		}
		fmt.Fprintf(w, "  update  %s\n          (%s)\n", p.cfgPath, strings.Join(keys, ", "))
	}
	fmt.Fprintln(w, "\nThis command runs no agent and no model. It does not log in, create an")
	fmt.Fprintln(w, "account or copy credentials.")
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
	cont := "\\"
	if runtime.GOOS == "windows" {
		cont = "`"
	}
	command := func(lines ...string) {
		fmt.Fprintf(w, "       %s fallbacks %s\n", prefix, strings.Join(append(lines, "-c "+cfgPath), " "+cont+"\n         "))
	}
	fmt.Fprintln(w, "  1. Preview the check. Free: no model call.")
	command("check", "--id "+id+" --exact --dry-run")
	fmt.Fprintln(w, "  2. Confirm the login and model with one real request. It uses this")
	fmt.Fprintln(w, "     account's quota and may cost money.")
	command("check", "--id "+id+" --exact")
	fmt.Fprintln(w, "  3. See recorded health and the handoff order.")
	command("list")
	fmt.Fprintln(w, "\nUntil a check passes, the fallback counts as not healthy and is not used.")
	if len(p.failoverOn) == 0 {
		fmt.Fprintln(w, "\nAutomatic handoff is still OFF. To turn it on, set this in keepgoing.yaml:")
		fmt.Fprintln(w, "  errors:\n    failover_on: [rate_limit]")
	}
	fmt.Fprintln(w, "\nA running daemon uses the new order after its next configuration reload.")
}

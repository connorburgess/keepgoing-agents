package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"gopkg.in/yaml.v3"
)

// fallbackEdit is one resolved change to a loop's fallback setup. Nothing in
// it has been written. A nil YAML means that file is left untouched.
type fallbackEdit struct {
	kind          string // fallback_handoff, fallback_remove or fallback_move
	cfg           *config.Config
	cfgPath       string
	fallbacksYAML []byte
	configYAML    []byte
	dropHealth    string // candidate id whose recorded health is deleted
	hadHealth     bool
	order         []config.Candidate // resulting handoff order
	rules         config.Redundancy
	active        string // current target id
	failoverOn    []string
	wasLive       bool
	unchanged     string // set when there is nothing to do, with the reason
	subject       string // candidate id for remove and move
	from, to      int    // 1-based positions; to is 0 for remove
	confirm       string // the yes/no question
	done          string // printed after a successful write
}

// Success-only integration contract shared by the three commands.
type fallbackEditReport struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Kind          string              `json:"kind"`
	Outcome       string              `json:"outcome"` // preview, changed or unchanged
	Loop          string              `json:"loop"`
	Candidate     string              `json:"candidate,omitempty"`
	From          int                 `json:"from,omitempty"`
	To            int                 `json:"to,omitempty"`
	Active        string              `json:"activeTarget"`
	Order         []fallbackAddTarget `json:"order"`
	Handoff       fallbackAddHandoff  `json:"handoff"`
	Redundancy    fallbackAddRule     `json:"redundancy"`
	Health        string              `json:"health,omitempty"` // deleted, none_recorded or kept
	Files         []string            `json:"files"`
	Reason        string              `json:"reason,omitempty"`
}

const fallbackManageUsage = `Change an existing loop's fallbacks without editing YAML.

Usage:
  keepgoing fallbacks handoff on|off -c keepgoing.yaml    turn automatic handoff for rate limits on or off
  keepgoing fallbacks remove ID -c keepgoing.yaml         take a fallback out of the loop
  keepgoing fallbacks move ID --to N -c keepgoing.yaml    change its place in the handoff order

Each command previews the result and writes only after you answer yes, or
with --yes. --dry-run previews only. Nothing is run: no agent, model or check.

Flags:
`

func fallbacksManageCommand(e env, sub string, args []string) int {
	return fallbacksManage(e, sub, args, os.Stdin)
}

func fallbacksManage(e env, sub string, args []string, input io.Reader) int {
	positional, rest := splitPositional(args)
	fs := flag.NewFlagSet("fallbacks "+sub, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprint(e.stderr, fallbackManageUsage)
		fs.PrintDefaults()
	}
	cfgPath := fs.String("c", "keepgoing.yaml", "path to the loop's config file")
	yes := fs.Bool("yes", false, "write without asking")
	dryRun := fs.Bool("dry-run", false, "preview only; write nothing")
	asJSON := fs.Bool("json", false, "print a versioned result; requires --yes or --dry-run")
	to := fs.Int("to", 0, "move: the new 1-based place in the handoff order")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	positional = append(positional, fs.Args()...)
	usage := map[string]string{
		"handoff": "usage: keepgoing fallbacks handoff on|off [-c keepgoing.yaml]",
		"remove":  "usage: keepgoing fallbacks remove ID [-c keepgoing.yaml]",
		"move":    "usage: keepgoing fallbacks move ID --to N [-c keepgoing.yaml]",
	}[sub]
	if len(positional) != 1 || (sub == "handoff" && positional[0] != "on" && positional[0] != "off") {
		e.errf("%s", usage)
		return exitUsage
	}
	if sub != "move" && *to != 0 {
		e.errf("fallbacks %s: --to applies only to fallbacks move", sub)
		return exitUsage
	}
	if *asJSON && !*yes && !*dryRun {
		e.errf("fallbacks %s: --json requires --yes or --dry-run", sub)
		return exitUsage
	}
	refuse := func(code int, err error) int {
		e.errf("fallbacks %s: refused, nothing written.\n  %v", sub, err)
		return code
	}
	if info, err := os.Lstat(*cfgPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return refuse(exitUsage, errors.New("The config path is a symbolic link. Pass the real keepgoing.yaml instead."))
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		e.errf("config: %v", err)
		return exitUsage
	}
	if cfg.FallbackSet == nil {
		return refuse(exitUsage, errors.New("This loop has no fallbacks. Add one first with: keepgoing fallbacks add"))
	}
	if info, err := os.Lstat(cfg.Loop.Fallbacks); err != nil || !info.Mode().IsRegular() {
		return refuse(exitUsage, errors.New("The loop's fallbacks file is a symbolic link or not a regular file.\n  Point loop.fallbacks at the real file instead."))
	}
	// Advisory: the same checks run again under the tick lock before writing.
	if code, err := fallbackAddAllowed(cfg); err != nil {
		return refuse(code, err)
	}
	var edit *fallbackEdit
	switch sub {
	case "handoff":
		edit, err = planFallbackHandoff(cfg, positional[0] == "on")
	case "remove":
		edit, err = planFallbackRemove(cfg, positional[0])
	default:
		edit, err = planFallbackMove(cfg, positional[0], *to)
	}
	if err != nil {
		return refuse(exitUsage, err)
	}
	report := func(outcome string) int {
		if err := json.NewEncoder(e.stdout).Encode(fallbackEditReportOf(edit, outcome)); err != nil {
			e.errf("fallbacks %s: could not write JSON result; files may already be changed", sub)
			return exitFailed
		}
		return exitOK
	}
	if edit.unchanged != "" {
		if *asJSON {
			return report("unchanged")
		}
		fmt.Fprintf(e.stdout, "%s Nothing was changed.\n", edit.unchanged)
		return exitOK
	}
	if *asJSON {
		if *dryRun {
			return report("preview")
		}
		if code, err := writeFallbackEdit(edit); err != nil {
			e.errf("fallbacks %s: %v", sub, err)
			return code
		}
		return report("changed")
	}
	printFallbackEdit(e.stdout, edit)
	if *dryRun {
		fmt.Fprintln(e.stdout, "\nDry run: nothing written.")
		return exitOK
	}
	if !*yes {
		fmt.Fprintln(e.stdout)
		w := &fallbackAddWizard{e: e, r: bufio.NewReader(input)}
		ok, err := w.askYesNo(edit.confirm+" (yes/no)", "no")
		if err != nil {
			e.errf("fallbacks %s: %v; nothing written", sub, err)
			return exitUsage
		}
		if !ok {
			fmt.Fprintln(e.stdout, "\nCancelled: nothing written.")
			return exitOK
		}
	}
	if err := e.ctx.Err(); err != nil {
		e.errf("fallbacks %s: %v", sub, err)
		return exitFailed
	}
	if code, err := writeFallbackEdit(edit); err != nil {
		e.errf("fallbacks %s: %v", sub, err)
		return code
	}
	fmt.Fprintf(e.stdout, "\n%s\nA running daemon uses the change after its next configuration reload.\n", edit.done)
	return exitOK
}

func newFallbackEdit(kind string, cfg *config.Config) (*fallbackEdit, error) {
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return nil, errors.New("The loop's saved state is unreadable, so its current target is unknown.")
	}
	on := cfg.Loop.Errors.FailoverOn
	return &fallbackEdit{kind: kind, cfg: cfg, cfgPath: cfg.Path(), order: cfg.FallbackSet.Candidates,
		rules: cfg.FallbackSet.Redundancy, active: st.Target(), failoverOn: append([]string{}, on...), wasLive: len(on) > 0}, nil
}

func planFallbackHandoff(cfg *config.Config, on bool) (*fallbackEdit, error) {
	p, err := newFallbackEdit("fallback_handoff", cfg)
	if err != nil {
		return nil, err
	}
	switch {
	case on && p.wasLive:
		p.unchanged = fmt.Sprintf("Automatic handoff is already on for: %s.", strings.Join(p.failoverOn, ", "))
		return p, nil
	case !on && !p.wasLive:
		p.unchanged = "Automatic handoff is already off."
		return p, nil
	}
	p.failoverOn = []string{}
	p.confirm, p.done = "Turn automatic handoff off?", "Automatic handoff is off."
	if on {
		p.failoverOn = []string{"rate_limit"}
		p.confirm, p.done = "Turn automatic handoff on?", "Automatic handoff is on for rate limits."
	}
	raw, err := os.ReadFile(p.cfgPath)
	if err != nil {
		return nil, errors.New("The configuration is unreadable.")
	}
	if p.configYAML, err = editLoopYAML(raw, false, &p.failoverOn); err != nil {
		return nil, err
	}
	next, err := config.Parse(p.configYAML)
	if err != nil || strings.Join(next.Loop.Errors.FailoverOn, ",") != strings.Join(p.failoverOn, ",") {
		return nil, errors.New("Could not update keepgoing.yaml safely; edit it by hand.")
	}
	return p, nil
}

func fallbackEditFind(cfg *config.Config, id string) (int, error) {
	for i, c := range cfg.FallbackSet.Candidates {
		if c.ID == id {
			return i, nil
		}
	}
	var ids []string
	for _, c := range cfg.FallbackSet.Candidates {
		ids = append(ids, c.ID)
	}
	return 0, fmt.Errorf("No fallback has the id %q. This loop has: %s.", bbLabel(id, 40), strings.Join(ids, ", "))
}

func planFallbackRemove(cfg *config.Config, id string) (*fallbackEdit, error) {
	p, err := newFallbackEdit("fallback_remove", cfg)
	if err != nil {
		return nil, err
	}
	at, err := fallbackEditFind(cfg, id)
	if err != nil {
		return nil, err
	}
	if p.active == id {
		return nil, fmt.Errorf("The loop is running on %q right now. Move it first with:\n    keepgoing retarget primary %s\n      -c %s", id, lineContinuation(), commandQuote(p.cfgPath))
	}
	if len(p.order) == 1 {
		return nil, fmt.Errorf("%q is this loop's only fallback, and a fallbacks file cannot be empty.\n  To stop automatic handoff, use: keepgoing fallbacks handoff off", id)
	}
	p.subject, p.from = id, at+1
	p.order = append(append([]config.Candidate{}, p.order[:at]...), p.order[at+1:]...)
	p.dropHealth = id
	if reg, err := health.Load(cfg.Loop.StateDir); err != nil {
		return nil, errors.New("The loop's recorded health is unreadable; nothing can be cleared safely.")
	} else if _, ok := reg.Candidates[id]; ok {
		p.hadHealth = true
	}
	p.confirm, p.done = "Remove this fallback?", fmt.Sprintf("Removed fallback %s.", id)
	return p, p.rewriteCandidates(func(list *yaml.Node) {
		list.Content = append(list.Content[:at], list.Content[at+1:]...)
	})
}

func planFallbackMove(cfg *config.Config, id string, to int) (*fallbackEdit, error) {
	p, err := newFallbackEdit("fallback_move", cfg)
	if err != nil {
		return nil, err
	}
	at, err := fallbackEditFind(cfg, id)
	if err != nil {
		return nil, err
	}
	if to < 1 || to > len(p.order) {
		return nil, fmt.Errorf("--to must be a place between 1 and %d.", len(p.order))
	}
	p.subject, p.from, p.to = id, at+1, to
	if to == at+1 {
		p.unchanged = fmt.Sprintf("Fallback %s is already at place %d.", id, to)
		return p, nil
	}
	moved := p.order[at]
	rest := append(append([]config.Candidate{}, p.order[:at]...), p.order[at+1:]...)
	p.order = append(append(append([]config.Candidate{}, rest[:to-1]...), moved), rest[to-1:]...)
	p.confirm, p.done = "Move this fallback?", fmt.Sprintf("Moved fallback %s to place %d of %d.", id, to, len(p.order))
	return p, p.rewriteCandidates(func(list *yaml.Node) {
		node := list.Content[at]
		content := append(append([]*yaml.Node{}, list.Content[:at]...), list.Content[at+1:]...)
		list.Content = append(append(append([]*yaml.Node{}, content[:to-1]...), node), content[to-1:]...)
	})
}

// rewriteCandidates edits the candidates list in the document tree, so each
// entry keeps its own comments, and checks that the result is the planned order.
func (p *fallbackEdit) rewriteCandidates(change func(list *yaml.Node)) error {
	raw, err := os.ReadFile(p.cfg.Loop.Fallbacks)
	if err != nil {
		return errors.New("The fallbacks file is unreadable.")
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil {
		return errors.New("The fallbacks file is not valid YAML.")
	}
	list := yamlMapValue(yamlRoot(&doc), "candidates")
	if list == nil || list.Kind != yaml.SequenceNode || len(list.Content) != len(p.cfg.FallbackSet.Candidates) {
		return errors.New("The fallbacks file has no editable candidates list; edit it by hand.")
	}
	change(list)
	if p.fallbacksYAML, err = yaml.Marshal(&doc); err != nil {
		return err
	}
	parsed, err := config.ParseFallbacks(p.fallbacksYAML)
	if err != nil || len(parsed.Candidates) != len(p.order) {
		return errors.New("Could not rewrite the fallbacks file safely; edit it by hand.")
	}
	for i, c := range parsed.Candidates {
		if c.ID != p.order[i].ID {
			return errors.New("Could not rewrite the fallbacks file safely; edit it by hand.")
		}
	}
	return nil
}

// writeFallbackEdit applies the change under the tick lock with the same
// guards as fallbacks add. The target is re-read there: the loop may have
// handed off since the preview.
func writeFallbackEdit(p *fallbackEdit) (int, error) {
	l, err := lock.Acquire(p.cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		return exitSkipped, errors.New("a check is running; nothing written. Try again in a moment")
	}
	if err != nil {
		return exitFailed, fmt.Errorf("lock: %w", err)
	}
	defer l.Release()
	if code, err := fallbackAddAllowed(p.cfg); err != nil {
		return code, fmt.Errorf("%w; nothing written", err)
	}
	if p.kind == "fallback_remove" {
		st, err := state.Load(p.cfg.Loop.StateDir)
		if err != nil || st.Target() == p.subject {
			return exitSkipped, errors.New("the loop moved to this fallback since the preview; nothing written")
		}
	}
	if p.fallbacksYAML != nil {
		if err := fallbackAddReplace(p.cfg.Loop.Fallbacks, p.fallbacksYAML); err != nil {
			return exitFailed, errors.New("could not update the fallbacks file; nothing written")
		}
	}
	if p.configYAML != nil {
		if err := fallbackAddReplace(p.cfgPath, p.configYAML); err != nil {
			return exitFailed, errors.New("could not update keepgoing.yaml; nothing written")
		}
	}
	if p.dropHealth != "" && p.hadHealth {
		reg, err := health.Load(p.cfg.Loop.StateDir)
		if err == nil {
			delete(reg.Candidates, p.dropHealth)
			err = reg.Save(p.cfg.Loop.StateDir)
		}
		if err != nil {
			return exitFailed, fmt.Errorf("the fallback was removed, but its recorded health could not be cleared; do not reuse the id %s until it is", p.dropHealth)
		}
	}
	return exitOK, nil
}

func fallbackEditReachable(p *fallbackEdit) bool {
	return fallbackAddReachable(&fallbackAddPlan{cfg: p.cfg, order: p.order, rules: &config.Fallbacks{Redundancy: p.rules}})
}

func (p *fallbackEdit) healthNote() string {
	switch {
	case p.kind != "fallback_remove":
		return ""
	case p.hadHealth:
		return "deleted"
	}
	return "none_recorded"
}

func fallbackEditReportOf(p *fallbackEdit, outcome string) fallbackEditReport {
	live := len(p.failoverOn) > 0
	r := fallbackEditReport{SchemaVersion: 1, Kind: p.kind, Outcome: outcome, Loop: p.cfg.Loop.Name, Candidate: p.subject, From: p.from, To: p.to,
		Active:     p.active,
		Handoff:    fallbackAddHandoff{Automatic: live, WasAutomatic: p.wasLive, Changed: live != p.wasLive, FailoverOn: p.failoverOn, FailoverOnWritten: p.configYAML != nil},
		Redundancy: fallbackAddRule{MinHealthy: p.rules.MinHealthy, Span: p.rules.Span, Reachable: fallbackEditReachable(p)},
		Health:     p.healthNote(), Files: []string{}, Reason: p.unchanged}
	if p.kind == "fallback_move" {
		r.Health = "kept"
	}
	for i, o := range p.order {
		r.Order = append(r.Order, fallbackAddTarget{Position: i + 1, ID: o.ID, Provider: o.Provider, Group: o.Group, Model: o.Model(), Disabled: o.Disabled})
	}
	if outcome != "unchanged" {
		r.Files = p.files()
	}
	return r
}

func (p *fallbackEdit) files() []string {
	var out []string
	if p.fallbacksYAML != nil {
		out = append(out, p.cfg.Loop.Fallbacks)
	}
	if p.configYAML != nil {
		out = append(out, p.cfgPath)
	}
	if p.dropHealth != "" && p.hadHealth {
		out = append(out, filepath.Join(p.cfg.Loop.StateDir, health.FileName))
	}
	return out
}

// printHandoffOrder is the order table shared by add, remove and move.
func printHandoffOrder(w io.Writer, cfg *config.Config, order []config.Candidate, active string, notes map[string]string) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  \tID\tPROVIDER\tGROUP\tMODEL\t")
	mark := func(id, note string) string {
		if id == active && active != "" {
			return strings.TrimSpace("in use now " + note)
		}
		return note
	}
	fmt.Fprintf(tw, "  \tprimary\t%s\t%s\t%s\t%s\n", cfg.Loop.Provider, cfg.Loop.Group, fallbackAddAgentModel(cfg.Loop.Agent), mark(state.Primary, ""))
	for i, o := range order {
		note := notes[o.ID]
		if note == "" && o.Disabled {
			note = "disabled"
		}
		fmt.Fprintf(tw, "  %d.\t%s\t%s\t%s\t%s\t%s\n", i+1, o.ID, o.Provider, o.Group, fallbackAddAgentModel(o.Agent), mark(o.ID, note))
	}
	tw.Flush()
}

func printFallbackEdit(w io.Writer, p *fallbackEdit) {
	name := p.cfg.Loop.Name
	switch p.kind {
	case "fallback_handoff":
		fmt.Fprintf(w, "Automatic handoff for loop %q (nothing written yet)\n\n", name)
		if len(p.failoverOn) > 0 {
			fmt.Fprintln(w, "  Now:    off")
			fmt.Fprintln(w, "  After:  ON for rate limits")
			fmt.Fprintln(w, "  When the agent in use reports a rate limit, keepgoing may move the loop to")
			fmt.Fprintln(w, "  the first fallback below that has just passed its check.")
		} else {
			fmt.Fprintf(w, "  Now:    on for %s\n", strings.Join(p.cfg.Loop.Errors.FailoverOn, ", "))
			fmt.Fprintln(w, "  After:  OFF")
			fmt.Fprintln(w, "  keepgoing will no longer move the loop to a fallback on its own. The")
			fmt.Fprintln(w, "  fallbacks stay saved, and scheduled checks of them continue.")
			if p.active != state.Primary {
				fmt.Fprintf(w, "  The loop stays on %s, where it is now. This does not move it\n  back; use keepgoing retarget primary for that.\n", p.active)
			}
		}
		fmt.Fprintln(w, "\nHandoff order (unchanged)")
		printHandoffOrder(w, p.cfg, p.order, p.active, nil)
	case "fallback_remove":
		fmt.Fprintf(w, "Remove fallback %s from loop %q (nothing written yet)\n\nHandoff order afterwards\n", p.subject, name)
		printHandoffOrder(w, p.cfg, p.order, p.active, nil)
		fmt.Fprintf(w, "  Removed: %s, which was at place %d.\n", p.subject, p.from)
		p.printNeeds(w)
		fmt.Fprintln(w, "\nRecorded health")
		if p.hadHealth {
			fmt.Fprintf(w, "  The saved check results for %s are deleted, so a fallback added later\n  under the same id starts with no history.\n", p.subject)
		} else {
			fmt.Fprintf(w, "  %s has no saved check results.\n", p.subject)
		}
		fmt.Fprintln(w, "  Any saved output from failed checks (probe-failures) is left in place.")
	default:
		fmt.Fprintf(w, "Move fallback %s in loop %q (nothing written yet)\n\nHandoff order afterwards\n", p.subject, name)
		printHandoffOrder(w, p.cfg, p.order, p.active, map[string]string{p.subject: "<- was " + strconv.Itoa(p.from) + "."})
		fmt.Fprintln(w, "\n  Order only decides which fallback is tried first at the next handoff.")
		fmt.Fprintln(w, "  Recorded health is kept, and the loop stays on its current target.")
	}
	fmt.Fprintln(w, "\nFiles")
	for _, f := range p.files() {
		fmt.Fprintf(w, "  update  %s\n", f)
	}
	fmt.Fprintln(w, "\nNothing is run: no agent, model or check.")
}

func (p *fallbackEdit) printNeeds(w io.Writer) {
	if len(p.failoverOn) == 0 {
		fmt.Fprintln(w, "\n  Automatic handoff is off, so no fallback is used on its own either way.")
		return
	}
	if !fallbackEditReachable(p) {
		fmt.Fprintln(w, "\n  WARNING: the fallbacks left cannot meet this loop's handoff rule, so")
		fmt.Fprintln(w, "  handoffs will be refused.")
	}
}

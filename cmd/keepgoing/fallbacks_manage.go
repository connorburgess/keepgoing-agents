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
	"reflect"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"gopkg.in/yaml.v3"
)

// fallbackEdit is one resolved change to a loop's fallback setup. Nothing in
// it has been written. A nil YAML means that file is left untouched.
type fallbackEdit struct {
	kind           string // fallback_handoff, fallback_remove or fallback_move
	cfg            *config.Config
	cfgPath        string
	fallbacksYAML  []byte
	configYAML     []byte
	leftovers      fallbackLeftovers  // what remove clears, as seen at the preview
	cleanupOnly    bool               // remove: the id is already out of the file
	comments       []string           // comment lines that go with the entry
	rateLimitAdded bool               // handoff on: rate_limit joined other classes
	replacedList   string             // handoff off: a custom list that is not remembered
	order          []config.Candidate // resulting handoff order
	rules          config.Redundancy
	active         string // current target id
	failoverOn     []string
	wasLive        bool
	unchanged      string // set when there is nothing to do, with the reason
	subject        string // candidate id for remove and move
	from, to       int    // 1-based positions; to is 0 for remove
	confirm        string // the yes/no question
	done           string // printed after a successful write
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
	// Cleared lists what remove deletes or archives besides the entry:
	// health, handoff_count and session, as seen at the preview.
	Cleared        []string `json:"cleared,omitempty"`
	RateLimitAdded bool     `json:"rateLimitAdded,omitempty"`
	Files          []string `json:"files"`
	Reason         string   `json:"reason,omitempty"`
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
	current := strings.Join(p.failoverOn, ", ")
	hasRateLimit := false
	for _, class := range p.failoverOn {
		hasRateLimit = hasRateLimit || class == "rate_limit"
	}
	switch {
	case on && hasRateLimit:
		p.unchanged = fmt.Sprintf("Automatic handoff is already on for: %s.", current)
		return p, nil
	case !on && !p.wasLive:
		p.unchanged = "Automatic handoff is already off."
		return p, nil
	case on:
		// Other classes already hand off; rate limits are what this command
		// promises, so add that class and keep the rest.
		p.rateLimitAdded = p.wasLive
		p.failoverOn = append(p.failoverOn, "rate_limit")
		p.confirm, p.done = "Turn automatic handoff on?", "Automatic handoff is on for rate limits."
		if p.rateLimitAdded {
			p.confirm, p.done = "Add rate limits to automatic handoff?", fmt.Sprintf("Automatic handoff is on for: %s.", strings.Join(p.failoverOn, ", "))
		}
	default:
		if current != "rate_limit" {
			p.replacedList = current
		}
		p.failoverOn = []string{}
		p.confirm, p.done = "Turn automatic handoff off?", "Automatic handoff is off."
	}
	raw, err := os.ReadFile(p.cfgPath)
	if err != nil {
		return nil, errors.New("The configuration is unreadable.")
	}
	// Prefer changing only the list's own characters; re-emit the document
	// only when the setting is absent or not written inline.
	if edited, ok := setInlineFailoverOn(raw, p.failoverOn); ok {
		p.configYAML = edited
	} else if p.configYAML, err = editLoopYAML(raw, false, &p.failoverOn); err != nil {
		return nil, err
	}
	next, err := config.Parse(p.configYAML)
	if err != nil || strings.Join(next.Loop.Errors.FailoverOn, ",") != strings.Join(p.failoverOn, ",") {
		return nil, errors.New("Could not update keepgoing.yaml safely; edit it by hand.")
	}
	return p, nil
}

// setInlineFailoverOn rewrites an inline list such as "failover_on: [a, b]"
// in place, leaving every other byte of keepgoing.yaml, including a comment
// after the list, exactly as it was. It reports false when the setting is
// absent or written another way.
func setInlineFailoverOn(raw []byte, classes []string) ([]byte, bool) {
	v := yamlMapValue(yamlMapValue(yamlMapValue(yamlDocRoot(raw), "loop"), "errors"), "failover_on")
	if v == nil || v.Kind != yaml.SequenceNode || v.Style&yaml.FlowStyle == 0 || v.Line < 1 || v.Column < 1 {
		return nil, false
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if v.Line > len(lines) {
		return nil, false
	}
	line := lines[v.Line-1]
	open := v.Column - 1
	if open >= len(line) || line[open] != '[' {
		return nil, false
	}
	closing := strings.IndexByte(line[open:], ']')
	if closing < 0 || strings.ContainsAny(line[open+1:open+closing], "[{#\"'") {
		return nil, false
	}
	lines[v.Line-1] = line[:open] + "[" + strings.Join(classes, ", ") + "]" + line[open+closing+1:]
	return []byte(strings.Join(lines, "")), true
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

// fallbackLeftovers is what a fallback leaves in the state directory besides
// its entry: saved check results, a count of rejected handoff messages, and
// the session its turns resume.
type fallbackLeftovers struct {
	health, briefs, session bool
}

func (l fallbackLeftovers) any() bool { return l.health || l.briefs || l.session }

func (l fallbackLeftovers) names() []string {
	var out []string
	for name, on := range map[string]bool{"health": l.health, "handoff_count": l.briefs, "session": l.session} {
		if on {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func fallbackTargetDir(cfg *config.Config, id string) string {
	return filepath.Join(cfg.Loop.StateDir, "targets", id)
}

// findFallbackLeftovers only looks; id must already be a safe path element.
func findFallbackLeftovers(cfg *config.Config, id string) (fallbackLeftovers, error) {
	var l fallbackLeftovers
	reg, err := health.Load(cfg.Loop.StateDir)
	if err != nil {
		return l, errors.New("The loop's recorded health is unreadable; nothing can be cleared safely.")
	}
	_, l.health = reg.Candidates[id]
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return l, errors.New("The loop's saved state is unreadable, so its current target is unknown.")
	}
	_, l.briefs = st.HandoffBriefs[id]
	if _, err := os.Lstat(fallbackTargetDir(cfg, id)); err == nil {
		l.session = true
	}
	return l, nil
}

func planFallbackRemove(cfg *config.Config, id string) (*fallbackEdit, error) {
	p, err := newFallbackEdit("fallback_remove", cfg)
	if err != nil {
		return nil, err
	}
	at, findErr := fallbackEditFind(cfg, id)
	if findErr != nil {
		// A removal that was interrupted after the file was written can be
		// finished by running it again: only the leftovers remain to clear.
		if fallbackAddSlug(id) != id || id == state.Primary || len(id) > 64 {
			return nil, findErr
		}
		if p.leftovers, err = findFallbackLeftovers(cfg, id); err != nil {
			return nil, err
		}
		if !p.leftovers.any() {
			return nil, findErr
		}
		if p.active == id {
			return nil, fmt.Errorf("The loop's saved state says it is running on %q, which is not a\n  fallback of this loop. Move it first with: keepgoing retarget primary", id)
		}
		p.subject, p.cleanupOnly = id, true
		p.confirm, p.done = "Clear these leftover records?", fmt.Sprintf("Cleared the leftover records of %s.", id)
		return p, nil
	}
	if p.active == id {
		return nil, fmt.Errorf("The loop is running on %q right now. Move it first with:\n    keepgoing retarget primary %s\n      -c %s", id, lineContinuation(), commandQuote(p.cfgPath))
	}
	if len(p.order) == 1 {
		return nil, fmt.Errorf("%q is this loop's only fallback, and a fallbacks file cannot be empty.\n  To stop automatic handoff, use: keepgoing fallbacks handoff off", id)
	}
	if p.leftovers, err = findFallbackLeftovers(cfg, id); err != nil {
		return nil, err
	}
	p.subject, p.from = id, at+1
	p.order = append(append([]config.Candidate{}, p.order[:at]...), p.order[at+1:]...)
	p.confirm, p.done = "Remove this fallback?", fmt.Sprintf("Removed fallback %s.", id)
	return p, p.rewriteCandidates(at, 0)
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
	return p, p.rewriteCandidates(at, to)
}

// candidateSpan is one entry's lines in the fallbacks file: the comment
// lines directly above it, with no blank line between, and its own lines.
// Blank lines and comments set apart by one belong to the file's layout, not
// to an entry, and are never moved or deleted.
type candidateSpan struct {
	start, end int // [start, end) line indexes
	comments   int // leading attached comment lines
}

func yamlComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }
func yamlBlank(line string) bool   { return strings.TrimSpace(line) == "" }

// candidateSpans locates every entry by the positions the YAML parser
// reports, so the edit can cut and paste whole lines and leave every other
// byte of the file as it was.
func candidateSpans(raw []byte) ([]string, []candidateSpan, error) {
	byHand := errors.New("The fallbacks file's candidates list cannot be edited line by line;\n  edit it by hand.")
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil, nil, errors.New("The fallbacks file is not valid YAML.")
	}
	root := yamlRoot(&doc)
	list := yamlMapValue(root, "candidates")
	if list == nil || list.Kind != yaml.SequenceNode || list.Style&yaml.FlowStyle != 0 || len(list.Content) == 0 {
		return nil, nil, byHand
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// The list ends where the next top-level key begins, or at the end of the file.
	limit := len(lines)
	for i := 0; i+1 < len(root.Content); i += 2 {
		if l := root.Content[i].Line - 1; l > list.Line-1 && l < limit {
			limit = l
		}
	}
	spans := make([]candidateSpan, len(list.Content))
	for i, item := range list.Content {
		start := item.Line - 1
		if start < 0 || start >= limit || !strings.HasPrefix(strings.TrimSpace(lines[start]), "-") || (i > 0 && start <= spans[i-1].start+spans[i-1].comments) {
			return nil, nil, byHand
		}
		first := start
		for first > 0 && yamlComment(lines[first-1]) {
			first--
		}
		spans[i] = candidateSpan{start: first, comments: start - first}
	}
	for i := range spans {
		end := limit
		if i+1 < len(spans) {
			end = spans[i+1].start
		}
		for end > spans[i].start+spans[i].comments+1 && (yamlBlank(lines[end-1]) || yamlComment(lines[end-1])) {
			end--
		}
		spans[i].end = end
	}
	return lines, spans, nil
}

// rewriteCandidates removes the entry at index at, or moves it to the 1-based
// place to, by cutting and pasting its lines. The result must parse to exactly
// the planned order with every entry unchanged, or the edit is refused.
func (p *fallbackEdit) rewriteCandidates(at, to int) error {
	raw, err := os.ReadFile(p.cfg.Loop.Fallbacks)
	if err != nil {
		return errors.New("The fallbacks file is unreadable.")
	}
	lines, spans, err := candidateSpans(raw)
	if err != nil {
		return err
	}
	if len(spans) != len(p.cfg.FallbackSet.Candidates) {
		return errors.New("The fallbacks file changed while it was being read; run the command again.")
	}
	cut := spans[at]
	block := append([]string{}, lines[cut.start:cut.end]...)
	for _, l := range block[:cut.comments] {
		p.comments = append(p.comments, bbLabel(l, 70))
	}
	eol := "\n"
	if strings.HasSuffix(lines[0], "\r\n") {
		eol = "\r\n"
	}
	terminate := func(ls []string) {
		if n := len(ls); n > 0 && !strings.HasSuffix(ls[n-1], "\n") {
			ls[n-1] += eol
		}
	}
	out := append(append([]string{}, lines[:cut.start]...), lines[cut.end:]...)
	if to > 0 {
		rest := append(append([]candidateSpan{}, spans[:at]...), spans[at+1:]...)
		shift := func(i int) int { // a line index after the cut
			if i >= cut.end {
				return i - (cut.end - cut.start)
			}
			return i
		}
		insert := shift(rest[len(rest)-1].end)
		if to-1 < len(rest) {
			insert = shift(rest[to-1].start)
		}
		terminate(block)
		head := append([]string{}, out[:insert]...)
		terminate(head)
		out = append(append(head, block...), out[insert:]...)
	}
	p.fallbacksYAML = []byte(strings.Join(out, ""))
	parsed, err := config.ParseFallbacks(p.fallbacksYAML)
	if err != nil || len(parsed.Candidates) != len(p.order) {
		return errors.New("Could not rewrite the fallbacks file safely; edit it by hand.")
	}
	before, _ := config.ParseFallbacks(raw)
	for i, c := range parsed.Candidates {
		old, ok := before.Find(c.ID)
		if c.ID != p.order[i].ID || !ok || !reflect.DeepEqual(c, old) {
			return errors.New("Could not rewrite the fallbacks file safely; edit it by hand.")
		}
	}
	if !reflect.DeepEqual(parsed.Redundancy, before.Redundancy) || !reflect.DeepEqual(parsed.Probe, before.Probe) {
		return errors.New("Could not rewrite the fallbacks file safely; edit it by hand.")
	}
	return nil
}

// fallbackHealthSave is swapped by tests to exercise a failed health write.
var fallbackHealthSave = func(reg *health.Registry, stateDir string) error { return reg.Save(stateDir) }

// writeFallbackEdit applies the change under the tick lock with the same
// guards as fallbacks add. Everything that depends on runtime records is
// decided here, not from the preview: a check may have run, or the loop may
// have handed off, while the question was on screen.
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
		if err != nil {
			return exitFailed, errors.New("the loop's saved state is unreadable, so its current target is unknown; nothing written")
		}
		if st.Target() == p.subject {
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
	if p.kind == "fallback_remove" {
		if err := clearFallbackLeftovers(p.cfg, p.subject); err != nil {
			if p.cleanupOnly {
				return exitFailed, fmt.Errorf("%w. Run the same command again to finish", err)
			}
			return exitFailed, fmt.Errorf("the fallback was removed, but %w. Run the same command again to finish; do not reuse the id %s until it succeeds", err, p.subject)
		}
	}
	return exitOK, nil
}

// clearFallbackLeftovers runs under the tick lock. It deletes the id's saved
// check results and rejected-handoff count, and moves its session directory
// aside, so nothing recorded for a removed fallback is picked up by a later
// one with the same id. Each step is skipped when there is nothing to clear.
func clearFallbackLeftovers(cfg *config.Config, id string) error {
	dir := cfg.Loop.StateDir
	reg, err := health.Load(dir)
	if err != nil {
		return errors.New("its saved check results are unreadable")
	}
	if _, ok := reg.Candidates[id]; ok {
		delete(reg.Candidates, id)
		if err := fallbackHealthSave(reg, dir); err != nil {
			return errors.New("its saved check results could not be deleted")
		}
	}
	st, err := state.Load(dir)
	if err != nil {
		return errors.New("the loop's saved state is unreadable")
	}
	if _, ok := st.HandoffBriefs[id]; ok {
		delete(st.HandoffBriefs, id)
		if len(st.HandoffBriefs) == 0 {
			st.HandoffBriefs = nil
		}
		if err := state.Save(dir, st); err != nil {
			return errors.New("its rejected-handoff count could not be cleared")
		}
	}
	target := fallbackTargetDir(cfg, id)
	if _, err := os.Lstat(target); err == nil {
		archive := filepath.Join(dir, "removed-targets")
		if err := os.MkdirAll(archive, 0o700); err != nil {
			return errors.New("its saved session could not be moved aside")
		}
		if err := os.Rename(target, filepath.Join(archive, id+"-"+time.Now().UTC().Format("20060102T150405.000000000Z"))); err != nil {
			return errors.New("its saved session could not be moved aside")
		}
	}
	return nil
}

func fallbackEditReachable(p *fallbackEdit) bool {
	return fallbackAddReachable(&fallbackAddPlan{cfg: p.cfg, order: p.order, rules: &config.Fallbacks{Redundancy: p.rules}})
}

func (p *fallbackEdit) healthNote() string {
	switch {
	case p.kind != "fallback_remove":
		return ""
	case p.leftovers.health:
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
		Health:     p.healthNote(), Files: []string{}, Reason: p.unchanged, Cleared: p.leftovers.names(), RateLimitAdded: p.rateLimitAdded}
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
	if p.leftovers.health {
		out = append(out, filepath.Join(p.cfg.Loop.StateDir, health.FileName))
	}
	if p.leftovers.briefs {
		out = append(out, filepath.Join(p.cfg.Loop.StateDir, state.FileName))
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
		was := strings.Join(p.cfg.Loop.Errors.FailoverOn, ", ")
		switch {
		case p.rateLimitAdded:
			fmt.Fprintf(w, "  Now:    on for %s, but not for rate limits\n", was)
			fmt.Fprintf(w, "  After:  on for %s\n", strings.Join(p.failoverOn, ", "))
			fmt.Fprintln(w, "  Rate limits are added to the error types that already hand off; the")
			fmt.Fprintln(w, "  others are kept.")
		case len(p.failoverOn) > 0:
			fmt.Fprintln(w, "  Now:    off")
			fmt.Fprintln(w, "  After:  ON for rate limits")
			fmt.Fprintln(w, "  When the agent in use reports a rate limit, keepgoing may move the loop to")
			fmt.Fprintln(w, "  the first fallback below that has just passed its check.")
		default:
			fmt.Fprintf(w, "  Now:    on for %s\n", was)
			fmt.Fprintln(w, "  After:  OFF")
			fmt.Fprintln(w, "  keepgoing will no longer move the loop to a fallback on its own. The")
			fmt.Fprintln(w, "  fallbacks stay saved, and scheduled checks of them continue.")
			if p.replacedList != "" {
				fallbackAddWrap(w, "  ", "  ", fmt.Sprintf("The current list (%s) is not remembered: turning handoff on again restores rate limits only.", p.replacedList))
			}
			if p.active != state.Primary {
				fmt.Fprintf(w, "  The loop stays on %s, where it is now. This does not move it\n  back; use keepgoing retarget primary for that.\n", p.active)
			}
		}
		fmt.Fprintln(w, "\nHandoff order (unchanged)")
		printHandoffOrder(w, p.cfg, p.order, p.active, nil)
	case "fallback_remove":
		if p.cleanupOnly {
			fmt.Fprintf(w, "%s is no longer a fallback of loop %q, but left records behind\n(nothing written yet)\n", p.subject, name)
		} else {
			fmt.Fprintf(w, "Remove fallback %s from loop %q (nothing written yet)\n\nHandoff order afterwards\n", p.subject, name)
			printHandoffOrder(w, p.cfg, p.order, p.active, nil)
			fmt.Fprintf(w, "  Removed: %s, which was at place %d.\n", p.subject, p.from)
			p.printComments(w, "Comment lines directly above it are removed with it:")
			p.printNeeds(w)
		}
		fmt.Fprintf(w, "\nRecords of %s\n", p.subject)
		row := func(found bool, verb, what string) {
			if found {
				fmt.Fprintf(w, "  %-9s %s\n", verb, what)
			}
		}
		row(p.leftovers.health, "deleted", "saved check results (health.json)")
		row(p.leftovers.briefs, "deleted", "count of rejected handoff messages (state.json)")
		row(p.leftovers.session, "archived", "saved session and turn files, moved to removed-targets/")
		if !p.leftovers.any() {
			fmt.Fprintln(w, "  none      no saved check results, handoff count or session")
		}
		fmt.Fprintln(w, "  kept      saved output from failed checks (probe-failures/), if any")
		fmt.Fprintln(w, "  This is looked at again when the change is written. A fallback added")
		fmt.Fprintln(w, "  later under this id starts a new session with no check results.")
	default:
		fmt.Fprintf(w, "Move fallback %s in loop %q (nothing written yet)\n\nHandoff order afterwards\n", p.subject, name)
		printHandoffOrder(w, p.cfg, p.order, p.active, map[string]string{p.subject: "<- was " + strconv.Itoa(p.from) + "."})
		p.printComments(w, "Comment lines directly above it move with it:")
		fmt.Fprintln(w, "\n  Order only decides which fallback is tried first at the next handoff.")
		fmt.Fprintln(w, "  Recorded health is kept, and the loop stays on its current target.")
	}
	if files := p.files(); len(files) > 0 {
		fmt.Fprintln(w, "\nFiles")
		for _, f := range files {
			fmt.Fprintf(w, "  update  %s\n", f)
		}
	}
	fmt.Fprintln(w, "\nNothing is run: no agent, model or check.")
}

// printComments shows which comment lines travel with the entry. Comments
// set apart by a blank line stay where they are and are not listed.
func (p *fallbackEdit) printComments(w io.Writer, heading string) {
	if len(p.comments) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s\n", heading)
	for i, c := range p.comments {
		if i == 4 && len(p.comments) > 5 {
			fmt.Fprintf(w, "    ... and %d more\n", len(p.comments)-4)
			break
		}
		fmt.Fprintf(w, "    %s\n", c)
	}
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

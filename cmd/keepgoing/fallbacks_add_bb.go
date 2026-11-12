package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// bbLookup runs one read-only bb listing and returns its stdout. The wizard
// calls it only after the user agrees; nothing is sent to any thread.
type bbLookup func(ctx context.Context, cli string, args ...string) ([]byte, error)

const (
	bbLookupLimit = 8 << 20
	bbListPage    = 9
)

// bbLookupTimeout bounds one listing; tests shorten it.
var bbLookupTimeout = 20 * time.Second

var bbReasoningLevels = []string{"low", "medium", "high", "xhigh", "max"}

// runBBLookup bounds a listing in time and size. stderr is discarded: only
// the structured stdout is ever interpreted.
func runBBLookup(ctx context.Context, cli string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, bbLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.WaitDelay = 2 * time.Second
	out := &bbLimitedBuffer{}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if out.over {
		return nil, errors.New("listing too large")
	}
	return out.buf.Bytes(), nil
}

// bbLimitedBuffer has no ReadFrom, so every byte passes through Write.
type bbLimitedBuffer struct {
	buf  bytes.Buffer
	over bool
}

func (b *bbLimitedBuffer) Write(p []byte) (int, error) {
	if b.over || b.buf.Len()+len(p) > bbLookupLimit {
		b.over = true
		return len(p), nil // keep draining so bb can exit
	}
	return b.buf.Write(p)
}

type bbThread struct {
	ID         string  `json:"id"`
	Provider   string  `json:"providerId"`
	Title      *string `json:"title"`
	Status     string  `json:"status"`
	Visibility string  `json:"visibility"`
	ArchivedAt any     `json:"archivedAt"`
}

type bbModel struct {
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
	Default     bool   `json:"isDefault"`
	Efforts     []struct {
		Level string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
}

func (m bbModel) levels() []string {
	var out []string
	for _, e := range m.Efforts {
		if bbReasoningLevel(e.Level) == nil {
			out = append(out, e.Level)
		}
	}
	return out
}

// bbThreads lists threads a candidate could use: live, visible, with a
// usable ID. An error envelope or any other shape is "unavailable".
func bbThreads(ctx context.Context, bb bbLookup, cli string) ([]bbThread, error) {
	raw, err := bb(ctx, cli, "thread", "list", "--json")
	if err != nil {
		return nil, errors.New("unavailable")
	}
	var all []bbThread
	if json.Unmarshal(raw, &all) != nil {
		return nil, errors.New("unavailable")
	}
	var out []bbThread
	for _, t := range all {
		if transferThreadID.MatchString(t.ID) && t.ArchivedAt == nil && t.Visibility != "hidden" {
			out = append(out, t)
		}
	}
	return out, nil
}

func bbModels(ctx context.Context, bb bbLookup, cli, provider string) ([]bbModel, error) {
	raw, err := bb(ctx, cli, "provider", "models", provider, "--json")
	if err != nil {
		return nil, errors.New("unavailable")
	}
	var all []bbModel
	if json.Unmarshal(raw, &all) != nil {
		return nil, errors.New("unavailable")
	}
	var out []bbModel
	for _, m := range all {
		// A listed model is written to configuration, so it must be clean as
		// given, not merely printable after cleaning.
		if m.Model != "" && fallbackAddModelName("model", m.Model) == nil && len(m.Model) <= 128 {
			out = append(out, m)
		}
	}
	return out, nil
}

// bbUnprintable reports characters that must never reach a terminal from
// bb's output: C0/C1 controls (which include escape and CSI), format
// characters such as bidirectional overrides, and line/paragraph separators.
func bbUnprintable(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// bbLabel makes text that came from bb safe and short for one terminal
// column. Everything printed from a listing passes through it.
func bbLabel(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case bbUnprintable(r) || unicode.IsSpace(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "(untitled)"
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

func bbReasoningLevel(s string) error {
	for _, l := range bbReasoningLevels {
		if s == l {
			return nil
		}
	}
	return fmt.Errorf("The reasoning level must be one of: %s.", strings.Join(bbReasoningLevels, ", "))
}

// bbProbeKind maps a BB provider to the local CLI that can probe it.
func bbProbeKind(provider string) (string, error) {
	switch provider {
	case "claude-code":
		return "claude", nil
	case "codex":
		return "codex", nil
	case "":
		return "", errors.New("A BB fallback needs its thread's provider: claude-code or codex.")
	}
	return "", fmt.Errorf("Provider %q has no built-in check.\n  Guided add supports BB threads on claude-code or codex; others need a\n  hand-written command probe.", bbLabel(provider, 24))
}

func bbCLI(cfg *config.Config, lookPath func(string) (string, error)) (string, error) {
	if cfg.Loop.BB != nil && cfg.Loop.BB.CLI != "" {
		return cfg.Loop.BB.CLI, nil
	}
	cli, err := lookPath("bb")
	if err != nil {
		return "", errors.New("The bb CLI was not found on PATH.")
	}
	return filepath.Abs(cli)
}

// bbUse describes how a thread is already used by the loop.
type bbUse struct {
	owner, provider, model string
}

func bbThreadUses(cfg *config.Config, thread string) []bbUse {
	var out []bbUse
	if cfg.Loop.BB != nil && cfg.Loop.BB.Thread == thread {
		out = append(out, bbUse{"the primary", cfg.Loop.Provider, cfg.Loop.BB.Model})
	}
	if cfg.FallbackSet != nil {
		for _, c := range cfg.FallbackSet.Candidates {
			if c.BB != nil && c.BB.Thread == thread {
				out = append(out, bbUse{fmt.Sprintf("fallback %q", c.ID), c.Provider, c.BB.Model})
			}
		}
	}
	return out
}

// bbSameThread refuses a second entry for a thread unless its model differs
// from every existing use, which is the only thing a handoff would change.
// Model names are compared without regard to case. A thread has one provider,
// so a different provider for a thread the loop already uses is a mistake.
func bbSameThread(cfg *config.Config, thread, provider, model string) error {
	for _, u := range bbThreadUses(cfg, thread) {
		if provider != "" && u.provider != "" && provider != u.provider {
			return fmt.Errorf("This thread is already %s, on provider %s. A thread has one\n  provider: use %s, or choose another thread.", u.owner, bbLabel(u.provider, 24), bbLabel(u.provider, 24))
		}
		if model == "" {
			return fmt.Errorf("This thread is already %s. Adding it again only helps with a\n  different model: name the model to switch to.", u.owner)
		}
		if strings.EqualFold(model, u.model) {
			return fmt.Errorf("This thread is already %s on model %s. Choose a different\n  model, or another thread.", u.owner, u.model)
		}
	}
	return nil
}

// bbModelInUse names the model a same-thread fallback must differ from.
func bbModelInUse(cfg *config.Config, thread string) string {
	var known []string
	for _, u := range bbThreadUses(cfg, thread) {
		if u.model == "" {
			return "The model in use is not recorded, so make sure yours differs"
		}
		known = append(known, u.model)
	}
	return "It must differ from " + strings.Join(known, " and ")
}

// fallbackAddBBDetail is what the preview and report say about a BB candidate.
type fallbackAddBBDetail struct {
	relation     string // same_as_primary, same_as_fallback or other
	title        string // from a listing; empty when the thread was given by ID
	thread       string // printed only with --show-ids
	modelListed  bool   // chosen from BB's model list
	probeAccount string // explicit probe account directory, or empty
	showIDs      bool
}

func fallbackAddBBDetailOf(cfg *config.Config, o fallbackAddOptions, a config.Agent) *fallbackAddBBDetail {
	if a.BB == nil {
		return nil
	}
	d := &fallbackAddBBDetail{relation: "other", title: o.threadTitle, thread: a.BB.Thread, modelListed: o.modelListed, showIDs: o.showIDs}
	if uses := bbThreadUses(cfg, a.BB.Thread); len(uses) > 0 {
		d.relation = "same_as_fallback"
		if uses[0].owner == "the primary" {
			d.relation = "same_as_primary"
		}
	}
	d.probeAccount, _ = fallbackAddAccount(o.probeAccount)
	return d
}

// fallbackAddBBTarget builds a BB candidate. It runs nothing: the thread,
// provider and model are taken as given.
func fallbackAddBBTarget(cfg *config.Config, o fallbackAddOptions, lookPath func(string) (string, error)) (config.Agent, config.Probe, []string, error) {
	fail := func(err error) (config.Agent, config.Probe, []string, error) {
		return config.Agent{}, config.Probe{}, nil, err
	}
	for _, name := range []string{"account-dir", "workdir"} {
		if o.set[name] {
			return fail(fmt.Errorf("--%s applies only to headless claude and codex. A BB thread uses the\n  account and project BB gave it; use --probe-account-dir for the probe.", name))
		}
	}
	thread := strings.TrimSpace(o.thread)
	if !transferThreadID.MatchString(thread) {
		return fail(errors.New("A BB fallback needs an existing thread ID (--thread thr_...).\n  Nothing is created or moved."))
	}
	provider := strings.TrimSpace(o.provider)
	kind, err := bbProbeKind(provider)
	if err != nil {
		return fail(err)
	}
	if o.reasoning != "" {
		if err := bbReasoningLevel(o.reasoning); err != nil {
			return fail(err)
		}
	}
	if err := bbSameThread(cfg, thread, provider, o.model); err != nil {
		return fail(err)
	}
	group := strings.TrimSpace(o.group)
	if group == "" {
		group = bbDefaultGroup(cfg, provider, o.model)
	}
	if err := fallbackAddGroup(cfg, group); err != nil {
		return fail(err)
	}
	cli, err := bbCLI(cfg, lookPath)
	if err != nil {
		return fail(err)
	}
	// The probe is a local CLI turn; without that CLI the candidate could
	// never be confirmed and so never used.
	if _, err := lookPath(kind); err != nil {
		return fail(fmt.Errorf("The %s CLI was not found on PATH. A BB fallback is checked with\n  that local CLI before any handoff, so it must be installed and signed in.", kind))
	}
	probe := config.Probe{Kind: kind, Model: o.probeModel}
	probeAccount, err := fallbackAddAccount(o.probeAccount)
	if err != nil {
		return fail(errors.New("Probe account directory: not an existing directory. Sign in to it first,\n  or leave it blank."))
	}
	if probeAccount != "" {
		probe.Env = map[string]string{fallbackAddAccountKey(kind): probeAccount}
	}
	agent := config.Agent{Adapter: "bb", Provider: provider, Group: group, BB: &config.BB{Thread: thread, CLI: cli, Model: o.model, Reasoning: o.reasoning}}

	// One short line each: the preview is read before a yes/no.
	var warnings []string
	uses := bbThreadUses(cfg, thread)
	claim := "this thread"
	if len(uses) > 0 {
		claim = o.model
		warnings = append(warnings, fmt.Sprintf("Same conversation as %s: same history, likely one account.", uses[0].owner))
		for _, u := range uses {
			if u.model == "" {
				warnings = append(warnings, fmt.Sprintf("Returning to %s leaves the thread on %s: its bb.model is unset.", u.owner, o.model))
			}
		}
	} else {
		warnings = append(warnings, "keepgoing cannot see which account BB uses for this thread.")
	}
	warnings = append(warnings, fmt.Sprintf("Its own quota group is your claim that %s has a separate limit. keepgoing cannot check that; the check before a handoff is the only test.", claim))
	if o.model == "" {
		warnings = append(warnings, fmt.Sprintf("No model set: the check uses the local %s CLI's default model.", kind))
	}
	return agent, probe, warnings, nil
}

// bbDefaultGroup suggests the model's name, since a second model on a known
// account is the commonest BB fallback, and otherwise the provider.
func bbDefaultGroup(cfg *config.Config, provider, model string) string {
	base := fallbackAddSlug(model)
	if base == "" {
		base = provider
	}
	if base == "" {
		base = "bb"
	}
	return fallbackAddFreeGroup(cfg, base)
}

// collectBB is steps 2 and 3 for a BB candidate: the thread, then the model.
func (w *fallbackAddWizard) collectBB() error {
	out, cfg, o := w.e.stdout, w.cfg, w.o
	cli, err := bbCLI(cfg, w.lookPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\nStep 2 of 4: Thread")
	fmt.Fprintln(out, "  Choose an existing BB thread. It can be this loop's own conversation, to")
	fmt.Fprintln(out, "  continue it on another model, or a different thread.")
	lookup := false
	if w.bb != nil && !(o.set["thread"] && o.set["provider"] && o.set["model"]) {
		fmt.Fprintln(out, "  keepgoing can ask BB for your threads and models so you can pick from a")
		fmt.Fprintln(out, "  list. That runs two read-only bb commands (bb thread list, bb provider")
		fmt.Fprintln(out, "  models). No message is sent and no thread is created or changed.")
		if lookup, err = w.askYesNo("Ask BB for the lists? (yes/no)", "no"); err != nil {
			return err
		}
	}
	if err := w.pickBBThread(cli, lookup); err != nil {
		return err
	}
	same := bbThreadUses(cfg, o.thread)
	if len(same) > 0 && same[0].provider != "" && !o.set["provider"] {
		o.provider = same[0].provider // a thread has one provider; the loop already records it
	}
	if _, known := bbProbeKind(o.provider); known != nil || o.set["provider"] {
		err = w.askValid("provider", "Provider of that thread (claude-code/codex)", "", &o.provider, func(s string) error {
			if _, err := bbProbeKind(s); err != nil {
				return err
			}
			if len(same) > 0 && same[0].provider != "" && s != same[0].provider {
				return fmt.Errorf("This thread is already %s, on provider %s. A thread has one\n  provider: use %s, or choose another thread.", same[0].owner, bbLabel(same[0].provider, 24), bbLabel(same[0].provider, 24))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	kind, _ := bbProbeKind(o.provider)
	if len(same) > 0 {
		fmt.Fprintf(out, "  Note: this is the same conversation as %s. A handoff will switch its\n  model and continue in it; you will choose that model next.\n", same[0].owner)
	} else {
		fmt.Fprintln(out, "  keepgoing cannot see which account BB uses for this thread.")
	}

	fmt.Fprintln(out, "\nStep 3 of 4: Model")
	if err := w.pickBBModel(cli, lookup, len(same) > 0); err != nil {
		return err
	}
	fmt.Fprintln(out, "  A quota group names one usage limit. A fallback needs a group of its own")
	fmt.Fprintf(out, "  (not %s, the primary's): that is your claim that it has a separate\n", cfg.Loop.Group)
	fmt.Fprintln(out, "  limit. keepgoing cannot check the claim.")
	if err := w.askValid("group", "Quota group", bbDefaultGroup(cfg, o.provider, o.model), &o.group, func(s string) error { return fallbackAddGroup(cfg, s) }); err != nil {
		return err
	}
	fmt.Fprintln(out, "  keepgoing hands off only to a fallback that has just passed a check: one")
	fmt.Fprintf(out, "  small real request from the local %s CLI. It tests BB's account only\n  if both use the same login.\n", kind)
	if err := w.askValid("probe-model", "Cheaper model for scheduled checks (blank = same model)", "", &o.probeModel, func(s string) error { return fallbackAddModelName("probe model", s) }); err != nil {
		return err
	}
	return w.askValid("probe-account-dir", "Account directory for the check (blank = the CLI's current login)", "", &o.probeAccount, func(s string) error {
		if _, err := fallbackAddAccount(s); err != nil {
			return errors.New("Not an existing directory. Sign in to it first, or leave it blank.")
		}
		return nil
	})
}

func (w *fallbackAddWizard) pickBBThread(cli string, lookup bool) error {
	out, cfg, o := w.e.stdout, w.cfg, w.o
	if o.set["thread"] {
		if !transferThreadID.MatchString(o.thread) {
			return errors.New("--thread must be an existing BB thread ID (thr_...).")
		}
		return nil
	}
	var threads []bbThread
	if lookup {
		fmt.Fprintln(out, "  Asking BB for your threads (up to 20 seconds)...")
		var err error
		if threads, err = bbThreads(w.e.ctx, w.bb, cli); err != nil || len(threads) == 0 {
			fmt.Fprintln(out, "  BB's thread list is unavailable; enter the thread ID instead.")
			threads = nil
		}
	}
	// This loop's own conversation first: continuing it on another model is
	// the commonest BB fallback.
	if cfg.Loop.BB != nil {
		for i, t := range threads {
			if t.ID == cfg.Loop.BB.Thread {
				threads = append([]bbThread{t}, append(threads[:i:i], threads[i+1:]...)...)
				break
			}
		}
	}
	shown, ids := 0, o.showIDs
	show := func(from, to int) {
		tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
		for i := from; i < to; i++ {
			t, title, note := threads[i], "", ""
			if t.Title != nil {
				title = *t.Title
			}
			if len(bbThreadUses(cfg, t.ID)) > 0 {
				note = "used by this loop"
			} else if _, err := bbProbeKind(t.Provider); err != nil {
				note = "not supported here"
			}
			row := fmt.Sprintf("  %d.\t%s\t%s\t%s\t%s", i+1, bbLabel(title, 30), bbLabel(t.Provider, 14), bbLabel(t.Status, 10), note)
			if ids {
				row += "\t" + t.ID // matched against the thread-ID pattern when listed
			}
			fmt.Fprintln(tw, row)
		}
		tw.Flush()
		shown = to
	}
	more := func() {
		if shown < len(threads) {
			fmt.Fprintf(out, "  Type more for the next %d of %d.\n", min(bbListPage, len(threads)-shown), len(threads))
		}
	}
	label := "Thread ID"
	if len(threads) > 0 {
		fmt.Fprintln(out, "  Your BB threads (listed by bb; nothing was sent):")
		show(0, min(bbListPage, len(threads)))
		label = "Thread (a number, or a thread ID)"
		fmt.Fprintln(out, "  Type ids to show thread IDs.")
		more()
	}
	// --provider must agree with what BB lists for a picked thread.
	conflict := func(t bbThread) bool {
		if o.set["provider"] && t.Provider != "" && t.Provider != o.provider {
			fmt.Fprintf(out, "  ! BB lists this thread's provider as %s, but --provider says %s.\n    Pick another thread, or run again without --provider.\n", bbLabel(t.Provider, 24), bbLabel(o.provider, 24))
			return true
		}
		return false
	}
	for {
		s, err := w.ask(label, "")
		if err != nil {
			return err
		}
		switch {
		case s == "more" && shown < len(threads):
			show(shown, min(shown+bbListPage, len(threads)))
			more()
			continue
		case s == "ids" && len(threads) > 0:
			ids = true
			show(0, shown) // keep the place in the list
			more()
			continue
		}
		if n, err := strconv.Atoi(s); err == nil && len(threads) > 0 {
			if n < 1 || n > shown {
				fmt.Fprintf(out, "  ! Choose a number between 1 and %d.\n", shown)
				continue
			}
			t := threads[n-1]
			if conflict(t) {
				continue
			}
			if _, err := bbProbeKind(t.Provider); err != nil {
				fmt.Fprintf(out, "  ! %v\n", err)
				continue
			}
			o.thread, o.threadTitle = t.ID, "(untitled)"
			if t.Title != nil {
				o.threadTitle = bbLabel(*t.Title, 44)
			}
			if !o.set["provider"] {
				o.provider = t.Provider
			}
			return nil
		}
		if !transferThreadID.MatchString(s) {
			fmt.Fprintln(out, "  ! Enter a number from the list or a thread ID (thr_...).")
			continue
		}
		listed := false
		for _, t := range threads {
			if t.ID == s {
				if conflict(t) {
					listed = true
					s = ""
					break
				}
				listed = true
				if t.Title != nil {
					o.threadTitle = bbLabel(*t.Title, 44)
				}
				if !o.set["provider"] {
					o.provider = t.Provider
				}
			}
		}
		if s == "" {
			continue
		}
		if len(threads) > 0 && !listed {
			fmt.Fprintln(out, "  Note: that ID is not in BB's list (it may be archived, hidden or on")
			fmt.Fprintln(out, "  another machine). It is used as typed.")
		}
		o.thread = s
		return nil
	}
}

func (w *fallbackAddWizard) pickBBModel(cli string, lookup, sameThread bool) error {
	out, cfg, o := w.e.stdout, w.cfg, w.o
	check := func(s string) error {
		if err := fallbackAddModelName("model", s); err != nil {
			return err
		}
		return bbSameThread(cfg, o.thread, o.provider, s)
	}
	var models []bbModel
	if lookup && !o.set["model"] {
		fmt.Fprintf(out, "  Asking BB for %s models (up to 20 seconds)...\n", o.provider)
		var err error
		if models, err = bbModels(w.e.ctx, w.bb, cli, o.provider); err != nil || len(models) == 0 {
			fmt.Fprintln(out, "  BB's model list is unavailable for this provider.")
			models = nil
		}
	}
	var levels []string
	if o.set["model"] {
		if err := check(o.model); err != nil {
			return err
		}
	} else {
		blank := "Blank keeps the thread's own model"
		if sameThread {
			blank = bbModelInUse(cfg, o.thread)
		}
		label, shown := "Model", 0
		show := func() {
			end := min(shown+bbListPage, len(models))
			tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			for i := shown; i < end; i++ {
				note := ""
				if models[i].Default {
					note = "BB's default"
				}
				fmt.Fprintf(tw, "  %d.\t%s\t%s\t%s\n", i+1, models[i].Model, bbLabel(models[i].DisplayName, 24), note)
			}
			tw.Flush()
			if shown = end; shown < len(models) {
				fmt.Fprintf(out, "  Showing %d of %d. Type more for the next %d.\n", shown, len(models), min(bbListPage, len(models)-shown))
			}
		}
		if len(models) > 0 {
			fmt.Fprintf(out, "  Models BB offers for %s:\n", o.provider)
			show()
			label = "Model (a number, or a name)"
			fmt.Fprintf(out, "  A name you type is not checked.\n  %s.\n", blank)
		} else {
			fmt.Fprintf(out, "  Type the model name; it is not checked here.\n  %s.\n", blank)
		}
		for {
			s, err := w.ask(label, "")
			if err != nil {
				return err
			}
			if s == "more" && shown < len(models) {
				show()
				continue
			}
			listed := false
			if n, err := strconv.Atoi(s); err == nil && len(models) > 0 {
				if n < 1 || n > shown {
					fmt.Fprintf(out, "  ! Choose a number between 1 and %d.\n", shown)
					continue
				}
				s, listed, levels = models[n-1].Model, true, models[n-1].levels()
			} else {
				for _, m := range models {
					if m.Model == s {
						listed, levels = true, m.levels()
					}
				}
			}
			if err := check(s); err != nil {
				fmt.Fprintf(out, "  ! %v\n", err)
				continue
			}
			o.model, o.modelListed = s, listed
			break
		}
	}
	if len(levels) == 0 {
		levels = bbReasoningLevels
	}
	return w.askValid("reasoning", "Reasoning level ("+strings.Join(levels, "/")+", blank = unchanged)", "", &o.reasoning, func(s string) error {
		if s == "" {
			return nil
		}
		for _, l := range levels {
			if s == l {
				return nil
			}
		}
		return fmt.Errorf("Choose one of: %s, or leave it blank.", strings.Join(levels, ", "))
	})
}

// printBBCandidate is the preview's "New fallback" block for a BB thread.
// Rows that would only repeat a default are left out to keep it short.
func printBBCandidate(w io.Writer, p *fallbackAddPlan) {
	c, d := p.candidate, p.bb
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "  ID:\t%s\n", c.ID)
	fmt.Fprintf(tw, "  Agent:\tBB thread (provider %s)\n", c.Provider)
	thread := "given by ID"
	if d.title != "" {
		thread = d.title
	}
	switch d.relation {
	case "same_as_primary":
		thread += " (this loop's own conversation)"
	case "same_as_fallback":
		thread += " (already a fallback, on another model)"
	}
	fmt.Fprintf(tw, "  Thread:\t%s\n", thread)
	if d.showIDs {
		fmt.Fprintf(tw, "  Thread ID:\t%s\n", d.thread)
	}
	fmt.Fprintf(tw, "  Account:\tchosen by BB; keepgoing cannot see or switch it\n")
	fmt.Fprintf(tw, "  Quota group:\t%s\n", c.Group)
	model := "the thread's own (not changed)"
	if m := c.Model(); m != "" {
		model = m + " (not checked)"
		if d.modelListed {
			model = m + " (from BB's model list)"
		}
	}
	if c.BB.Reasoning != "" {
		model += ", reasoning " + c.BB.Reasoning
	}
	fmt.Fprintf(tw, "  Model:\t%s\n", model)
	fmt.Fprintf(tw, "  Checked with:\tthe local %s CLI; tests BB's account only if same login\n", c.Probe.Kind)
	if d.probeAccount != "" {
		fmt.Fprintf(tw, "  Check login:\t%s\n", d.probeAccount)
	}
	if c.Probe.Model != "" {
		fmt.Fprintf(tw, "  Check model:\t%s\n", c.Probe.Model)
	}
	if what := bbChanges(c); what != "" {
		fmt.Fprintf(tw, "  At handoff:\tsets the thread's %s (bb thread\n", what)
		fmt.Fprintf(tw, "  \tupdate), then sends the handoff message. Nothing changes now.\n")
	} else {
		fmt.Fprintf(tw, "  At handoff:\tsends the handoff message to the thread as it is.\n")
		fmt.Fprintf(tw, "  \tNothing changes now.\n")
	}
	tw.Flush()
}

// bbChanges names what a handoff changes on the thread before its message.
func bbChanges(c config.Candidate) string {
	switch {
	case c.Model() != "" && c.BB.Reasoning != "":
		return "model and reasoning level"
	case c.Model() != "":
		return "model"
	case c.BB.Reasoning != "":
		return "reasoning level"
	}
	return ""
}

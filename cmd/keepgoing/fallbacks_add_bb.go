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

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// bbLookup runs one read-only bb listing and returns its stdout. The wizard
// calls it only after the user agrees; nothing is sent to any thread.
type bbLookup func(ctx context.Context, cli string, args ...string) ([]byte, error)

const (
	bbLookupTimeout = 20 * time.Second
	bbLookupLimit   = 8 << 20
	bbListPage      = 9
)

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
		if m.Model != "" && fallbackAddModelName("model", m.Model) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// bbLabel makes listed text safe and short for one terminal column.
func bbLabel(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }), " ")
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
	return "", fmt.Errorf("Provider %q has no built-in probe. Guided add supports BB threads\n  on claude-code or codex; others need a hand-written command probe.", provider)
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
	owner, model string
}

func bbThreadUses(cfg *config.Config, thread string) []bbUse {
	var out []bbUse
	if cfg.Loop.BB != nil && cfg.Loop.BB.Thread == thread {
		out = append(out, bbUse{"the primary", cfg.Loop.BB.Model})
	}
	if cfg.FallbackSet != nil {
		for _, c := range cfg.FallbackSet.Candidates {
			if c.BB != nil && c.BB.Thread == thread {
				out = append(out, bbUse{fmt.Sprintf("fallback %q", c.ID), c.BB.Model})
			}
		}
	}
	return out
}

// bbSameThread refuses a second entry for a thread unless its model differs
// from every existing use, which is the only thing a handoff would change.
func bbSameThread(cfg *config.Config, thread, model string) error {
	for _, u := range bbThreadUses(cfg, thread) {
		if model == "" {
			return fmt.Errorf("This thread is already %s. Adding it again only helps with a\n  different model: name the model to switch to.", u.owner)
		}
		if model == u.model {
			return fmt.Errorf("This thread is already %s on model %s. Choose a different\n  model, or another thread.", u.owner, model)
		}
	}
	return nil
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
	if err := bbSameThread(cfg, thread, o.model); err != nil {
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

	var warnings []string
	uses := bbThreadUses(cfg, thread)
	if len(uses) > 0 {
		warnings = append(warnings, fmt.Sprintf("Same conversation as %s. A handoff switches this thread to model %s and continues in it, with the same history. It is almost certainly the same account, so it helps only if that model has its own limit.", uses[0].owner, o.model))
		for _, u := range uses {
			if u.model == "" {
				warnings = append(warnings, fmt.Sprintf("The model of %s is not set in its configuration, so it cannot be compared with %s, and returning to it would leave the thread on %s. Set its bb.model to make both explicit.", u.owner, o.model, o.model))
			}
		}
	} else {
		warnings = append(warnings, "keepgoing cannot see which account BB uses for this thread. If it shares a usage limit with another target, give them the same quota group.")
	}
	if o.model == "" {
		warnings = append(warnings, fmt.Sprintf("No model is set, so the thread keeps its own. The check before a handoff then uses the local %s CLI's default model, which may differ.", kind))
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
		if lookup, err = w.askYesNo("Ask BB for the lists? (yes/no)", "yes"); err != nil {
			return err
		}
	}
	if err := w.pickBBThread(cli, lookup); err != nil {
		return err
	}
	if _, known := bbProbeKind(o.provider); known != nil || o.set["provider"] {
		err = w.askValid("provider", "Provider of that thread (claude-code/codex)", "", &o.provider, func(s string) error {
			_, err := bbProbeKind(s)
			return err
		})
		if err != nil {
			return err
		}
	}
	kind, _ := bbProbeKind(o.provider)
	same := bbThreadUses(cfg, o.thread)
	if len(same) > 0 {
		fmt.Fprintf(out, "  Note: this is the same conversation as %s. A handoff will switch its\n  model and continue in it; you will choose that model next.\n", same[0].owner)
	} else {
		fmt.Fprintln(out, "  keepgoing cannot see which account BB uses for this thread.")
	}

	fmt.Fprintln(out, "\nStep 3 of 4: Model")
	if err := w.pickBBModel(cli, lookup, len(same) > 0); err != nil {
		return err
	}
	fmt.Fprintln(out, "  A quota group names one usage limit. Targets that share a limit must share")
	fmt.Fprintf(out, "  a group; a fallback must not use the primary's group (%s).\n", cfg.Loop.Group)
	if err := w.askValid("group", "Quota group", bbDefaultGroup(cfg, o.provider, o.model), &o.group, func(s string) error { return fallbackAddGroup(cfg, s) }); err != nil {
		return err
	}
	fmt.Fprintf(out, "  Before any handoff, the fallback is checked with one small request (a\n  probe) from the local %s CLI. That confirms BB's account only if both\n  use the same login.\n", kind)
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
	show := func() {
		end := min(shown+bbListPage, len(threads))
		tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
		for i := shown; i < end; i++ {
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
				row += "\t" + t.ID
			}
			fmt.Fprintln(tw, row)
		}
		tw.Flush()
		shown = end
	}
	label := "Thread ID"
	if len(threads) > 0 {
		fmt.Fprintln(out, "  Your BB threads (listed by bb; nothing was sent):")
		show()
		label = "Thread (a number, or a thread ID)"
		hint := "  Type ids to show thread IDs"
		if shown < len(threads) {
			hint += fmt.Sprintf(", or more for the next %d of %d", min(bbListPage, len(threads)-shown), len(threads))
		}
		fmt.Fprintln(out, hint+".")
	}
	for {
		s, err := w.ask(label, "")
		if err != nil {
			return err
		}
		switch {
		case s == "more" && shown < len(threads):
			show()
			continue
		case s == "ids" && len(threads) > 0:
			ids, shown = true, 0
			show()
			continue
		}
		if n, err := strconv.Atoi(s); err == nil && len(threads) > 0 {
			if n < 1 || n > shown {
				fmt.Fprintf(out, "  ! Choose a number between 1 and %d.\n", shown)
				continue
			}
			t := threads[n-1]
			if _, err := bbProbeKind(t.Provider); err != nil && !o.set["provider"] {
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
		o.thread = s
		for _, t := range threads {
			if t.ID == s {
				if t.Title != nil {
					o.threadTitle = bbLabel(*t.Title, 44)
				}
				if !o.set["provider"] {
					o.provider = t.Provider
				}
			}
		}
		return nil
	}
}

func (w *fallbackAddWizard) pickBBModel(cli string, lookup, sameThread bool) error {
	out, cfg, o := w.e.stdout, w.cfg, w.o
	check := func(s string) error {
		if err := fallbackAddModelName("model", s); err != nil {
			return err
		}
		return bbSameThread(cfg, o.thread, s)
	}
	var models []bbModel
	if lookup && !o.set["model"] {
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
		blank := "blank keeps the thread's own model"
		if sameThread {
			blank = "it must differ from the model already in use"
		}
		label := "Model"
		if len(models) > 0 {
			if len(models) > 2*bbListPage {
				models = models[:2*bbListPage]
			}
			fmt.Fprintf(out, "  Models BB offers for %s:\n", o.provider)
			tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			for i, m := range models {
				note := ""
				if m.Default {
					note = "BB's default"
				}
				fmt.Fprintf(tw, "  %d.\t%s\t%s\t%s\n", i+1, bbLabel(m.Model, 30), bbLabel(m.DisplayName, 24), note)
			}
			tw.Flush()
			label = "Model (a number, or a name)"
			fmt.Fprintf(out, "  A name you type is not checked; %s.\n", blank)
		} else {
			fmt.Fprintf(out, "  Type the model name. It is not checked here; %s.\n", blank)
		}
		for {
			s, err := w.ask(label, "")
			if err != nil {
				return err
			}
			listed := false
			if n, err := strconv.Atoi(s); err == nil && len(models) > 0 {
				if n < 1 || n > len(models) {
					fmt.Fprintf(out, "  ! Choose a number between 1 and %d.\n", len(models))
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
	fmt.Fprintf(tw, "  Model:\t%s\n", model)
	reasoning := "unchanged"
	if c.BB.Reasoning != "" {
		reasoning = c.BB.Reasoning
	}
	fmt.Fprintf(tw, "  Reasoning:\t%s\n", reasoning)
	account := "that CLI's current login"
	if d.probeAccount != "" {
		account = d.probeAccount
	}
	fmt.Fprintf(tw, "  Checked with:\tthe local %s CLI\n", c.Probe.Kind)
	fmt.Fprintf(tw, "  Check login:\t%s\n", account)
	probeModel := "same as the model"
	if c.Probe.Model != "" {
		probeModel = c.Probe.Model
	}
	fmt.Fprintf(tw, "  Check model:\t%s\n", probeModel)
	tw.Flush()
	fmt.Fprintln(w, "\nAt a handoff")
	if what := bbChanges(c); what != "" {
		fmt.Fprintf(w, "  keepgoing sets the thread's %s with bb thread update,\n", what)
		fmt.Fprintln(w, "  then sends the handoff message. Nothing is sent or changed now.")
	} else {
		fmt.Fprintln(w, "  keepgoing sends the handoff message to the thread as it is. Nothing is")
		fmt.Fprintln(w, "  sent or changed now.")
	}
	fmt.Fprintln(w, "  The check passing shows the local CLI's login works. It shows BB's account")
	fmt.Fprintln(w, "  works only if BB uses that same login.")
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

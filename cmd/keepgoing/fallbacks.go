package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

type candidateRow struct {
	ID       string        `json:"id"`
	Provider string        `json:"provider"`
	Group    string        `json:"group"`
	Target   string        `json:"target"`
	Disabled bool          `json:"disabled,omitempty"`
	Healthy  bool          `json:"healthy"`
	Last     *probe.Result `json:"last,omitempty"`
}

type fallbacksOut struct {
	Candidates []candidateRow `json:"candidates"`
	Redundancy health.Summary `json:"redundancy"`
}

func needFallbacks(e env, cfg *config.Config) bool {
	if cfg.FallbackSet == nil {
		e.errf("no fallbacks file configured (set loop.fallbacks)")
		return false
	}
	return true
}

// fallbacksCheck probes candidates now. Manual probes do not count toward
// the scheduled daily cap. Exit 1 when the redundancy rule is not met, so a
// scheduler or CI job can alert on it.
func fallbacksCheck(e env, cfg *config.Config, now time.Time, only string, exact, asJSON bool) int {
	if !needFallbacks(e, cfg) {
		return exitUsage
	}
	fb := cfg.FallbackSet
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		e.errf("a tick is running; try again in a moment")
		return exitSkipped
	}
	if err != nil {
		e.errf("lock: %v", err)
		return exitFailed
	}
	defer l.Release()
	reg, err := health.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	tier := probe.Manual
	if exact {
		tier = probe.Exact
	}
	ran := 0
	for _, c := range fb.Candidates {
		if (only != "" && c.ID != only) || (only == "" && c.Disabled) {
			continue
		}
		ctx, cancel := context.WithCancel(e.ctx)
		r := probe.Run(ctx, c, tier, fb.Probe.Timeout.Duration)
		cancel()
		reg.Record(c.ID, r)
		if err := probe.KeepFailure(cfg.Loop.StateDir, c.ID, r); err != nil {
			e.errf("warning: could not keep failed probe output for %s: %v", c.ID, err)
		}
		ran++
	}
	if only != "" && ran == 0 {
		e.errf("no fallback candidate %q", only)
		return exitUsage
	}
	if err := reg.Save(cfg.Loop.StateDir); err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	out := summarize(cfg, reg, time.Now())
	printFallbacks(e, out, asJSON)
	if !out.Redundancy.Meets {
		return exitFailed
	}
	return exitOK
}

func fallbacksList(e env, cfg *config.Config, now time.Time, asJSON bool) int {
	if !needFallbacks(e, cfg) {
		return exitUsage
	}
	reg, err := health.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	printFallbacks(e, summarize(cfg, reg, now), asJSON)
	return exitOK
}

func summarize(cfg *config.Config, reg *health.Registry, now time.Time) fallbacksOut {
	fb := cfg.FallbackSet
	st, _ := state.Load(cfg.Loop.StateDir)
	group := cfg.Loop.Group
	if c, ok := fb.Find(st.Target()); ok {
		group = c.Group
	}
	out := fallbacksOut{Redundancy: health.Evaluate(fb, reg, now, st.Target(), group)}
	for _, c := range fb.Candidates {
		row := candidateRow{ID: c.ID, Provider: c.Provider, Group: c.Group, Target: c.Describe(), Disabled: c.Disabled, Healthy: reg.Healthy(c.ID, now, fb.Probe.TTL.Duration)}
		if en, ok := reg.Candidates[c.ID]; ok {
			row.Last = en.Last
		}
		out.Candidates = append(out.Candidates, row)
	}
	return out
}

func printFallbacks(e env, out fallbacksOut, asJSON bool) {
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return
	}
	tw := tabwriter.NewWriter(e.stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROVIDER\tGROUP\tHEALTHY\tLAST PROBE\tRESULT\tLATENCY")
	for _, r := range out.Candidates {
		last, result, latency := "never", "", ""
		if r.Last != nil {
			last = r.Last.At.UTC().Format("2006-01-02 15:04") + " " + string(r.Last.Tier)
			result = "ok"
			if !r.Last.OK {
				result = r.Last.ErrorClass + " (" + r.Last.Detail + ")"
			}
			latency = fmt.Sprintf("%dms", r.Last.LatencyMs)
		}
		healthy := "yes"
		switch {
		case r.Disabled:
			healthy = "disabled"
		case !r.Healthy:
			healthy = "no"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Provider, r.Group, healthy, last, result, latency)
	}
	tw.Flush()
	s := out.Redundancy
	verdict := "handoffs allowed"
	if !s.Meets {
		verdict = "handoffs refused"
	}
	fmt.Fprintf(e.stdout, "\n%d healthy of %d eligible, %d distinct by %s, need %d: %s\n", len(s.Healthy), len(s.Eligible), s.Distinct, s.Span, s.Min, verdict)
}

func reportTest(e env, cfg *config.Config, dryRun bool) int {
	r := cfg.Loop.Report
	if r == nil {
		e.errf("no report configured (set loop.report)")
		return exitUsage
	}
	w, err := report.New(*r)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	ev := report.Event{Type: "test", Loop: cfg.Loop.Name, Text: cfg.Loop.Name + ": test report from keepgoing", At: time.Now(), Data: map[string]any{"test": true}}
	if dryRun {
		body, err := w.Body(ev)
		if err != nil {
			e.errf("%v", err)
			return exitFailed
		}
		fmt.Fprintln(e.stdout, string(body))
		return exitOK
	}
	// Force delivery regardless of the subscribed events.
	all := *r
	all.Events = []string{"all"}
	w, _ = report.New(all)
	if err := w.Report(e.ctx, ev); err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	fmt.Fprintln(e.stdout, "report delivered")
	return exitOK
}

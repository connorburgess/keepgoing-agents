package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/looptender/looptender/internal/prbacklog"
)

func prBacklogCommand(e env, args []string, run prbacklog.Query) int {
	fs := flag.NewFlagSet("pr-backlog", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	repo := fs.String("repo", "", "explicit github.com OWNER/NAME")
	maxOpen := fs.Int("max-open", 0, "WIP threshold, including all open PRs (1..5000)")
	held := fs.String("held-label", "", "exact label declaring held work")
	superseded := fs.String("superseded-label", "", "exact label declaring superseded work")
	asJSON := fs.Bool("json", false, "print structured evidence")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return exitUsage
	}
	p := prbacklog.Policy{Repo: *repo, MaxOpen: *maxOpen, HeldLabel: *held, SupersededLabel: *superseded, Interval: 15 * time.Minute, MaxAge: time.Hour}
	if err := p.Validate(); err != nil {
		e.errf("pr-backlog: %v", err)
		return exitUsage
	}
	if run == nil {
		run = prbacklog.ExecGH("gh")
	}
	r := prbacklog.Collect(e.ctx, run, p, time.Now)
	c := prbacklog.Update(nil, p, r, r.FinishedAt)
	notice, _ := prbacklog.Notice(p, c, r.FinishedAt)
	if *asJSON {
		out := struct {
			prbacklog.Report
			MaxOpen int    `json:"maxOpen"`
			Notice  string `json:"notice"`
		}{r, p.MaxOpen, notice}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprintf(e.stdout, "PR backlog %s: complete=%t failure=%s held-label-configured=%t superseded-label-configured=%t\n", p.Repo, r.Complete, r.Failure, r.HeldLabelConfigured, r.SupersededLabelConfigured)
		for _, row := range r.Rows {
			fmt.Fprintf(e.stdout, "  #%d created=%s updated=%s +%d -%d files=%d draft=%t held=%t superseded=%t merge=%s review=%s branch-dependencies=%v\n", row.Number, row.CreatedAt.Format(time.RFC3339), row.UpdatedAt.Format(time.RFC3339), row.Additions, row.Deletions, row.Files, row.Draft, row.Held, row.Superseded, row.MergeState, row.Review, row.DependsOn)
		}
		fmt.Fprint(e.stdout, notice)
	}
	if !r.Complete || r.Summary == nil || r.Summary.Open >= p.MaxOpen {
		return exitFailed
	}
	return exitOK
}

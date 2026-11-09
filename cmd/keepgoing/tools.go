package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/ciwait"
	"github.com/connorburgess/keepgoing-agents/internal/netretry"
)

func ciWait(e env, args []string) int {
	fs := flag.NewFlagSet("ci-wait", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	repo := fs.String("repo", "", "owner/name")
	pr := fs.Int("pr", 0, "pull request number (uses its current head commit)")
	sha := fs.String("sha", "", "exact commit, with --branch")
	branch := fs.String("branch", "", "branch the commit's runs are on, with --sha")
	interval := fs.Duration("interval", 30*time.Second, "poll interval")
	timeout := fs.Duration("timeout", time.Hour, "give up after this long")
	noRuns := fs.Duration("no-runs-after", 5*time.Minute, "give up if no run appears for the commit")
	gh := fs.String("gh", "gh", "path to the gh CLI")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rep, err := ciwait.Wait(e.ctx, ciwait.ExecGH(*gh), ciwait.Options{
		Repo: *repo, PR: *pr, SHA: *sha, Branch: *branch,
		Interval: *interval, Timeout: *timeout, NoRunsAfter: *noRuns,
		Net: netretry.Options{Log: func(s string) { e.errf("net-retry: %s", s) }},
		Log: func(s string) { e.errf("ci-wait: %s", s) },
	})
	if err != nil {
		e.errf("ci-wait: %v", err)
		return exitFailed
	}
	if *asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprintf(e.stdout, "%s %.7s on %s: %s\n", rep.Repo, rep.SHA, rep.Branch, rep.Conclusion)
		for _, r := range rep.Runs {
			fmt.Fprintf(e.stdout, "  %s run %d: %s\n", r.Workflow, r.ID, r.Conclusion)
			for _, j := range r.Jobs {
				fmt.Fprintf(e.stdout, "    %s: %s\n", j.Name, j.Conclusion)
			}
		}
	}
	return rep.ExitCode()
}

func netRetry(e env, args []string) int {
	fs := flag.NewFlagSet("net-retry", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	maxWait := fs.Duration("max-wait", 2*time.Hour, "total time to wait for the network")
	attempts := fs.Int("attempts", 6, "total runs, including the first")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	argv := fs.Args()
	if len(argv) == 0 {
		e.errf("usage: keepgoing net-retry [--max-wait 2h] [--attempts 6] -- command [args...]")
		return exitUsage
	}
	return netretry.Command(e.ctx, netretry.Options{MaxWait: *maxWait, Attempts: *attempts, Log: func(s string) { e.errf("net-retry: %s", s) }}, argv)
}

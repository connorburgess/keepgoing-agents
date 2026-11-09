// Package ciwait waits for every CI workflow on an exact commit to finish,
// using the gh CLI so no token handling lives here. When a run is
// superseded, for example cancelled by a pull request edit, the newest run
// of the same workflow on the same commit is followed instead.
package ciwait

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/netretry"
)

// GH runs gh with args and returns stdout.
type GH func(ctx context.Context, args ...string) ([]byte, error)

// ExecGH runs the gh binary at path.
func ExecGH(path string) GH {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).Output()
	}
}

// Options select the commit and bound the wait.
type Options struct {
	Repo        string
	PR          int
	SHA         string
	Branch      string
	Interval    time.Duration
	Timeout     time.Duration
	NoRunsAfter time.Duration // give up if no run appears for the commit
	Net         netretry.Options
	Sleep       func(ctx context.Context, d time.Duration)
	Now         func() time.Time
	Log         func(string)
}

// Job is one job's outcome.
type Job struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// Run is one workflow run.
type Run struct {
	ID         int64   `json:"id"`
	Workflow   string  `json:"workflow"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	URL        string  `json:"url,omitempty"`
	Jobs       []Job   `json:"jobs,omitempty"`
	Superseded []int64 `json:"superseded,omitempty"`
}

// Report is the result.
type Report struct {
	Repo       string `json:"repo"`
	PR         int    `json:"pr,omitempty"`
	SHA        string `json:"sha"`
	Branch     string `json:"branch"`
	Conclusion string `json:"conclusion"` // success, failure, no_runs, timeout
	Runs       []Run  `json:"runs"`
}

// Exit codes.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitNoRuns  = 3
	ExitTimeout = 124
)

// ExitCode maps a report to the command's exit status.
func (r Report) ExitCode() int {
	switch r.Conclusion {
	case "success":
		return ExitSuccess
	case "no_runs":
		return ExitNoRuns
	case "timeout":
		return ExitTimeout
	}
	return ExitFailure
}

func (o *Options) fill() {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = time.Hour
	}
	if o.NoRunsAfter <= 0 {
		o.NoRunsAfter = 5 * time.Minute
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
}

// Wait polls until every workflow's newest run on the commit has completed.
func Wait(ctx context.Context, gh GH, o Options) (Report, error) {
	o.fill()
	call := func(args ...string) ([]byte, error) {
		var out []byte
		err := netretry.Do(ctx, o.Net, func() error {
			var err error
			out, err = gh(ctx, args...)
			return err
		})
		return out, err
	}
	if o.Repo == "" {
		return Report{}, errors.New("ci-wait: --repo is required")
	}
	if o.PR > 0 {
		out, err := call("pr", "view", strconv.Itoa(o.PR), "--repo", o.Repo, "--json", "headRefName,headRefOid")
		if err != nil {
			return Report{}, fmt.Errorf("gh pr view: %w", err)
		}
		var pr struct {
			HeadRefName string `json:"headRefName"`
			HeadRefOid  string `json:"headRefOid"`
		}
		if err := json.Unmarshal(out, &pr); err != nil || pr.HeadRefOid == "" {
			return Report{}, errors.New("gh pr view: unexpected JSON")
		}
		o.SHA, o.Branch = pr.HeadRefOid, pr.HeadRefName
	}
	if o.SHA == "" || o.Branch == "" {
		return Report{}, errors.New("ci-wait: give --pr, or both --sha and --branch")
	}
	rep := Report{Repo: o.Repo, PR: o.PR, SHA: o.SHA, Branch: o.Branch}
	start := o.Now()
	for {
		out, err := call("run", "list", "--repo", o.Repo, "--branch", o.Branch, "--limit", "50",
			"--json", "databaseId,headSha,status,conclusion,workflowName,url")
		if err != nil {
			return rep, fmt.Errorf("gh run list: %w", err)
		}
		var all []struct {
			ID         int64  `json:"databaseId"`
			HeadSha    string `json:"headSha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Workflow   string `json:"workflowName"`
			URL        string `json:"url"`
		}
		if err := json.Unmarshal(out, &all); err != nil {
			return rep, errors.New("gh run list: unexpected JSON")
		}
		latest := map[string]*Run{}
		for _, r := range all {
			if r.HeadSha != o.SHA {
				continue
			}
			cur, ok := latest[r.Workflow]
			if ok && cur.ID > r.ID {
				cur.Superseded = append(cur.Superseded, r.ID)
				continue
			}
			nr := &Run{ID: r.ID, Workflow: r.Workflow, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL}
			if ok {
				nr.Superseded = append(cur.Superseded, cur.ID)
			}
			latest[r.Workflow] = nr
		}
		elapsed := o.Now().Sub(start)
		if len(latest) == 0 && elapsed >= o.NoRunsAfter {
			rep.Conclusion = "no_runs"
			return rep, nil
		}
		done := len(latest) > 0
		for _, r := range latest {
			if r.Status != "completed" {
				done = false
			}
		}
		if done {
			rep.Runs = nil
			names := make([]string, 0, len(latest))
			for n := range latest {
				names = append(names, n)
			}
			sort.Strings(names)
			rep.Conclusion = "success"
			for _, n := range names {
				r := latest[n]
				if out, err := call("run", "view", strconv.FormatInt(r.ID, 10), "--repo", o.Repo, "--json", "jobs"); err == nil {
					var v struct {
						Jobs []Job `json:"jobs"`
					}
					if json.Unmarshal(out, &v) == nil {
						r.Jobs = v.Jobs
					}
				}
				if !passing(r.Conclusion) {
					rep.Conclusion = "failure"
				}
				rep.Runs = append(rep.Runs, *r)
			}
			return rep, nil
		}
		if elapsed >= o.Timeout {
			rep.Conclusion = "timeout"
			for _, r := range latest {
				rep.Runs = append(rep.Runs, *r)
			}
			return rep, nil
		}
		o.Log(fmt.Sprintf("waiting on %d workflow run(s) for %.7s", len(latest), o.SHA))
		o.Sleep(ctx, o.Interval)
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
	}
}

func passing(conclusion string) bool {
	switch conclusion {
	case "success", "skipped", "neutral":
		return true
	}
	return false
}

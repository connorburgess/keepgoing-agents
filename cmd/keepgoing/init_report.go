package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

const initTaskLimit = 1 << 20

// Do not expose reader errors: an integration's reader may include task text in
// them. Cancellation can return while stdin is still blocked, as in the wizard;
// the CLI owns its process lifetime, not the supplied reader.
func readInitTask(ctx context.Context, input io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, initTaskLimit+1))
		done <- result{data, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if r.err != nil {
			return "", errors.New("could not read task from stdin")
		}
		if len(r.data) > initTaskLimit {
			return "", errors.New("stdin task exceeds 1 MiB")
		}
		if !utf8.Valid(r.data) {
			return "", errors.New("stdin task must be UTF-8 text")
		}
		return string(r.data), nil
	}
}

// Success-only integration contract. Task/prompt text, environment values,
// account-directory paths, and raw configuration never enter this report.
// Next commands are argv arrays, not shell strings, and are never executed here.
type initReport struct {
	SchemaVersion int               `json:"schemaVersion"`
	Version       string            `json:"version"`
	Outcome       string            `json:"outcome"` // preview or created
	Loop          string            `json:"loop"`
	Project       string            `json:"project"`
	ConfigPath    string            `json:"configPath"`
	StateDir      string            `json:"stateDir"`
	PromptPath    string            `json:"promptPath"`
	TaskSource    string            `json:"taskSource"` // stdin, file, inline
	Primary       initTarget        `json:"primary"`
	Fallbacks     []initFallbackRef `json:"fallbacks"`
	Limits        initLimits        `json:"limits"`
	Probes        *initProbePolicy  `json:"probes,omitempty"`
	Next          initNextCommands  `json:"next"`
	Limitations   []string          `json:"limitations"`
}

type initTarget struct {
	Agent   agentRef `json:"agent"`
	Account string   `json:"account"` // inherited or explicit_directory
}
type initFallbackRef struct {
	ID     string     `json:"id"`
	Target initTarget `json:"target"`
}
type initLimits struct {
	WakeEvery    string   `json:"wakeEvery"`
	StallAfter   string   `json:"stallAfter"`
	Interval     string   `json:"interval"`
	Backoff      string   `json:"backoff"`
	MaxNudges    int      `json:"maxNudges"`
	MaxReprompts int      `json:"maxReprompts"`
	FailoverOn   []string `json:"failoverOn"`
}
type initProbePolicy struct {
	Every                string `json:"every"`
	TTL                  string `json:"ttl"`
	DailyCapPerCandidate int    `json:"dailyCapPerCandidate"`
	ExactOutsideCap      bool   `json:"exactOutsideCap"`
	ManualOutsideCap     bool   `json:"manualOutsideCap"`
}
type initNextCommands struct {
	Doctor          []string `json:"doctor"`
	StartForeground []string `json:"startForeground"`
	StartBackground []string `json:"startBackground"`
}

func initTargetOf(a config.Agent) initTarget {
	t := initTarget{Agent: refOf(a), Account: "inherited"}
	if (a.Claude != nil && len(a.Claude.Env) > 0) || (a.Codex != nil && len(a.Codex.Env) > 0) {
		t.Account = "explicit_directory"
	}
	return t
}

func printInitJSON(e env, o initOptions, cfg *config.Config) int {
	cfgPath := filepath.Join(o.directory, "keepgoing.yaml")
	r := initReport{
		SchemaVersion: 1, Version: version, Outcome: "created", Loop: o.name,
		Project: o.project, ConfigPath: cfgPath, StateDir: cfg.Loop.StateDir,
		PromptPath: filepath.Join(o.directory, "prompt.md"), TaskSource: "inline",
		Primary: initTargetOf(cfg.Loop.Agent), Fallbacks: []initFallbackRef{},
		Limits: initLimits{o.wake.String(), o.stall.String(), o.interval.String(), o.backoff.String(), o.nudges, o.reprompts, []string{}},
		Next: initNextCommands{
			Doctor:          []string{keepgoingExecutable(), "doctor", "--json", "-c", cfgPath},
			StartForeground: []string{keepgoingExecutable(), "start", "--foreground", "-c", cfgPath},
			StartBackground: []string{keepgoingExecutable(), "start", "--background", "-c", cfgPath},
		},
		Limitations: []string{
			"Setup leaves supervision disabled; no agents, probes, logins, or schedulers are started.",
			"Target identity, account access, quota independence, and provider health have not been verified.",
			"Existing provider permissions are inherited; setup does not change models or logins on BB threads.",
			"Review the task, target, and configuration before explicitly starting supervision. Other timers are not detected or changed.",
		},
	}
	if o.dryRun {
		r.Outcome = "preview"
	}
	if o.taskFile == "-" {
		r.TaskSource = "stdin"
	} else if o.taskFile != "" {
		r.TaskSource = "file"
	}
	if fs := cfg.FallbackSet; fs != nil {
		for _, c := range fs.Candidates {
			r.Fallbacks = append(r.Fallbacks, initFallbackRef{ID: c.ID, Target: initTargetOf(c.Agent)})
		}
		r.Limits.FailoverOn = []string{"rate_limit"}
		r.Probes = &initProbePolicy{fs.Probe.Every.String(), fs.Probe.TTL.String(), fs.Probe.DailyCap, true, true}
	}
	if err := json.NewEncoder(e.stdout).Encode(r); err != nil {
		e.errf("init: could not write JSON result; setup files may already exist")
		return exitFailed
	}
	return exitOK
}

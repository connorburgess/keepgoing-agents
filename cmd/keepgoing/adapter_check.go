package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/conformance"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// adapter check runs the adapter contract against a loop's real agent. By
// default it only reads status. It sends a prompt only with --send, and then
// only while the loop cannot act on the result.

type adapterCheckReport struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Adapter       string               `json:"adapter,omitempty"`
	Target        string               `json:"target,omitempty"`
	Sent          bool                 `json:"sent"`
	Declared      conformance.Declared `json:"declared"`
	Results       []conformance.Result `json:"results"`
}

func adapterCommand(e env, args []string) int {
	if len(args) == 0 || args[0] != "check" {
		e.errf("usage: keepgoing adapter check [-c keepgoing.yaml] [--target primary|ID] [--send] [--list] [--json]")
		return exitUsage
	}
	fs := flag.NewFlagSet("adapter check", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Never echo parser or config values.
	path := fs.String("c", "keepgoing.yaml", "path to the config file")
	target := fs.String("target", state.Primary, "primary, or the id of a fallback candidate")
	send := fs.Bool("send", false, "also send one fixed check prompt to the real agent; the loop must be held or disabled")
	list := fs.Bool("list", false, "print every contract without reading a configuration")
	asJSON := fs.Bool("json", false, "print the versioned report")
	limit := fs.Duration("limit", 30*time.Second, "longest one status read may take")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("adapter check: invalid arguments; see adapter check --help")
		return exitUsage
	}
	if fs.NArg() != 0 || *limit <= 0 || (*list && *send) {
		e.errf("adapter check: no positional arguments; a positive --limit; --list cannot be combined with --send")
		return exitUsage
	}
	r := adapterCheckReport{SchemaVersion: 1}
	if *list {
		r.Results = conformance.Cases()
		return printAdapterCheck(e, r, *asJSON, true)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("adapter check: configuration unavailable or invalid; run validate for details")
		return exitUsage
	}
	agent := cfg.Loop.Agent
	if *target != state.Primary {
		candidate, ok := cfg.FallbackSet.Find(*target)
		if !ok {
			e.errf("adapter check: no fallback candidate has that id")
			return exitUsage
		}
		agent = candidate.Agent
	}
	if *send {
		// A prompt changes what the next check sees. Hold the tick lock so no
		// check runs meanwhile, and require that the loop would not act.
		held, err := lock.Acquire(cfg.Loop.StateDir)
		if err != nil {
			e.errf("adapter check: a check is running or the daemon holds the loop; try again")
			return exitSkipped
		}
		defer held.Release()
		ctl, err := control.Load(cfg.Loop.StateDir)
		if err != nil {
			e.errf("adapter check: the control record cannot be read; nothing was sent")
			return exitFailed
		}
		if ctl.Enabled && !ctl.Paused && !ctl.Stop && !ctl.AwaitingUser && !ctl.Completed {
			e.errf("adapter check: --send needs the loop paused, held or disabled, so supervision does not act on the check prompt; nothing was sent")
			return exitUsage
		}
	}
	r.Adapter, r.Target, r.Sent = agent.Adapter, *target, *send
	r.Declared = declaredFor(agent)
	subject := conformance.Subject{Name: agent.Adapter, Declared: r.Declared, CallLimit: *limit,
		New: func() (adapter.Adapter, error) { return factory.New(agent, cfg.Loop.StateDir, *target) }}
	r.Results = conformance.Run(e.ctx, subject, conformance.Options{Send: *send})
	return printAdapterCheck(e, r, *asJSON, false)
}

// declaredFor is what each adapter kind says about itself. An agent on the
// runner contract carries its own declaration.
func declaredFor(a config.Agent) conformance.Declared {
	if spec, ok := runner.SpecFor(a.Adapter); ok {
		c := spec.Capabilities
		return conformance.Declared{ExactResume: c.ExactResume, Stoppable: c.Stoppable, StructuredErrors: c.StructuredErrors, StructuredWaiting: c.StructuredWaiting}
	}
	switch a.Adapter {
	case "bb":
		return conformance.Declared{Stoppable: true, StructuredErrors: true, StructuredWaiting: true}
	case "cursor":
		return conformance.Declared{ExactResume: true, Stoppable: true}
	case "process":
		return conformance.Declared{Stoppable: a.Process != nil && len(a.Process.StopCommand) > 0, StructuredErrors: true, StructuredWaiting: true}
	}
	return conformance.Declared{}
}

func printAdapterCheck(e env, r adapterCheckReport, asJSON, listing bool) int {
	failed := false
	for _, res := range r.Results {
		failed = failed || res.Outcome == conformance.Fail
	}
	if asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if enc.Encode(r) != nil {
			return exitFailed
		}
	} else {
		if listing {
			fmt.Fprintln(e.stdout, "The adapter contract. Every adapter, built in or your own, is run against these:")
		} else {
			fmt.Fprintf(e.stdout, "Adapter contract check: %s adapter, target %s.\n", r.Adapter, r.Target)
			if r.Sent {
				fmt.Fprintln(e.stdout, "One check prompt was sent to the real agent.")
			} else {
				fmt.Fprintln(e.stdout, "Status was read; nothing was sent. Cases that need a stand-in agent run in the test suite, not here.")
			}
			fmt.Fprintf(e.stdout, "declares: exact resume %t, can be stopped %t, structured errors %t, structured waiting %t\n",
				r.Declared.ExactResume, r.Declared.Stoppable, r.Declared.StructuredErrors, r.Declared.StructuredWaiting)
		}
		for _, res := range r.Results {
			fmt.Fprintf(e.stdout, "\n%-8s %s\n  %s\n", res.Outcome, res.ID, res.Contract)
			if res.Detail != "" {
				fmt.Fprintf(e.stdout, "  %s\n", res.Detail)
			}
		}
	}
	if failed {
		return exitFailed
	}
	return exitOK
}

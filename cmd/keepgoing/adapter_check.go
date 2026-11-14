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
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// adapter check runs the part of the adapter contract that only reads against
// a loop's real agent: one status read. It never sends a prompt, stops
// anything or writes a file. An adapter whose status read is not free of side
// effects is refused with the reason, before anything is built or run.

type adapterCheckReport struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Adapter       string               `json:"adapter,omitempty"`
	Target        string               `json:"target,omitempty"`
	Declared      conformance.Declared `json:"declared"`
	Results       []conformance.Result `json:"results"`
}

func adapterCommand(e env, args []string) int {
	if len(args) == 0 || args[0] != "check" {
		e.errf("usage: keepgoing adapter check [-c keepgoing.yaml] [--target primary|ID] [--list] [--json] [--limit 30s]")
		return exitUsage
	}
	fs := flag.NewFlagSet("adapter check", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Never echo parser or config values.
	path := fs.String("c", "keepgoing.yaml", "path to the config file")
	target := fs.String("target", state.Primary, "primary, or the id of a fallback candidate")
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
	if fs.NArg() != 0 || *limit <= 0 {
		e.errf("adapter check: no positional arguments, and a positive --limit")
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
	if reason := notReadOnly(agent.Adapter); reason != "" {
		e.errf("adapter check: not run for the %s adapter: %s Nothing was read, started or written. Its contract is checked in the test suite; see adapter check --list.", agent.Adapter, reason)
		return exitUsage
	}
	r.Adapter, r.Target = agent.Adapter, *target
	r.Declared = declaredFor(agent)
	subject := conformance.Subject{Name: agent.Adapter, Declared: r.Declared, CallLimit: *limit,
		New: func() (adapter.Adapter, error) { return factory.New(agent, cfg.Loop.StateDir, *target) }}
	r.Results = conformance.Run(e.ctx, subject, conformance.Options{})
	return printAdapterCheck(e, r, *asJSON, false)
}

// notReadOnly says why an adapter's status read is not free of side effects,
// or "" when it is. Only the process adapter's is: it runs the loop's own
// status command and keeps nothing. An adapter that is not listed is refused,
// so a new one is not read until someone has shown that reading it is safe.
func notReadOnly(kind string) string {
	switch kind {
	case "process":
		return ""
	case "bb":
		return "reading a BB thread's status advances the loop's saved position in the thread's log and takes the notices a check is waiting for."
	case "codex":
		return "reading a failed Codex turn starts the Codex app-server to ask about limits, and a status read saves the session a finished turn reported."
	case "claude", "cursor":
		return "a status read saves the session a finished turn reported, which belongs to a check."
	}
	return "its status read has not been shown to be free of side effects."
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
			fmt.Fprintln(e.stdout, "The status command was run once. Nothing was sent, stopped or written. Cases that need a stand-in agent run in the test suite, not here.")
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

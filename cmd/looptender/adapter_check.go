package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/adapter/conformance"
	"github.com/looptender/looptender/internal/adapter/factory"
	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/state"
)

// adapter check attempts one configured process status invocation. It never
// invokes resume or stop and does not itself write loop state. The status
// program is not sandboxed; its external behavior belongs to that program.
// Other adapter kinds are refused before construction.

type adapterCheckReport struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Adapter       string               `json:"adapter,omitempty"`
	Target        string               `json:"target,omitempty"`
	Declared      conformance.Declared `json:"declared"`
	Results       []conformance.Result `json:"results"`
}

func adapterCommand(e env, args []string) int {
	if len(args) == 0 || args[0] != "check" {
		e.errf("usage: keepgoing adapter check [-c FILE] [--target primary|ID] [--list] [--json] [--limit 30s]")
		return exitUsage
	}
	fs := flag.NewFlagSet("adapter check", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Never echo parser or config values.
	path := fs.String("c", "", configFlagHelp("path to the config file"))
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
	if !configDefault(e, "adapter check", fs, path) {
		return exitUsage
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
		e.errf("adapter check: not run for the %s adapter: %s No adapter was constructed or invoked. See adapter check --list for contract cases and test coverage.", agent.Adapter, reason)
		return exitUsage
	}
	if err := cfg.CheckSources(); err != nil {
		e.errf("adapter check: configuration changed or is unavailable; reload and retry")
		return exitUsage
	}
	r.Adapter, r.Target = agent.Adapter, *target
	r.Declared = declaredFor(agent)
	subject := conformance.Subject{Name: agent.Adapter, Declared: r.Declared, CallLimit: *limit,
		New: func() (adapter.Adapter, error) { return factory.New(agent, cfg.Loop.StateDir, *target) }}
	r.Results = conformance.Run(e.ctx, subject, conformance.Options{})
	return printAdapterCheck(e, r, *asJSON, false)
}

// notReadOnly refuses adapters whose observation manages runtime state or
// starts helpers. The process adapter keeps no observation state itself, but
// its configured status program is not sandboxed. Unlisted kinds are refused.
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
			fmt.Fprintln(e.stdout, "The adapter contract. Offline stand-ins cover process, Claude Code and Codex; other adapters and integrator staging are not covered.")
		} else {
			fmt.Fprintf(e.stdout, "Adapter contract check: %s adapter, target %s.\n", r.Adapter, r.Target)
			fmt.Fprintln(e.stdout, "This check attempts only the configured status command; construction or launch failure can prevent execution. It never invokes resume or stop and does not itself write loop state. The status program is not sandboxed and may have its own side effects. Cases needing stand-ins run only in the test suite.")
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

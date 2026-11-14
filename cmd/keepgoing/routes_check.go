package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

// routeCheckOut is the result of checking the standard list: whether it can
// be used, and if not the fixed reason and, for an invalid list, what is
// wrong. It runs nothing: probes of a loop's routes are routes check --loop.
type routeCheckOut struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`  // route_check
	State         string `json:"state"` // ok, missing or unreadable
	Problem       string `json:"problem,omitempty"`
	// Errors are the validation errors of an invalid list. They name no path.
	Errors []string `json:"errors"`
	Routes int      `json:"routes"`
	// Loops are the registered loops that follow the list, with the routes
	// that apply to each and those that do not, with why.
	Loops []routeCheckLoop `json:"loops"`
}

type routeCheckLoop struct {
	Loop    string         `json:"loop"`
	Problem string         `json:"problem,omitempty"`
	Routes  []string       `json:"routes"`
	Skipped []skippedRoute `json:"skipped"`
}

func routesCheckCommand(e env, args []string) int {
	positional, rest := splitPositional(args)
	fs := flag.NewFlagSet("routes check", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	loopName := fs.String("loop", "", "probe this registered loop's routes with keepgoing fallbacks check")
	directory := fs.String("directory", "", "private registry directory (default: user config/keepgoing/registry)")
	asJSON := fs.Bool("json", false, "print a versioned result")
	dryRun := fs.Bool("dry-run", false, "with --loop: preview the probes only")
	exact := fs.Bool("exact", false, "with --loop: probe the route's own model, not the cheaper one")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	positional = append(positional, fs.Args()...)
	if *loopName == "" {
		if len(positional) != 0 || *dryRun || *exact {
			e.errf("routes check validates the standard list and takes only --json; to probe routes, add --loop NAME [ID]")
			return exitUsage
		}
		return routesCheckStandard(e, *directory, *asJSON)
	}
	if len(positional) > 1 {
		e.errf("usage: keepgoing routes check --loop NAME [ID] [--dry-run] [--exact] [--json]")
		return exitUsage
	}
	dir, err := registryDirectory(*directory)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	f, err := registry.Load(dir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	for _, entry := range f.Entries {
		if entry.Loop != *loopName {
			continue
		}
		argv := []string{"fallbacks", "check", "-c", entry.ConfigPath}
		if len(positional) == 1 {
			argv = append(argv, "--id", positional[0])
		}
		for flagName, on := range map[string]bool{"--dry-run": *dryRun, "--exact": *exact, "--json": *asJSON} {
			if on {
				argv = append(argv, flagName)
			}
		}
		return run(e.ctx, argv, e.stdout, e.stderr)
	}
	e.errf("no registered loop named %q", *loopName)
	return exitUsage
}

func routesCheckStandard(e env, directory string, asJSON bool) int {
	out := routeCheckOut{SchemaVersion: 1, Kind: "route_check", State: "unreadable", Errors: []string{}, Loops: []routeCheckLoop{}}
	code := exitFailed
	path, err := config.StandardRoutesPath()
	var r *config.Routes
	if err == nil {
		r, err = config.LoadRoutes(path)
	}
	var fileErr *config.RoutesFileError
	switch {
	case err == nil:
		out.State, out.Routes, code = "ok", len(r.Routes), exitOK
	case errors.Is(err, os.ErrNotExist):
		out.State, code = "missing", exitOK
	default:
		out.Problem = config.RoutesProblem(err)
		if errors.As(err, &fileErr) && fileErr.Code == config.RoutesInvalid {
			out.Errors = strings.Split(err.Error(), "\n")
		}
	}
	if r != nil {
		if d, err := registryDirectory(directory); err == nil {
			if f, err := registry.Load(d); err == nil {
				for _, entry := range f.Entries {
					cfg, err := config.Load(entry.ConfigPath)
					if err != nil {
						out.Loops = append(out.Loops, routeCheckLoop{Loop: entry.Loop, Problem: "configuration_unavailable", Routes: []string{}, Skipped: []skippedRoute{}})
						continue
					}
					if !cfg.Loop.FollowsStandard() {
						continue
					}
					fb, skipped := r.Resolve(cfg.Loop)
					v := routeCheckLoop{Loop: cfg.Loop.Name, Routes: candidateIDs(fb), Skipped: []skippedRoute{}}
					for _, s := range skipped {
						v.Skipped = append(v.Skipped, skippedRoute{ID: s.ID, Reason: s.Reason})
					}
					out.Loops = append(out.Loops, v)
				}
			}
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return code
	}
	switch out.State {
	case "ok":
		fmt.Fprintf(e.stdout, "Standard route list: valid, %d routes.\n", out.Routes)
	case "missing":
		fmt.Fprintln(e.stdout, "Standard route list: none written yet. Add a route with keepgoing routes add.")
	default:
		fmt.Fprintf(e.stdout, "Standard route list: unreadable (%s): %s.\n", out.Problem, routesProblemText(out.Problem))
		for _, msg := range out.Errors {
			fmt.Fprintf(e.stdout, "  %s\n", msg)
		}
	}
	for _, l := range out.Loops {
		if l.Problem != "" {
			fmt.Fprintf(e.stdout, "  %s: configuration unavailable; inspect it with keepgoing validate.\n", l.Loop)
			continue
		}
		fmt.Fprintf(e.stdout, "  %s: hands off to %s\n", l.Loop, idList(l.Routes))
		for _, s := range l.Skipped {
			fmt.Fprintf(e.stdout, "    not used here: %s (%s)\n", s.ID, s.Reason)
		}
	}
	return code
}

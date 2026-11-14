package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

func goalCommand(e env, args []string) int {
	if len(args) == 0 {
		e.errf("usage: keepgoing goal create|revise|show [-c config] [--file private.json] [--id ID --revision N] [--json] [--private]")
		return exitUsage
	}
	cmd := args[0]
	if cmd != "create" && cmd != "revise" && cmd != "show" {
		e.errf("goal: expected create, revise, or show")
		return exitUsage
	}
	fs := flag.NewFlagSet("goal "+cmd, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "", configFlagHelp("configuration path"))
	file := fs.String("file", "", "private JSON goal specification (create/revise)")
	id := fs.String("id", "", "expected goal identity (revise)")
	revision := fs.Uint64("revision", 0, "expected current revision (revise)")
	asJSON := fs.Bool("json", false, "machine-readable summary")
	private := fs.Bool("private", false, "show the complete private record and history as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 || (cmd == "show" && (*file != "" || *id != "" || *revision != 0)) || (cmd != "show" && (*file == "" || *private)) || (cmd == "create" && (*id != "" || *revision != 0)) || (cmd == "revise" && (*id == "" || *revision == 0)) {
		e.errf("goal: invalid flags; create/revise need --file, revise also needs --id and --revision; --private is only for show")
		return exitUsage
	}
	if !configDefault(e, "goal", path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("goal: configuration unavailable or invalid; check the configuration privately")
		return exitUsage
	}
	if cmd == "show" {
		g, err := control.LoadGoal(cfg.Loop.StateDir)
		if err != nil {
			e.errf("%v", err)
			return exitFailed
		}
		if g == nil {
			e.errf("no goal recorded")
			return exitFailed
		}
		if *private {
			b, _ := json.MarshalIndent(g, "", "  ")
			fmt.Fprintln(e.stdout, string(b))
		} else {
			printGoal(e, g.Summary(), *asJSON)
		}
		return exitOK
	}
	// Never place task text in flags, errors, run history, or reports.
	info, statErr := os.Stat(*file)
	if statErr != nil || !info.Mode().IsRegular() {
		e.errf("goal input must be a readable regular file")
		return exitUsage
	}
	f, err := os.Open(*file)
	if err != nil {
		e.errf("cannot read goal input file")
		return exitUsage
	}
	info, statErr = f.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		f.Close()
		e.errf("goal input must be a regular file")
		return exitUsage
	}
	b, err := io.ReadAll(io.LimitReader(f, 32769))
	f.Close()
	if err != nil {
		e.errf("cannot read goal input file")
		return exitUsage
	}
	spec, err := control.ParseGoalSpec(b)
	if err != nil {
		e.errf("%v", err)
		return exitUsage
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return ownershipError(e, err)
	}
	defer l.Release()
	if err := cfg.CheckSources(); err != nil {
		e.errf("configuration changed or unavailable; no goal saved; reload and retry")
		return exitSkipped
	}
	g, err := control.WriteGoal(cfg.Loop.StateDir, cfg.Loop.Name, os.Getenv("BB_THREAD_ID"), spec, *id, *revision)
	if err != nil {
		return ownershipError(e, err)
	}
	printGoal(e, g.Summary(), *asJSON)
	return exitOK
}

func printGoal(e env, g control.GoalSummary, asJSON bool) {
	if asJSON {
		b, _ := json.MarshalIndent(g, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return
	}
	fmt.Fprintf(e.stdout, "goal: %s revision %d (%s; metadata only)\n", g.ID, g.Revision, g.State)
}

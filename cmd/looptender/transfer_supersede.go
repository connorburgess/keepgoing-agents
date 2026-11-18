package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
)

// Supersession retires only the source. The successor is an operator-declared
// reference, not a verified relationship or permission to act on another loop.
func transferSupersede(e env, args []string) int {
	fs := flag.NewFlagSet("transfer supersede", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("c", "", configFlagHelp("obsolete loop configuration path"))
	from := fs.String("from", "", "expected configured BB primary thread")
	successor := fs.String("successor-thread", "", "existing BB thread continuing the work in a separate loop (operator declaration)")
	asJSON := fs.Bool("json", false, "machine-readable result")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("supersede: invalid arguments; see transfer supersede --help")
		return exitUsage
	}
	if fs.NArg() != 0 || !transferThreadID.MatchString(*from) || !transferThreadID.MatchString(*successor) || *from == *successor {
		e.errf("supersede: distinct --from and --successor-thread are required")
		return exitUsage
	}
	if !configDefault(e, "transfer supersede", fs, path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("supersede: configuration unavailable; inspect it privately")
		return exitUsage
	}
	if cfg.Loop.Adapter != "bb" || cfg.Loop.BB == nil || cfg.Loop.HasRouteList() || cfg.Loop.BB.Thread != *from {
		e.errf("supersede: requires the expected single BB primary without fallbacks")
		return exitFailed
	}
	fail := func(err error) int {
		e.errf("supersede: %v", err)
		if errors.Is(err, lock.ErrHeld) {
			return exitSkipped
		}
		return exitFailed
	}
	if err := transferPrivatePaths(*path, cfg); err != nil {
		return fail(err)
	}
	tick, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return fail(err)
	}
	defer tick.Release()
	if err := checkTransferSources(cfg, false); err != nil {
		return fail(err)
	}
	_, err = updateControlWithHistory(cfg.Loop.StateDir, "supersede", func(f *control.File) error {
		if err := control.CheckTransferReady(*f); err != nil {
			return err
		}
		if err := control.CheckOwner(*f, os.Getenv("BB_THREAD_ID"), false); err != nil {
			return err
		}
		if err := cfg.CheckSources(); err != nil {
			return errors.New("configuration changed; reload before superseding")
		}
		f.SupersededBy = *successor
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(struct {
			Outcome         string `json:"outcome"`
			SuccessorThread string `json:"successorThread"`
		}{"superseded", *successor}) != nil {
			return exitFailed
		}
	} else {
		fmt.Fprintln(e.stdout, "Loop superseded; resume, enable and takeover are refused. Stop remains available.")
		fmt.Fprintln(e.stdout, "Successor task, goal, holds and supervision were not changed or verified.")
	}
	return exitOK
}

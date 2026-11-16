package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/loopcontext"
	"github.com/looptender/looptender/internal/state"
	"github.com/looptender/looptender/internal/supervise"
)

// contextCmd prints the loop's supervision context. It reads the loop's
// configuration and records only: it writes nothing, takes no lock, and
// queries neither a provider nor the daemon. It exits 1 when control.json
// or state.json could not be read, after printing what could be.
func contextCmd(e env, cfg *config.Config, cfgPath string, now time.Time, asJSON bool) int {
	v := loopcontext.Build(readContextInput(cfg, cfgPath, now))
	if asJSON {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprint(e.stdout, v.Text())
	}
	if len(v.Unavailable) > 0 {
		return exitFailed
	}
	return exitOK
}

// readContextInput loads what loopcontext.Build reads.
func readContextInput(cfg *config.Config, cfgPath string, now time.Time) loopcontext.Input {
	l := cfg.Loop
	if abs, err := filepath.Abs(cfgPath); err == nil {
		cfgPath = abs
	}
	var ctl *control.File
	if c, err := control.Load(l.StateDir); err == nil {
		ctl = &c
	}
	var st *state.File
	if s, err := state.Load(l.StateDir); err == nil {
		st = &s
	}
	return supervise.ContextInput(cfg, initCommandPrefix(), commandQuote(cfgPath), ctl, st, now)
}

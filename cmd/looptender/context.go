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
	"github.com/looptender/looptender/internal/window"
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
	in := loopcontext.Input{Loop: l, CommandPrefix: initCommandPrefix(), ConfigArg: commandQuote(cfgPath), Now: now}
	if ctl, err := control.Load(l.StateDir); err == nil {
		in.Control = &ctl
	}
	if st, err := state.Load(l.StateDir); err == nil {
		in.State = &st
		if st.Target() == state.Primary {
			a := l.Agent
			in.TargetAgent = &a
		} else if c, ok := effectiveRoutes(cfg, st).Find(st.Target()); ok && !c.Disabled {
			if a, known := st.CandidateAgent(c); known {
				in.TargetAgent = &a
				in.TargetCreated = c.Agent.BB != nil && c.Agent.BB.NewThread
			}
		}
	}
	if l.Limits.MaxSends > 0 {
		if used, err := state.LoadSends(l.StateDir); err != nil {
			in.SendsError = "sends.json could not be read"
		} else {
			in.SendsUsed = &used
		}
	}
	if timeout := l.Limits.NoProgressTimeout.Duration; timeout > 0 {
		p, err := state.LoadProgress(l.StateDir)
		switch {
		case err != nil:
			in.ProgressError = "progress.json could not be read"
		case p == nil:
		case now.Before(p.ObservedAt):
			in.ProgressError = "host clock precedes the last verified progress observation"
		case p.Sources != state.ProgressSources(l.Progress.Repositories):
			in.ProgressError = "repository list changed; the next check sets a new baseline"
		default:
			in.Progress = p
		}
	}
	inside, why, err := window.Inside(l.Window, now)
	in.Window = loopcontext.Window{Set: l.Window != nil, Inside: inside, Text: why}
	if err != nil {
		in.Window.Error = "the window could not be evaluated"
	}
	return in
}

package main

import (
	"context"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// updateDaemonConfiguration runs after stop handling. It changes diagnostic
// metadata only, under the tick lock; it never observes or messages an agent.
// Saving the attempt before delivery favors a missed report over duplicates.
func updateDaemonConfiguration(e env, cfg *config.Config, loaded, dryRun bool, lease *ownerLease, since *time.Time) *time.Time {
	if since == nil && cfg.UnreadableSources() {
		at := time.Now().UTC()
		since = &at
	}
	if dryRun {
		if loaded && cfg.CheckSources() == nil {
			return nil
		}
		return since
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return since
	}
	defer l.Release()
	if checkOwnership(cfg.Loop.StateDir, lease) != nil {
		return since
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("configuration diagnostic could not be saved; no report sent")
		return since
	}
	if loaded && cfg.CheckSources() == nil {
		if st.DaemonConfigUnavailable != nil {
			st.DaemonConfigUnavailable = nil
			if state.Save(cfg.Loop.StateDir, st) != nil {
				e.errf("configuration recovery diagnostic could not be saved")
			}
		}
		return nil
	}
	if st.DaemonConfigUnavailable != nil {
		at := st.DaemonConfigUnavailable.Since
		since = &at
	} else if since != nil {
		st.DaemonConfigUnavailable = &state.DaemonConfigUnavailable{Since: *since}
	}
	episode := st.DaemonConfigUnavailable
	if episode == nil || episode.ReportAttempted {
		return since
	}
	episode.ReportAttempted = true
	if state.Save(cfg.Loop.StateDir, st) != nil {
		e.errf("configuration diagnostic could not be saved; no report sent")
		return since
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	event := report.Event{Type: "error", Loop: cfg.Loop.Name, At: time.Now().UTC(), Text: cfg.Loop.Name + ": the configuration file cannot be loaded. Ordinary checks have stopped until it is restored. A saved stop may use the last configuration read.", Data: map[string]any{"cause": "configuration_unavailable", "since": episode.Since}}
	if err := reporter(e, cfg).Report(ctx, event); err != nil {
		e.errf("configuration error report attempt failed; it will not be repeated")
	}
	return since
}

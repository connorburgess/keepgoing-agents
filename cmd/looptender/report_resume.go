package main

import (
	"fmt"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/report"
)

func resumeReport(e env, cfg *config.Config) int {
	if cfg.Loop.Report == nil || cfg.Loop.Report.Format != "sessionpipe" {
		e.errf("report resume requires Sessionpipe reporting")
		return exitUsage
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		e.errf("report resume: a check is running or state is unavailable; nothing changed")
		return exitSkipped
	}
	defer l.Release()
	if err := checkOwnership(cfg.Loop.StateDir, nil); err != nil {
		return ownershipError(e, err)
	}
	if err := checkTransferSources(cfg, false); err != nil {
		e.errf("report resume: transfer fence refused; nothing changed")
		return exitSkipped
	}
	if err := report.ResumeSessionpipe(cfg.Loop.StateDir); err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	fmt.Fprintln(e.stdout, "Sessionpipe report pause cleared; no report sent or replayed; loop holds unchanged")
	return exitOK
}

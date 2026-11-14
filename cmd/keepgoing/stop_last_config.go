package main

import (
	"errors"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
)

// stopFromLastConfig carries out a saved stop, and nothing else, from cfg,
// the last configuration this daemon read, when a file it was read from can
// no longer be read. Everything else a check needs holds as for any stop:
// the tick lock, the daemon's ownership lease, the pending-transfer rule
// (which allows a stop) and the session claim (another loop's live check on
// the session refuses it). The one check skipped is that the configuration
// files are unchanged: they cannot be read, and a stop is the safe direction.
// The check's record says it was carried out from the configuration read at
// cfg.ReadAt.
func stopFromLastConfig(e env, cfg *config.Config, lease *ownerLease) int {
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		e.errf("another tick holds the lock; skipping")
		return exitSkipped
	}
	if err != nil {
		e.errf("lock: %v", err)
		return exitFailed
	}
	defer l.Release()
	if err := checkOwnership(cfg.Loop.StateDir, lease); err != nil {
		return ownershipError(e, err)
	}
	if err := checkTransferRecord(cfg, true); err != nil {
		e.errf("%v; no stop performed", err)
		return exitSkipped
	}
	if !supervise.StopReachable(cfg) {
		e.errf("the stop cannot reach bb from the last configuration read; no stop performed")
		return exitSkipped
	}
	readAt := cfg.ReadAt()
	if readAt.IsZero() {
		readAt = time.Now() // not from a file; never the daemon's case
	}
	e.errf("the configuration file cannot be read; carrying out the saved stop from the configuration read at %s", readAt.UTC().Format(time.RFC3339))
	opts := tickOptions(e, cfg, false)
	opts.StopOnly, opts.LastConfigReadAt = true, readAt
	res, err := supervise.Tick(e.ctx, cfg, opts)
	var claimed *session.ClaimedError
	if errors.As(err, &claimed) {
		e.errf("tick: %v", err)
		return exitSkipped // another loop's live check: tried again at the next poll
	}
	if err != nil {
		e.errf("tick: %v", err)
		if res.Recorded {
			return exitSkipped // a recorded failed stop is still pending
		}
		return exitFailed
	}
	return exitOK
}

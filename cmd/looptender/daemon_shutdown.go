package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/daemoninfo"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
	"github.com/looptender/looptender/internal/state"
)

// Called under tick then control locks. This is a read-only binding, never a
// control update. CheckSources verifies all loaded source bytes; SourceDigest
// identifies the main configuration, and cfg includes the parsed fallback set.
func shutdownBinding(cfg *config.Config, path, caller string, lease *ownerLease) (string, error) {
	if err := checkTransferSources(cfg, false); err != nil {
		return "", err
	}
	if err := checkOwnership(cfg.Loop.StateDir, lease); err != nil {
		return "", err
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		return "", err
	}
	if err := control.CheckOwner(ctl, caller, false); err != nil {
		return "", err
	}
	if err := control.CheckTransferReady(ctl); err != nil {
		return "", err
	}
	goal, err := control.LoadGoal(cfg.Loop.StateDir)
	if err != nil {
		return "", err
	}
	if goal != nil && goal.OwningLoop != cfg.Loop.Name {
		return "", errors.New("goal belongs to another loop")
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal([]any{path, cfg.SourceDigest(), cfg, ctl, goal, st.Target()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Keep the tick lock until the loop has returned. Thus acceptance cannot race a
// new provider check. Busy checks refuse immediately; no in-flight work is killed.
func enableDaemonShutdown(m *daemoninfo.Server, cfg *config.Config, path string, lease *ownerLease, cancel context.CancelFunc) func() {
	var mu sync.Mutex
	var held *lock.Lock
	closed := false
	m.EnableShutdown(func(req daemoninfo.ShutdownRequest) error {
		mu.Lock()
		defer mu.Unlock()
		if closed || held != nil {
			return errors.New("shutdown already requested")
		}
		tick, err := lock.Acquire(cfg.Loop.StateDir)
		if err != nil {
			return err
		}
		retain := false
		defer func() {
			if !retain {
				tick.Release()
			}
		}()
		ctl, err := lock.AcquireNamed(cfg.Loop.StateDir, lock.Control)
		if err != nil {
			return err
		}
		defer ctl.Release()
		binding, err := shutdownBinding(cfg, path, req.Caller, lease)
		if err != nil {
			return err
		}
		if binding != req.Binding {
			return errors.New("shutdown binding changed")
		}
		held, retain = tick, true
		cancel()
		return nil
	})
	return func() {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		if held != nil {
			held.Release()
		}
	}
}

func daemonShutdownCommand(e env, args []string) int {
	fs := flag.NewFlagSet("daemon-shutdown", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "", configFlagHelp("path to the config file"))
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return exitUsage
	}
	if !configDefault(e, "config", fs, path) {
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		e.errf("shutdown: configuration unavailable")
		return exitFailed
	}
	// Challenge first; the signed request carries exactly this invocation/build.
	expected, err := daemoninfo.Query(e.ctx, cfg.Loop.StateDir)
	if err != nil {
		e.errf("shutdown: no verified endpoint; use the original owned lifecycle manager for legacy daemons")
		return exitFailed
	}
	tick, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return ownershipError(e, err)
	}
	ctl, err := lock.AcquireNamed(cfg.Loop.StateDir, lock.Control)
	if err != nil {
		tick.Release()
		return ownershipError(e, err)
	}
	caller := os.Getenv("BB_THREAD_ID")
	binding, err := shutdownBinding(cfg, *path, caller, nil)
	ctl.Release()
	tick.Release()
	if err != nil {
		return ownershipError(e, err)
	}
	if err := daemoninfo.Shutdown(e.ctx, cfg.Loop.StateDir, expected, binding, caller); err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	// Acknowledgement is cancellation intent only. Completion requires removal of
	// the endpoint plus release of daemon.lock. Never launch or use a saved PID.
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	for {
		_, epErr := snapshot.Lstat(filepath.Join(cfg.Loop.StateDir, daemoninfo.FileName))
		busy, lockErr := lock.Held(cfg.Loop.StateDir, lock.Daemon)
		if errors.Is(epErr, os.ErrNotExist) && lockErr == nil && !busy {
			fmt.Fprintln(e.stdout, "supervisor shutdown completed; loop controls preserved")
			return exitOK
		}
		if !sleepContext(ctx, 50*time.Millisecond) {
			e.errf("shutdown accepted but exit unverified; reconcile before any launch")
			return exitFailed
		}
	}
}

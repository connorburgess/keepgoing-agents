package main

import (
	"errors"
	"os"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// A daemon binds to the claim it started with, including an unowned loop.
// Control generations prevent a handed-off daemon from becoming valid again.
type ownerLease struct {
	thread     string
	generation uint64
}

func leaseOf(f control.File) *ownerLease {
	l := &ownerLease{}
	if f.Ownership != nil {
		l.thread = f.Ownership.ThreadID
		l.generation = f.Ownership.Generation
	}
	return l
}

func checkOwnership(stateDir string, lease *ownerLease) error {
	if err := control.CheckControlPath(stateDir); err != nil {
		return err
	}
	f, err := control.Load(stateDir)
	if err != nil {
		return err
	}
	if lease != nil {
		if err := control.CheckOwner(f, "", false); err != nil {
			return err
		}
		current := leaseOf(f)
		if *current != *lease {
			return errors.New("loop ownership changed; this daemon has handed off and must exit")
		}
		return nil
	}
	return control.CheckOwner(f, os.Getenv("BB_THREAD_ID"), false)
}

func firstLease(leases []*ownerLease) *ownerLease {
	if len(leases) > 0 {
		return leases[0]
	}
	return nil
}

// Validation is advisory and read-only except for the private lock file. Claim
// operations recheck while holding both locks; no provider lookup is needed.
func validateOwner(stateDir string, takeOver bool) error {
	// No runtime directory means no recorded owner. Validation remains advisory;
	// enable and daemon always recheck under their locks before acting.
	if _, err := os.Stat(stateDir); errors.Is(err, os.ErrNotExist) {
		return control.CheckOwner(control.File{}, os.Getenv("BB_THREAD_ID"), takeOver)
	} else if err != nil {
		return err
	}
	l, err := lock.AcquireNamed(stateDir, lock.Control)
	if err != nil {
		return err
	}
	defer l.Release()
	if err := control.CheckControlPath(stateDir); err != nil {
		return err
	}
	f, err := control.Load(stateDir)
	if err != nil {
		return err
	}
	return control.CheckOwner(f, os.Getenv("BB_THREAD_ID"), takeOver)
}

// Called after acquiring daemon.lock. Takeover never evicts a running daemon;
// enable --take-over can transfer its claim, then it exits at its next check.
func bindDaemonOwner(stateDir string, takeOver bool) (*ownerLease, error) {
	l, err := lock.Acquire(stateDir)
	if err != nil {
		return nil, err
	}
	defer l.Release()
	if takeOver {
		f, err := updateControlWithHistory(stateDir, "daemon_takeover", func(f *control.File) error { return control.ClaimOwner(f, os.Getenv("BB_THREAD_ID"), true) })
		if err != nil {
			return nil, err
		}
		return leaseOf(f), nil
	}
	if err := validateOwner(stateDir, false); err != nil {
		return nil, err
	}
	f, err := control.Load(stateDir)
	if err != nil {
		return nil, err
	}
	return leaseOf(f), nil
}

func ownershipError(e env, err error) int {
	e.errf("ownership: %v", err)
	if errors.Is(err, lock.ErrHeld) {
		return exitSkipped
	}
	return exitFailed
}

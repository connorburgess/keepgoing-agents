package main

import (
	"os"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/session"
)

// checkHoldClearer refuses the loop's own supervised session, and anything
// its supervisor started, when it asks to clear one of the loop's holds.
// caller is the BB thread the request acts as. It reads the loop's records;
// an unreadable state record names the primary as the target, as status does.
func checkHoldClearer(cfg *config.Config, caller string) error {
	thread := ""
	if a := activeAgent(cfg); a.Adapter == "bb" && a.BB != nil {
		thread = a.BB.Thread
	}
	return control.CheckHoldClearer(control.HoldClearer{Caller: caller, TargetThread: thread, Supervised: supervisedBy(cfg.Loop.StateDir)})
}

// supervisedBy reports whether this process carries the marker for the loop
// kept in stateDir.
func supervisedBy(stateDir string) bool {
	marked := os.Getenv(control.SupervisedLoopVar)
	if marked == "" {
		return false
	}
	self := session.CleanPath(stateDir)
	return self != "" && session.CleanPath(marked) == self
}

// markSupervised puts the loop's marker in this process's environment, so
// every turn, probe and command started from here on inherits it. The
// returned function puts back what was there before.
func markSupervised(stateDir string) func() {
	old, had := os.LookupEnv(control.SupervisedLoopVar)
	if p := session.CleanPath(stateDir); p != "" {
		os.Setenv(control.SupervisedLoopVar, p)
	}
	return func() {
		if had {
			os.Setenv(control.SupervisedLoopVar, old)
		} else {
			os.Unsetenv(control.SupervisedLoopVar)
		}
	}
}

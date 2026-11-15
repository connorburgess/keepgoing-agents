package control

import (
	"errors"
	"fmt"
)

// SupervisedLoopVar names the variable a supervisor puts in its own
// environment, so that every turn, probe and command it starts carries the
// state directory of the loop that supervises it. A person's shell never has
// it. It is a cooperative marker, not a credential: a process can unset it.
const SupervisedLoopVar = "LOOPTENDER_SUPERVISED_LOOP"

// HoldClearer is what is known about a process asking to clear a loop's
// holds.
type HoldClearer struct {
	// Caller is BB_THREAD_ID, "" outside BB. It is not checked with BB.
	Caller string
	// TargetThread is the BB thread of the loop's current target, "" when
	// the target is not a BB thread or its thread is not known yet.
	TargetThread string
	// Supervised reports that the process carries this loop's
	// SupervisedLoopVar: the loop's own supervisor started it.
	Supervised bool
}

// ErrSupervisedSession marks a refusal by CheckHoldClearer.
var ErrSupervisedSession = errors.New("the supervised session cannot clear its own loop's hold")

// CheckHoldClearer is pure. Supervision never clears a hold: the session a
// loop supervises, and anything that session or its supervisor started, is
// refused. Anyone else is left to the ownership rules.
func CheckHoldClearer(h HoldClearer) error {
	switch {
	case h.Supervised:
		return fmt.Errorf("%w: this process was started by the loop's supervisor (%s names this loop); a person resumes from an ordinary terminal or the panel", ErrSupervisedSession, SupervisedLoopVar)
	case h.Caller != "" && h.Caller == h.TargetThread:
		return fmt.Errorf("%w: BB thread %s is the loop's current target; a person resumes from an ordinary terminal or the panel", ErrSupervisedSession, h.Caller)
	}
	return nil
}

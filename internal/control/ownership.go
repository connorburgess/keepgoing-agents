package control

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Ownership is a durable claim, not a lease on the thread's current activity.
// Generation changes on transfer, fencing daemons even across A -> B -> A.
type Ownership struct {
	ThreadID   string   `json:"threadId"`
	Generation uint64   `json:"generation"`
	Previous   *Handoff `json:"previous,omitempty"`
}

// Handoff records the most recent transfer without mutating BB relationships.
type Handoff struct {
	ThreadID string    `json:"threadId"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
}

var ownerID = regexp.MustCompile(`^thr_[A-Za-z0-9_-]{1,128}$`)

// CheckOwner is pure. Missing caller identity represents an external scheduler
// or terminal: it may use the existing claim, but cannot take it over.
func CheckOwner(f File, caller string, takeOver bool) error {
	if caller != "" && !ownerID.MatchString(caller) {
		return errors.New("invalid BB_THREAD_ID; expected a BB thread ID")
	}
	if takeOver && caller == "" {
		return errors.New("--take-over requires BB_THREAD_ID; run it from the new owning BB thread")
	}
	o := f.Ownership
	if o == nil {
		return nil
	}
	if !ownerID.MatchString(o.ThreadID) || o.Generation == 0 {
		return errors.New("invalid loop ownership metadata; restore a valid control file before claiming this loop")
	}
	if caller != "" && caller != o.ThreadID && !takeOver {
		return fmt.Errorf("loop is owned by BB thread %s; use enable --take-over from the new owning thread to transfer ownership", o.ThreadID)
	}
	return nil
}

// ClaimOwner runs inside UpdateChecked and under the tick lock. It preserves
// every control hold. Same-owner calls do not change the generation.
func ClaimOwner(f *File, caller string, takeOver bool) error {
	if err := CheckOwner(*f, caller, takeOver); err != nil {
		return err
	}
	if caller == "" {
		return nil
	}
	if f.Ownership == nil {
		f.Ownership = &Ownership{ThreadID: caller, Generation: 1}
		return nil
	}
	old := f.Ownership
	if old.ThreadID == caller {
		return nil
	}
	if old.Generation == ^uint64(0) {
		return errors.New("loop ownership generation exhausted")
	}
	f.Ownership = &Ownership{ThreadID: caller, Generation: old.Generation + 1, Previous: &Handoff{ThreadID: old.ThreadID, Status: "handed_off", At: time.Now().UTC()}}
	return nil
}

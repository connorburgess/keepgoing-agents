// Package state owns the machine-written records in a loop's state
// directory: state.json for what the last tick knew and runs.jsonl for an
// append-only event log.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileName of the state record.
const FileName = "state.json"

// LogName of the event log.
const LogName = "runs.jsonl"

// Primary is the active-target id of the loop's own agent.
const Primary = "primary"

// File is what one tick leaves for the next.
type File struct {
	LastPromptSend    *PromptSend `json:"lastPromptSend,omitempty"`
	StallRetry        *StallRetry `json:"stallRetry,omitempty"`
	Fingerprint       string      `json:"fingerprint,omitempty"`
	LastProgressAt    time.Time   `json:"lastProgressAt,omitempty"`
	LastWakeAt        time.Time   `json:"lastWakeAt,omitempty"`
	CheckedAt         time.Time   `json:"checkedAt,omitempty"`
	LastState         string      `json:"lastState,omitempty"`
	LastCheck         *Check      `json:"lastCheck,omitempty"`
	Reprompts         int         `json:"reprompts,omitempty"`
	Nudges            int         `json:"nudges,omitempty"` // since progress or the most recent turn end
	LastRepromptAt    time.Time   `json:"lastRepromptAt,omitempty"`
	StopRequestedSent bool        `json:"stopSent,omitempty"`

	ActiveTarget  string    `json:"activeTarget,omitempty"` // "" or "primary" means the loop's own agent
	LastHandoffAt time.Time `json:"lastHandoffAt,omitempty"`
	Handoffs      int       `json:"handoffs,omitempty"`
	Degraded      bool      `json:"degraded,omitempty"`
	// PrimaryResetsAt is when the primary's limit was reported to lift at the
	// handoff away from it; switch-back waits for it.
	PrimaryResetsAt   time.Time `json:"primaryResetsAt,omitempty"`
	LastSwitchBackTry time.Time `json:"lastSwitchBackTry,omitempty"`
	// AwaitingReported is set once a pending interaction has been reported,
	// so the person is told once, not on every tick.
	AwaitingReported bool `json:"awaitingReported,omitempty"`
}

// StallRetry is a target-scoped grace period for ordinary stall nudges only.
// It never replaces Git evidence or refunds loop-wide allowances.
type StallRetry struct {
	Target    string    `json:"target"`
	Waiting   bool      `json:"waiting"`
	ResumedAt time.Time `json:"resumedAt,omitempty"`
}

// Check records a completed tick's decision and outcome, not a live session
// status. It contains no prompts, provider output, or raw error text.
type Check struct {
	At             time.Time `json:"at"`
	Version        string    `json:"version,omitempty"`
	Target         string    `json:"target"`
	ObservedTarget string    `json:"observedTarget,omitempty"`
	ObservedState  string    `json:"observedState"`
	AwaitingInput  bool      `json:"awaitingInput,omitempty"`
	Action         string    `json:"action"`
	Reason         string    `json:"reason"`
	Outcome        string    `json:"outcome"` // succeeded, failed, or dry_run
	DryRun         bool      `json:"dryRun,omitempty"`
}

// Target returns the active target id, defaulting to primary.
func (f File) Target() string {
	if f.ActiveTarget == "" {
		return Primary
	}
	return f.ActiveTarget
}

// Load reads state.json; a missing file is an empty record.
func Load(stateDir string) (File, error) {
	var f File
	b, err := os.ReadFile(filepath.Join(stateDir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	if f.LastPromptSend != nil && !f.LastPromptSend.Valid() {
		f.LastPromptSend = nil
	}
	return f, nil
}

// Save writes state.json atomically with owner-only permissions.
func Save(stateDir string, f File) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, FileName)
	tmp, err := os.CreateTemp(stateDir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Event is one line of runs.jsonl.
type Event struct {
	At     time.Time      `json:"at"`
	Loop   string         `json:"loop"`
	Event  string         `json:"event"`
	Action string         `json:"action,omitempty"`
	Reason string         `json:"reason,omitempty"`
	State  string         `json:"state,omitempty"`
	Target string         `json:"target,omitempty"`
	Error  string         `json:"error,omitempty"`
	Extra  map[string]any `json:"extra,omitempty"`
}

// Append adds an event to runs.jsonl.
func Append(stateDir string, e Event) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, LogName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

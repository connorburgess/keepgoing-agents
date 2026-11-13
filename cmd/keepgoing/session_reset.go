package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Operations recorded for a session reset. The incident history holds no
// identities, so who ran it is recorded as its kind of caller; the thread ID,
// when there is one, is in runs.jsonl.
const (
	sessionResetByThread   = "session_reset_by_thread"
	sessionResetByTerminal = "session_reset_by_terminal"
)

type sessionResetReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	Type          string `json:"type"`
	Loop          string `json:"loop"`
	Target        string `json:"target"`
	// HadSession says a session was saved before the reset.
	HadSession bool `json:"hadSession"`
	// By is "thread" when a BB thread ran it, "terminal" otherwise.
	By string `json:"by"`
	// Holds are the holds still in force; the reset clears none of them.
	Holds   []string `json:"holds"`
	Message string   `json:"message"`
}

// sessionReset forgets a headless target's saved session so that its next
// turn starts a new conversation. It is a person's deliberate decision: no
// check does this by itself. It sends nothing, starts nothing and clears no
// hold.
func sessionReset(e env, cfg *config.Config, target, note string, asJSON bool) int {
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		e.errf("a check is running; try again in a moment")
		return exitSkipped
	}
	if err != nil {
		e.errf("lock: %v", err)
		return exitFailed
	}
	defer l.Release()
	if err := checkOwnership(cfg.Loop.StateDir, nil); err != nil {
		return ownershipError(e, err)
	}
	if err := checkTransferSources(cfg, false); err != nil {
		e.errf("%v; no session reset; reload and retry", err)
		return exitSkipped
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	if target == "" {
		target = st.Target()
	}
	agent := cfg.Loop.Agent
	if target != state.Primary {
		c, ok := cfg.FallbackSet.Find(target)
		if !ok {
			e.errf("no target %q: use primary or a fallback's id", target)
			return exitUsage
		}
		agent = c.Agent
	}
	h, ok := runner.New(agent, filepath.Join(cfg.Loop.StateDir, "targets", target))
	switch {
	case agent.Adapter == "cursor":
		e.errf("session reset is not available for a Cursor target yet: its runner keeps its own session rule")
		return exitUsage
	case !ok:
		e.errf("session reset applies to a headless agent that keeps a saved session (claude, codex, or an agent defined in configuration); target %s is %s, whose conversation keepgoing does not own", target, agent.Adapter)
		return exitUsage
	case !h.Spec().Capabilities.ExactResume:
		e.errf("target %s does not continue a session: every turn already starts a new conversation", target)
		return exitUsage
	}
	had, err := h.ResetSession()
	if errors.Is(err, runner.ErrTurnRunning) {
		e.errf("a turn is running on %s; wait for it to end or stop it, then run this again", target)
		return exitFailed
	}
	if err != nil {
		e.errf("session reset: the saved session could not be removed: %v", err)
		return exitFailed
	}

	ctl, _ := control.Load(cfg.Loop.StateDir)
	caller := os.Getenv("BB_THREAD_ID")
	report := sessionResetReport{SchemaVersion: 1, Type: "session_reset", Loop: cfg.Loop.Name, Target: target, HadSession: had, By: "terminal", Holds: []string{}}
	operation := sessionResetByTerminal
	extra := map[string]any{"hadSession": had, "by": "terminal"}
	if caller != "" {
		report.By, operation = "thread", sessionResetByThread
		extra["by"], extra["thread"] = "thread", caller
	}
	for _, hold := range []struct {
		name string
		on   bool
	}{{"disabled", !ctl.Enabled}, {"paused", ctl.Paused}, {"stop", ctl.Stop}, {"awaitingUser", ctl.AwaitingUser}, {"completed", ctl.Completed}} {
		if hold.on {
			report.Holds = append(report.Holds, hold.name)
		}
	}
	report.Message = fmt.Sprintf("the next turn on %s starts a new conversation", target)
	if !had {
		report.Message = fmt.Sprintf("no session was saved for %s; its next turn starts a new conversation", target)
	}
	if len(report.Holds) > 0 {
		report.Message += "; nothing is sent while the loop is held, so run keepgoing resume when you are ready"
	}
	state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "session_reset", Reason: note, Target: target, Extra: extra})
	if asJSON {
		b, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprintf(e.stdout, "%s: session reset; %s\n", cfg.Loop.Name, report.Message)
	}
	// The session is already reset, so a history failure is reported, not undone.
	gate := incident.Gate(ctl.Gate())
	if err := incident.Append(cfg.Loop.StateDir, incident.Record{Kind: "control", CheckStartedAt: time.Now().UTC(), Build: version,
		ControlBefore: gate, ControlAfter: gate,
		ControlChange: &incident.ControlChange{Operation: operation, SaveObservedAt: time.Now().UTC(), OwnerPresentBefore: ctl.Ownership != nil, OwnerPresentAfter: ctl.Ownership != nil}}); err != nil {
		e.errf("session reset: done; incident history write failed; diagnostic evidence is incomplete")
		return exitFailed
	}
	return exitOK
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/state"
)

const (
	sessionClearUnconfirmedByThread   = "session_clear_unconfirmed_by_thread"
	sessionClearUnconfirmedByTerminal = "session_clear_unconfirmed_by_terminal"
)

type sessionClearUnconfirmedReport struct {
	SchemaVersion int      `json:"schemaVersion"`
	Type          string   `json:"type"`
	Loop          string   `json:"loop"`
	Target        string   `json:"target"`
	By            string   `json:"by"`
	Holds         []string `json:"holds"`
	Message       string   `json:"message"`
}

// sessionClearUnconfirmed clears an unconfirmed Antigravity turn record after
// a supervisor crash so the next send can roll over while preserving any saved
// session or unadopted hold. It sends nothing, starts nothing, and clears no
// hold.
func sessionClearUnconfirmed(e env, cfg *config.Config, target, note string, asJSON bool) int {
	caller := os.Getenv("BB_THREAD_ID")
	if err := checkHoldClearer(cfg, caller); err != nil {
		e.errf("%v", err)
		return exitUsage
	}
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
		e.errf("%v; unconfirmed turn not cleared; reload and retry", err)
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
		if cfg.FallbackSet == nil {
			e.errf("no target %q: use primary", target)
			return exitUsage
		}
		c, ok := cfg.FallbackSet.Find(target)
		if !ok {
			e.errf("no target %q: use primary or a fallback's id", target)
			return exitUsage
		}
		agent = c.Agent
	}
	if agent.Adapter != "antigravity" || agent.Antigravity == nil {
		e.errf("session clear-unconfirmed applies to an Antigravity target; target %s is %s", target, agent.Adapter)
		return exitUsage
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("session clear-unconfirmed: controls unavailable; unconfirmed turn not cleared")
		return exitFailed
	}
	a := runner.NewAntigravity(runner.AntigravityConfig{}, filepath.Join(cfg.Loop.StateDir, "targets", target))
	if err := a.ClearUnconfirmed(e.ctx); err != nil {
		e.errf("session clear-unconfirmed: %v", err)
		return exitFailed
	}

	report := sessionClearUnconfirmedReport{
		SchemaVersion: 1,
		Type:          "session_clear_unconfirmed",
		Loop:          cfg.Loop.Name,
		Target:        target,
		By:            "terminal",
		Holds:         []string{},
	}
	operation := sessionClearUnconfirmedByTerminal
	extra := map[string]any{"by": "terminal"}
	if caller != "" {
		report.By, operation = "thread", sessionClearUnconfirmedByThread
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
	report.Message = fmt.Sprintf("unconfirmed turn cleared for %s; any saved session and holds were preserved", target)
	if len(report.Holds) > 0 {
		report.Message += "; nothing is sent while the loop is held, so run looptender resume when you are ready"
	}
	if err := state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "session_clear_unconfirmed", Reason: note, Target: target, Extra: extra}); err != nil {
		e.errf("session clear-unconfirmed: done; event history write failed; diagnostic evidence is incomplete")
		return exitFailed
	}
	if asJSON {
		b, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprintf(e.stdout, "%s: session clear-unconfirmed; %s\n", cfg.Loop.Name, report.Message)
	}
	gate := incident.Gate(ctl.Gate())
	if err := incident.Append(cfg.Loop.StateDir, incident.Record{
		Kind:           "control",
		CheckStartedAt: time.Now().UTC(),
		Build:          version,
		ControlBefore:  gate,
		ControlAfter:   gate,
		ControlChange: &incident.ControlChange{
			Operation:          operation,
			SaveObservedAt:     time.Now().UTC(),
			OwnerPresentBefore: ctl.Ownership != nil,
			OwnerPresentAfter:  ctl.Ownership != nil,
		},
	}); err != nil {
		e.errf("session clear-unconfirmed: done; incident history write failed; diagnostic evidence is incomplete")
		return exitFailed
	}
	return exitOK
}

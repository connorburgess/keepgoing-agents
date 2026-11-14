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

const (
	sessionAdoptByThread   = "session_adopt_by_thread"
	sessionAdoptByTerminal = "session_adopt_by_terminal"
)

// claudeTwoPlacesWarning is Claude Code's own warning, quoted from its VS
// Code documentation, about one conversation open in two places.
const claudeTwoPlacesWarning = `Claude Code's documentation warns: "Using it in two places at once can mix up its messages."`

type sessionAdoptReport struct {
	SchemaVersion int      `json:"schemaVersion"`
	Type          string   `json:"type"`
	Loop          string   `json:"loop"`
	Target        string   `json:"target"`
	By            string   `json:"by"`
	Holds         []string `json:"holds"`
	Message       string   `json:"message"`
}

// sessionAdopt hands a Claude Code conversation that keepgoing did not start,
// typically one a person had open in the Claude Code IDE extension, to a
// headless Claude Code target: the next turn resumes it with --resume. Claude
// Code documents this handover from the extension to the CLI. Only the
// identifier's shape is checked; the conversation is never read, and whether
// it exists is unknown until a turn resumes it.
func sessionAdopt(e env, cfg *config.Config, target, id, note string, asJSON bool) int {
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
		e.errf("%v; no session adopted; reload and retry", err)
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
	switch agent.Adapter {
	case "claude":
	case "codex", "cursor", config.CustomAdapter:
		e.errf("session adopt applies to a headless Claude Code target only; target %s is %s, which documents no handover of an IDE conversation to its command line that keepgoing supports", target, agent.Adapter)
		return exitUsage
	default:
		e.errf("session adopt applies to a headless Claude Code target only; target %s is %s, whose conversation keepgoing does not keep a session for", target, agent.Adapter)
		return exitUsage
	}
	if !runner.ValidClaudeSession(id) {
		e.errf("%q is not shaped like a Claude Code session identifier (a UUID); nothing was adopted", truncateForMessage(id))
		return exitUsage
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	if ctl.Stop {
		e.errf("a stop is pending for this loop; let it finish and resume first, then adopt")
		return exitFailed
	}
	h, ok := runner.New(agent, filepath.Join(cfg.Loop.StateDir, "targets", target))
	if !ok {
		e.errf("target %s has no usable Claude Code settings", target)
		return exitUsage
	}
	switch err := h.AdoptSession(id); {
	case errors.Is(err, runner.ErrTurnRunning):
		e.errf("a turn is running on %s; wait for it to end or stop it, then run this again", target)
		return exitFailed
	case errors.Is(err, runner.ErrSessionSaved):
		e.errf("target %s already continues a conversation of its own; run keepgoing session reset first if you mean to leave it", target)
		return exitFailed
	case err != nil:
		e.errf("session adopt: the session could not be saved: %v", err)
		return exitFailed
	}

	caller := os.Getenv("BB_THREAD_ID")
	report := sessionAdoptReport{SchemaVersion: 1, Type: "session_adopt", Loop: cfg.Loop.Name, Target: target, By: "terminal", Holds: []string{}}
	operation := sessionAdoptByTerminal
	extra := map[string]any{"by": "terminal"}
	if caller != "" {
		report.By, operation = "thread", sessionAdoptByThread
		extra["by"], extra["thread"] = "thread", caller
	}
	for _, hold := range []struct {
		name string
		on   bool
	}{{"disabled", !ctl.Enabled}, {"paused", ctl.Paused}, {"awaitingUser", ctl.AwaitingUser}, {"completed", ctl.Completed}} {
		if hold.on {
			report.Holds = append(report.Holds, hold.name)
		}
	}
	report.Message = fmt.Sprintf("the next turn on %s resumes the adopted conversation. Close it in the IDE first: keepgoing cannot check that. %s", target, claudeTwoPlacesWarning)
	if len(report.Holds) > 0 {
		report.Message += " Nothing is sent while the loop is held, so run keepgoing resume when you are ready."
	}
	state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "session_adopt", Reason: note, Target: target, Extra: extra})
	if asJSON {
		b, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprintf(e.stdout, "%s: session adopted; %s\n", cfg.Loop.Name, report.Message)
	}
	gate := incident.Gate(ctl.Gate())
	if err := incident.Append(cfg.Loop.StateDir, incident.Record{Kind: "control", CheckStartedAt: time.Now().UTC(), Build: version,
		ControlBefore: gate, ControlAfter: gate,
		ControlChange: &incident.ControlChange{Operation: operation, SaveObservedAt: time.Now().UTC(), OwnerPresentBefore: ctl.Ownership != nil, OwnerPresentAfter: ctl.Ownership != nil}}); err != nil {
		e.errf("session adopt: done; incident history write failed; diagnostic evidence is incomplete")
		return exitFailed
	}
	return exitOK
}

func truncateForMessage(s string) string {
	if len(s) > 64 {
		return s[:64] + "…"
	}
	return s
}

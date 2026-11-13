package supervise

import (
	"errors"
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The run reference graph is diagnostic history. Nothing below returns an
// error to the tick or feeds a decision: a failed write leaves a missing
// record and one warning. Dry runs record nothing.

func (t *tick) graphUpdate(fn func(*rungraph.Graph) (bool, error)) bool {
	if t.opts.DryRun {
		return false
	}
	if err := rungraph.Update(t.loop.StateDir, t.now, fn); err != nil {
		t.graphFailed = true
		return false
	}
	return true
}

// graphSyncHook is a test seam: it runs before the tick takes the graph lock.
var graphSyncHook func()

// graphSync records the run boundary implied by saved control state, then the
// goal revision in effect for an open run. Control is read inside the graph
// lock: a boundary recorded by a control command always holds that lock, so
// the tick can never apply control state older than the graph it updates.
func (t *tick) graphSync() {
	if t.opts.DryRun {
		return
	}
	if graphSyncHook != nil {
		graphSyncHook()
	}
	runID, err := rungraph.NewID("run_")
	if err != nil {
		t.graphFailed = true
		return
	}
	goal, _ := control.LoadGoal(t.loop.StateDir)
	t.graphUpdate(func(g *rungraph.Graph) (bool, error) {
		ctl, err := control.Load(t.loop.StateDir)
		if err != nil {
			return false, nil // The tick reports the control failure itself.
		}
		changed, err := g.Sync(rungraph.Boundary{Gate: rungraph.GateOf(ctl), At: t.now, NewRunID: runID})
		if err != nil || goal == nil || g.CurrentRun == "" || g.Completed {
			return changed, err
		}
		s := goal.Summary()
		linked, err := g.Relate(rungraph.Link{Kind: rd.GoalLink, GoalID: s.ID, GoalRevision: s.Revision, Source: rungraph.SourceGoal, RecordedAt: t.now})
		return changed || linked, err
	})
}

// graphClose records the session reference and reports a failed graph write.
// It runs once, before the check record is written, so the warning reaches
// runs.jsonl; Tick calls it again for paths that return without a record.
func (t *tick) graphClose() {
	if t.graphClosed {
		return
	}
	t.graphClosed = true
	t.graphSession()
	if t.graphFailed {
		t.warn("run reference record unavailable; discovery evidence is incomplete")
	}
}

// graphSession records the chat/session reference known for the tick's target
// without asking a provider: a BB target's configured thread, or a headless
// runner's saved resume ID. Neither says the conversation exists or is live.
func (t *tick) graphSession() {
	target := t.res.Target
	if t.opts.DryRun || target == "" {
		return
	}
	agent := t.loop.Agent
	if target != state.Primary {
		c, ok := t.fb.Find(target)
		if !ok {
			return
		}
		agent = c.Agent
	}
	link := rungraph.Link{Kind: rd.SessionLink, Target: target, Provider: agent.Adapter, RecordedAt: t.now}
	switch agent.Adapter {
	case "bb":
		if agent.BB == nil {
			return
		}
		link.Session, link.Source = agent.BB.Thread, rungraph.SourceConfigured
	case "claude", "codex", "cursor":
		id, err := runner.Dir(filepath.Join(t.loop.StateDir, "targets", target)).InspectSession()
		if err != nil || id == "" {
			return
		}
		link.Session, link.Source = id, rungraph.SourceRunnerFile
	default:
		return // No recorded session identity exists for this adapter.
	}
	skipped := false
	t.graphUpdate(func(g *rungraph.Graph) (bool, error) {
		if g.CurrentRun == "" || g.Completed {
			return false, nil
		}
		changed, err := g.Relate(link)
		if errors.Is(err, rd.ErrInvalid) {
			// The identifier is outside the recordable characters. That is a
			// gap in the evidence, not a failed write: note it once per run.
			skipped, err = g.Skip(target, rungraph.ReasonSessionID, t.now)
			return skipped, err
		}
		return changed, err
	})
	if skipped {
		t.warn("target %q: its chat/session identifier cannot be recorded, so this run has no chat reference for it", target)
	}
}

// graphBegin persists a send intent before the adapter is called.
func (t *tick) graphBegin(target, action, mode string, message prompt.Message) string {
	if t.opts.DryRun {
		return ""
	}
	id, err := rungraph.NewID("attempt_")
	if err != nil {
		t.graphFailed = true
		return ""
	}
	a := rungraph.Attempt{ID: id, Target: target, Action: action, Mode: mode, GoalID: message.GoalID, GoalRevision: message.GoalRevision, RecordedAt: t.now}
	if !t.graphUpdate(func(g *rungraph.Graph) (bool, error) { return true, g.Begin(a) }) {
		return ""
	}
	return id
}

// graphFinish records that the adapter call returned. It is not delivery.
func (t *tick) graphFinish(id, outcome string) {
	if id == "" {
		return
	}
	t.graphUpdate(func(g *rungraph.Graph) (bool, error) { return g.Finish(id, rd.Outcome(outcome), t.now) })
}

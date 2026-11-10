package supervise

import (
	"context"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

// PollWake asks whether a new notification warrants an early idle check. The
// caller holds the tick lock and bounds ctx. No sends, probes, target changes,
// reports, or progress/budget updates occur here. Tick rechecks all gates before
// acting; a notification does not authorize an action by itself.
func PollWake(ctx context.Context, cfg *config.Config, newAdapter factory.Func) bool {
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil || !ctl.Enabled || ctl.Stop || ctl.Paused || ctl.Completed || ctl.AwaitingUser {
		return false
	}
	// Leave the deadline hold to the next ordinary tick without consuming child
	// notices or querying a provider. Do not spin early ticks in dry-run mode.
	if cfg.Loop.Limits.DeadlineReached(time.Now()) {
		return false
	}
	inWindow, _, err := window.Inside(cfg.Loop.Window, time.Now())
	if err != nil || !inWindow {
		return false
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return false
	}
	agent := cfg.Loop.Agent
	if st.Target() != state.Primary {
		c, ok := cfg.FallbackSet.Find(st.Target())
		if !ok || c.Disabled {
			return false // Let the next ordinary tick resolve removed targets.
		}
		agent = c.Agent
	}
	if newAdapter == nil {
		newAdapter = factory.New
	}
	a, err := newAdapter(agent, cfg.Loop.StateDir, st.Target())
	if err != nil {
		return false
	}
	p, ok := a.(adapter.WakePoller)
	if !ok || !p.PollWake(ctx) {
		return false
	}
	obs, err := a.Observe(ctx)
	if err != nil || ctx.Err() != nil {
		return false
	}
	d := decide.Decide(decide.Input{Control: ctl.Gate(), Obs: obs, InWindow: inWindow})
	// Child input wins over a completion wake; let the next tick persist and
	// report the hold rather than waiting for the normal interval.
	return d.Action == decide.Wake || (d.Action == decide.AwaitUser && len(obs.AwaitingChildren) > 0)
}

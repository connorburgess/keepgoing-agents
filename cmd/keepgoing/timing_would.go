package main

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/timing"
)

// timingWould is what usage-aware timing would do at the loop's next turn
// boundary, from the records status reads. Nothing acts on it yet.
type timingWould struct {
	Action   string     `json:"action"`
	To       string     `json:"to,omitempty"`
	Rule     string     `json:"rule"`
	Reason   string     `json:"reason"`
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
	Text     string     `json:"text"`
	ActedOn  bool       `json:"actedOn"`
}

// timingWouldOf decides as timing.Decide would at the next turn boundary.
// Status cannot see a turn end, so it assumes one; the gates it can read
// from records (holds, an error or unknown last observation, a route the
// failover rules or a person chose) apply as they would then.
func timingWouldOf(cfg *config.Config, st state.File, ctl control.File, reg *health.Registry, now time.Time) *timingWould {
	l := cfg.Loop
	if !l.TimingOn() || cfg.RouteList.Unreadable || cfg.TimingStepsUnavailable != "" {
		return nil
	}
	in := timing.Input{Now: now, Timing: *l.Timing, Current: st.Target(), TurnEnded: true,
		Held: ctl.Paused || ctl.Stop || ctl.AwaitingUser || ctl.Completed}
	if fb := cfg.FallbackSet; fb != nil {
		in.ProbeTTL = fb.Probe.TTL.Duration
	}
	if in.Current != timing.First {
		// Timing has made no move yet, so another route was chosen by a
		// handoff or by a person.
		if !st.LastHandoffAt.IsZero() {
			in.MovedByFailover = true
		} else {
			in.Retarget = &timing.Retarget{}
		}
	}
	if c := st.LastCheck; c != nil {
		in.InError = c.ObservedState == string(decide.Error)
		in.RefusalOrUnknown = c.ObservedState == string(decide.Unknown)
	}
	if reg != nil {
		first := l.PrimaryCandidate()
		id := probe.UsageIdentity(first)
		if e := reg.Candidates[first.ID]; e != nil && e.Usage.For(id) {
			in.Usage = e.Usage
			if e.UsageHistory.For(id) {
				in.History = e.UsageHistory
			}
		}
	}
	if in.History == nil {
		in.History = &allowance.History{}
	}
	d := timing.Decide(in)
	return &timingWould{Action: d.Action, To: d.To, Rule: d.Rule, Reason: d.Reason, Window: d.Window, ResetsAt: d.ResetsAt,
		Text: "timing would, at the next turn boundary: " + d.Text(usageSpan) + " (not acted on yet)"}
}

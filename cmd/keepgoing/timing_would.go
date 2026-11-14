package main

import (
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/timing"
)

// timingWould is what usage-aware timing would do at the loop's next turn
// boundary, from the records status reads: the decision the next check
// makes unless something changes before then.
type timingWould struct {
	Action   string     `json:"action"`
	To       string     `json:"to,omitempty"`
	Rule     string     `json:"rule"`
	Reason   string     `json:"reason"`
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
	Text     string     `json:"text"`
}

// timingWouldOf decides as timing.Decide would at the next turn boundary,
// from the same records a check builds its input from. Status cannot see a
// turn end, so it assumes one; the gates it can read from records (holds, an
// error or unknown last observation, a route the failover rules or a person
// chose) apply as they would then.
func timingWouldOf(cfg *config.Config, st state.File, ctl control.File, reg *health.Registry, now time.Time) *timingWould {
	if !cfg.Loop.TimingOn() {
		return nil
	}
	in := timing.FromRecords(cfg, st, reg, now)
	in.TurnEnded = true
	in.Held = ctl.Paused || ctl.Stop || ctl.AwaitingUser || ctl.Completed
	if c := st.LastCheck; c != nil {
		in.InError = c.ObservedState == string(decide.Error)
		in.RefusalOrUnknown = c.ObservedState == string(decide.Unknown)
	}
	if at := timing.RetargetReset(in); at != nil {
		in.Retarget.ResetsAt = at
	}
	d := timing.Decide(in)
	return &timingWould{Action: d.Action, To: d.To, Rule: d.Rule, Reason: d.Reason, Window: d.Window, ResetsAt: d.ResetsAt,
		Text: "timing at the next turn boundary: " + d.Text(usageSpan)}
}

// timingResetAfter is the reset of the first named window in the latest
// reading of the first route's account that is still to come, or nil.
func timingResetAfter(cfg *config.Config, now time.Time) *time.Time {
	reg, err := health.Load(cfg.Loop.StateDir)
	if err != nil {
		return nil
	}
	first := cfg.Loop.PrimaryCandidate()
	e := reg.Candidates[first.ID]
	if e == nil || !e.Usage.For(probe.UsageIdentity(first)) {
		return nil
	}
	for _, key := range cfg.Loop.Timing.Windows {
		for _, w := range e.Usage.Windows {
			if w.Key == key && w.ResetsAt != nil && w.ResetsAt.After(now) {
				at := w.ResetsAt.UTC()
				return &at
			}
		}
	}
	return nil
}

// timingRecordLines are status lines from timing's record: its last move and
// a step it has stopped trying.
func timingRecordLines(st state.File, now time.Time) []string {
	tr := st.Timing
	if tr == nil {
		return nil
	}
	var out []string
	if m := tr.LastMove; m != nil {
		text := m.Text
		if text == "" {
			text = m.Action + " to " + m.To
		}
		out = append(out, fmt.Sprintf("timing last moved %s: %s", usageAge(int64(now.Sub(m.At)/time.Second)), text))
	}
	if tr.Capped {
		out = append(out, fmt.Sprintf("timing: could not step to %s %d times for this window; it stops trying until the window resets", tr.FailedTo, tr.Failures))
	}
	if p := tr.Pending; p != nil {
		out = append(out, fmt.Sprintf("timing: a step from %s to %s was recorded and not settled; the next check sets the thread to the route on record", p.From, p.To))
	}
	return out
}

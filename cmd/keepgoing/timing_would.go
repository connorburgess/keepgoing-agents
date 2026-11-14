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
	if timing.LeftConfiguration(cfg.Loop, st) {
		return &timingWould{Action: timing.Back, To: timing.First, Rule: timing.RuleGate, Reason: "left_configuration",
			Text: fmt.Sprintf("timing at the next turn boundary: return to %s: %s is no longer a timing step", timing.First, st.Target())}
	}
	if !cfg.Loop.TimingOn() {
		return nil
	}
	in := timing.FromRecords(cfg, st, reg, now)
	in.TurnEnded = true
	in.Held = ctl.Paused || ctl.Stop || ctl.AwaitingUser || ctl.Completed
	if c := st.LastCheck; c != nil {
		in.InError = c.ObservedState == string(decide.Error)
		in.Unknown = c.ObservedState == string(decide.Unknown)
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
	if cfg.Loop.Timing == nil {
		return nil
	}
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

// timingRecordLines are status lines from timing's record: its last move,
// with its window's reset as it stands now, a step it stopped trying, a move
// it keeps retrying, a step not yet settled, and a route reapplied.
func timingRecordLines(st state.File, now time.Time) []string {
	var out []string
	if st.ReapplyRoute {
		out = append(out, fmt.Sprintf("route: the loop is on %s because its route left the configuration; the next check that acts sets the thread's model and effort to %s's", st.Target(), st.Target()))
	}
	tr := st.Timing
	if tr == nil {
		return out
	}
	if m := tr.LastMove; m != nil {
		verb := map[string]string{"up": "stepped up", "down": "stepped down", "back": "returned"}[m.Action]
		if verb == "" {
			verb = "moved"
		}
		line := fmt.Sprintf("timing last moved %s: %s from %s to %s", usageAge(int64(now.Sub(m.At)/time.Second)), verb, m.From, m.To)
		if m.Window != "" && !m.ResetsAt.IsZero() {
			when := "resets " + m.ResetsAt.UTC().Format("2006-01-02 15:04Z")
			if !m.ResetsAt.After(now) {
				when = "has reset since (" + m.ResetsAt.UTC().Format("2006-01-02 15:04Z") + ")"
			}
			line += fmt.Sprintf(" for the %s window, which %s", m.Window, when)
		}
		out = append(out, line)
	}
	switch {
	case tr.Capped:
		out = append(out, fmt.Sprintf("timing: could not step up to %s %d times for this %s window; it stops trying until the window resets", tr.FailedTo, tr.Failures, tr.FailedWindow))
	case tr.Failures > 0:
		out = append(out, fmt.Sprintf("timing: could not move to %s %d times; it tries again at each turn boundary", tr.FailedTo, tr.Failures))
	}
	if p := tr.Pending; p != nil {
		out = append(out, fmt.Sprintf("timing: a step from %s to %s was recorded and not settled; the next check that acts sets the thread to %s, the route on record", p.From, p.To, st.Target()))
	}
	return out
}

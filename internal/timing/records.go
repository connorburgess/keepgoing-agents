package timing

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// FromRecords builds the decision's input from a loop's records: its route
// and how it got there, timing's own record, and the first route's latest
// reading and history. The check's outcome (held, in error, another move, a
// turn ended, a refusal or unknown) is the caller's to set.
func FromRecords(cfg *config.Config, st state.File, reg *health.Registry, now time.Time) Input {
	l := cfg.Loop
	in := Input{Now: now, Current: st.Target(), History: &allowance.History{}}
	if l.Timing != nil {
		in.Timing = *l.Timing
	}
	if fb := cfg.FallbackSet; fb != nil {
		in.ProbeTTL = fb.Probe.TTL.Duration
	}
	if in.Current != First {
		switch st.TargetSetBy {
		case state.SetByHandoff, state.SetByNewThread:
			in.MovedByFailover = true
		case "":
			// State written before the record existed: a handoff on record
			// is taken as the cause, so timing never moves the loop off it.
			in.MovedByFailover = !st.LastHandoffAt.IsZero()
		}
	}
	move := func(m *state.TimingMove) *Move {
		if m == nil {
			return nil
		}
		return &Move{At: m.At, From: m.From, To: m.To, Action: m.Action, Window: m.Window, ResetsAt: m.ResetsAt}
	}
	if tr := st.Timing; tr != nil {
		in.LastMove, in.LastStepUp = move(tr.LastMove), move(tr.LastUp)
		if r := tr.Retarget; r != nil {
			in.Retarget = &Retarget{At: r.At, ResetsAt: r.ResetsAt}
		}
		in.GaveUp = move(tr.GaveUp)
	}
	if in.Retarget == nil && in.Current != First && st.TargetSetBy == state.SetByRetarget {
		// A retarget with no record of its own: timing waits as for one made
		// now, and a check saves it so the wait is not moved again.
		in.Retarget = &Retarget{At: now}
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
	return in
}

// RetargetReset is the reset a retarget with none known waits for: the first
// named window's reset in a reading taken after the retarget. Nil until such
// a reading exists.
func RetargetReset(in Input) *time.Time {
	r := in.Retarget
	if r == nil || r.ResetsAt != nil || in.Usage == nil || in.Usage.At.Before(r.At) {
		return nil
	}
	for _, key := range in.Timing.Windows {
		if w := in.window(key); w != nil && w.ResetsAt != nil && w.ResetsAt.After(r.At) {
			at := w.ResetsAt.UTC()
			return &at
		}
	}
	return nil
}

// LeftConfiguration: the loop is on a step timing chose that is no longer a
// step, because timing was turned off or the step was dropped from steps. The
// next turn boundary returns it to the first route.
func LeftConfiguration(l config.Loop, st state.File) bool {
	target := st.Target()
	return st.TargetSetBy == state.SetByTiming && target != First && (!l.TimingOn() || l.Timing.StepIndex(target) < 0)
}

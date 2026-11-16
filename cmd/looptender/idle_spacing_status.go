package main

import (
	"fmt"
	"io"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/state"
)

// idleSpacingStatus shows loop.progress.idle_spacing as the last check left
// it. It is historical: the next check compares the facts again, and any
// change wakes the session at once.
type idleSpacingStatus struct {
	Enabled bool   `json:"enabled"`
	First   string `json:"first,omitempty"`
	Max     string `json:"max,omitempty"`
	// Unchanged counts consecutive idle wakes after which no watched fact
	// changed, up to the last one.
	Unchanged  int        `json:"unchanged,omitempty"`
	LastWakeAt *time.Time `json:"lastIdleWakeAt,omitempty"`
	// NextIdleWakeAt and Wait are set when the last check spaced the next
	// idle wake: when it is due, and the fixed reason code.
	NextIdleWakeAt *time.Time `json:"nextIdleWakeAt,omitempty"`
	Wait           string     `json:"wait,omitempty"`
	// SpacedTotal counts the idle wakes the rule spaced over the days kept
	// in Days. It is history, never a decision input.
	SpacedTotal int                   `json:"spacedTotal"`
	Days        []state.IdleSpacedDay `json:"days,omitempty"`
}

func describeIdleSpacing(st state.File, p config.Progress) idleSpacingStatus {
	out := idleSpacingStatus{SpacedTotal: st.IdleSpacedTotal(), Days: st.IdleSpacedDays}
	first, max := p.IdleSpacingWaits()
	if max <= 0 {
		return out
	}
	out.Enabled, out.First, out.Max = true, first.String(), max.String()
	if r := st.IdleSpacing; r != nil && !r.LastWakeAt.IsZero() {
		at := r.LastWakeAt
		out.Unchanged, out.LastWakeAt = r.Unchanged, &at
		if r.Wait != "" && !r.WaitUntil.IsZero() {
			next := r.WaitUntil
			out.NextIdleWakeAt, out.Wait = &next, r.Wait
		}
	}
	return out
}

func printIdleSpacing(w io.Writer, s idleSpacingStatus, now time.Time) {
	if !s.Enabled {
		if s.SpacedTotal > 0 {
			fmt.Fprintf(w, "idle spacing: off; %d idle wake(s) were spaced while it was on (history)\n", s.SpacedTotal)
		}
		return
	}
	fmt.Fprintf(w, "idle spacing: on (first %s, max %s); %d idle wake(s) spaced on record", s.First, s.Max, s.SpacedTotal)
	switch {
	case s.LastWakeAt == nil:
		fmt.Fprint(w, "; no idle wake on record, so the next is not spaced")
	case s.NextIdleWakeAt != nil:
		fmt.Fprintf(w, "; next idle wake due %s (%s: %d idle wake(s) in a row changed nothing looptender watches)", when(*s.NextIdleWakeAt), s.Wait, s.Unchanged+1)
		if !now.Before(*s.NextIdleWakeAt) {
			fmt.Fprint(w, "; now passed, the next check decides")
		}
	default:
		fmt.Fprintf(w, "; last idle wake %s; not waiting", when(*s.LastWakeAt))
	}
	fmt.Fprintln(w)
}

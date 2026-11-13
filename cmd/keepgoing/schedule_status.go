package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// scheduleStatus is what status, doctor and the panel say about the loop's
// scheduler, from the record alone (state.ScheduleOf).
type scheduleStatus struct {
	Overdue     bool       `json:"overdue"`
	LastCheckAt *time.Time `json:"lastCheckAt,omitempty"`
	For         string     `json:"for,omitempty"`
	Usual       string     `json:"usual,omitempty"`
	Threshold   string     `json:"threshold"`
	// StartedBy is the scheduler identity the last check was started with,
	// when one was supplied (tick --routine source/routine).
	StartedBy *state.Routine `json:"startedBy,omitempty"`
	Message   string         `json:"message,omitempty"`
}

func describeSchedule(st state.File, daemonInterval time.Duration, enabled bool, now time.Time) scheduleStatus {
	s := state.ScheduleOf(st, daemonInterval, enabled, now)
	out := scheduleStatus{Overdue: s.Overdue, Threshold: s.Threshold.String(), StartedBy: s.StartedBy}
	if !s.LastCheckAt.IsZero() {
		at := s.LastCheckAt
		out.LastCheckAt, out.For = &at, s.For.Truncate(time.Second).String()
	}
	if s.Usual > 0 {
		out.Usual = s.Usual.String()
	}
	if s.Overdue {
		out.Message = overdueSentence(s)
	}
	return out
}

// overdueSentence says that the loop's scheduler may have stopped. It names a
// BB automation only when the last check said it was started by one, never
// by guessing from the configuration.
func overdueSentence(s state.Schedule) string {
	usual := "its checks' usual interval is unknown"
	if s.Usual > 0 {
		usual = "checks normally run every " + shortDuration(s.Usual)
	}
	text := fmt.Sprintf("no check has run for %s; %s, so the loop's scheduler may have stopped", shortDuration(s.For.Truncate(time.Minute)), usual)
	if r := s.StartedBy; r != nil && r.Source == state.RoutineBBAutomation && r.Routine != "" {
		return text + "; the BB automation that runs this loop (" + r.Routine + ") may have disabled itself: check it in BB's Automations"
	}
	return text + ": check the scheduler that runs `keepgoing tick` for this loop, or the keepgoing service or daemon"
}

// shortDuration prints a duration without a trailing zero unit: 3h10m, not
// 3h10m0s.
func shortDuration(d time.Duration) string {
	out := d.String()
	for _, z := range []string{"m0s", "h0m"} {
		if strings.HasSuffix(out, z) {
			out = strings.TrimSuffix(out, z[1:])
		}
	}
	return out
}

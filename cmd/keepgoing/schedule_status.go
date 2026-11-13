package main

import (
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
)

// scheduleStatus is what status, doctor and the panel say about whether the
// loop's checks are running, from the record alone (state.ScheduleOf).
// Usual and Threshold are given only while the rule is judging.
type scheduleStatus struct {
	Overdue bool `json:"overdue"`
	Judging bool `json:"judging"`
	// NotJudged says why it is not: disabled, held or never_checked.
	NotJudged string `json:"notJudged,omitempty"`
	// Basis is usual (its usual interval is known) or fallback (fewer than
	// three gaps between checks seen; called stopped only after a day).
	Basis       string     `json:"basis,omitempty"`
	GapsSeen    int        `json:"gapsSeen"`
	LastCheckAt *time.Time `json:"lastCheckAt,omitempty"`
	For         string     `json:"for,omitempty"`
	Usual       string     `json:"usual,omitempty"`
	Threshold   string     `json:"threshold,omitempty"`
	// Slept: the daemon that runs this loop has been running since before
	// the last check, so the time without one is the computer sleeping.
	Slept bool `json:"slept,omitempty"`
	// Blocked: checks were started after the last one but found an earlier
	// check still running.
	Blocked bool `json:"blocked,omitempty"`
	// StartedBy is the scheduler identity the last check was started with,
	// when one was supplied (tick --routine source/routine).
	StartedBy *state.Routine `json:"startedBy,omitempty"`
	Message   string         `json:"message,omitempty"`
	sch       state.Schedule
}

// describeSchedule applies the overdue rule as the supervisor does. d is
// the daemon as just queried, or nil when it was not.
func describeSchedule(l *config.Loop, st state.File, ctl control.File, d *daemonStatus, now time.Time) scheduleStatus {
	var started time.Time
	if d != nil && d.State == "responsive" && d.StartedAt != nil {
		started = *d.StartedAt
	}
	s := state.ScheduleOf(st, supervise.ScheduleInput(l, ctl, started), now)
	out := scheduleStatus{Overdue: s.Overdue, Judging: s.NotJudged == "", NotJudged: s.NotJudged, Basis: s.Basis, GapsSeen: s.GapsSeen,
		Slept: s.Slept, Blocked: s.Blocked, StartedBy: s.StartedBy, sch: s}
	if !s.LastCheckAt.IsZero() {
		at := s.LastCheckAt
		out.LastCheckAt = &at
	}
	if out.Judging {
		out.For, out.Threshold = state.ShortDuration(s.For), state.ShortDuration(s.Threshold)
		if s.Usual > 0 {
			out.Usual = state.ShortDuration(s.Usual)
		}
	}
	if s.Overdue {
		out.Message = overdueSentence(s)
	}
	return out
}

// overdueSentence says why no check may have run. It names a BB automation
// only when the last check said it was started by one, never by guessing
// from the configuration.
func overdueSentence(s state.Schedule) string {
	measure := fmt.Sprintf("checks normally run every %s", state.ShortDuration(s.Usual))
	if s.Basis == "fallback" {
		measure = fmt.Sprintf("its usual interval is not known yet, so it is called stopped after %s", state.ShortDuration(s.Threshold))
	}
	if s.Blocked {
		return fmt.Sprintf("no check has finished for %s (%s): checks are being started, but each finds an earlier check still running, which may be hung", state.ShortDuration(s.For), measure)
	}
	text := fmt.Sprintf("no check has run for %s (%s): the computer may have slept, or ", state.ShortDuration(s.For), measure)
	if r := s.StartedBy; r != nil && r.Source == state.RoutineBBAutomation && r.Routine != "" {
		return text + "the BB automation that runs this loop (" + r.Routine + ") may have disabled itself; check it in BB's Automations"
	}
	return text + "the scheduler that runs `keepgoing tick` for this loop may have stopped"
}

// scheduleDoctorLine is doctor's supervisor.schedule finding, or ok=false
// when there is nothing to say.
func scheduleDoctorLine(s scheduleStatus) (level, text, fix string, ok bool) {
	sch := s.sch
	switch {
	case s.Overdue && sch.Blocked:
		return "warning", "Checks are not finishing: " + s.Message + ".", "Look for a keepgoing tick that has been running for a long time and end it; the next check then runs.", true
	case s.Overdue:
		return "warning", "No check may be running: " + s.Message + ".", "If the computer was not asleep, start the scheduler again. A BB automation that failed repeatedly may have disabled itself (doctor --bb-automation <id> --bb-project <id> inspects one); `keepgoing service install` runs checks from a service the operating system restarts.", true
	case s.NotJudged == "held":
		return "ok", "Not judged while the loop is held (paused, waiting for a person or complete): its scheduler may have been stopped on purpose.", "", true
	case !s.Judging:
		return "", "", "", false
	case sch.Slept:
		return "ok", "The last check was " + s.For + " ago, but the daemon that runs this loop has been running since before it: the computer probably slept.", "", true
	case s.Basis == "fallback":
		return "ok", fmt.Sprintf("Not judged against its usual interval yet: %d of %d gaps between checks seen. Until then it is called stopped only after %s without a check; the last one was %s ago.", s.GapsSeen, state.CadenceMinGaps, s.Threshold, s.For), "", true
	default:
		return "ok", "Checks are running: the last one was " + s.For + " ago, and they normally run every " + s.Usual + ".", "", true
	}
}

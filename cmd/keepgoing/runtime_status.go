package main

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const defaultStaleAfter = 30 * time.Minute
const unknownNextCheck = "unknown: scheduler installation and next run are not recorded"

// runtimeStatus distinguishes current control/configuration from historical
// observations. It cannot establish that a scheduler or session is alive.
type runtimeStatus struct {
	State            string `json:"state"`
	Reason           string `json:"reason"`
	Source           string `json:"source"`    // control, configuration, last_check, unavailable
	Freshness        string `json:"freshness"` // never, recent, stale, future
	StaleAfter       string `json:"staleAfter"`
	BinaryComparison string `json:"binaryComparison"` // unknown, match, different
	NextCheck        string `json:"nextCheck"`
}

func compareCheckVersion(c *state.Check, current string) string {
	if c == nil || c.Version == "" || c.Version == "dev" || current == "" || current == "dev" {
		return "unknown"
	}
	if c.Version == current {
		return "match"
	}
	return "different"
}

func describeRuntime(ctl control.File, st state.File, inWindow, targetConfigured bool, now time.Time, staleAfter time.Duration, currentVersion string) runtimeStatus {
	r := runtimeStatus{State: "unknown", Source: "unavailable", Freshness: "never", StaleAfter: staleAfter.String(), BinaryComparison: compareCheckVersion(st.LastCheck, currentVersion), NextCheck: unknownNextCheck}
	if !st.CheckedAt.IsZero() {
		r.Freshness = "recent"
		if now.Sub(st.CheckedAt) > staleAfter {
			r.Freshness = "stale"
		} else if st.CheckedAt.Sub(now) > time.Minute {
			r.Freshness = "future"
		}
	}
	r.Source = "control"
	switch {
	case ctl.Stop:
		r.State, r.Reason = "stop_requested", "stop requested; no further wakes"
	case !ctl.Enabled:
		r.State, r.Reason = "disabled", "supervision is disabled"
	case ctl.Paused:
		r.State, r.Reason = "paused", "supervision is paused"
	case ctl.AwaitingUser:
		r.State, r.Reason = "awaiting_user", "waiting for a person; resume explicitly when ready"
	case ctl.Completed:
		r.State, r.Reason = "completed", "task marked complete; resume explicitly to reopen it"
	default:
		r.Source = "configuration"
		if !targetConfigured {
			r.Reason = "recorded target is missing or disabled in configuration"
			return r
		}
		if !inWindow {
			r.State, r.Reason = "outside_window", "outside the configured operating window"
			return r
		}
		r.Source = "unavailable"
		if r.Freshness != "recent" {
			r.Reason = "no recent check; inspect the supervisor and its scheduler"
			return r
		}
		c := st.LastCheck
		if c == nil || !c.At.Equal(st.CheckedAt) {
			r.Reason = "detailed check history is unavailable; wait for a tick from an updated binary"
			return r
		}
		if c.Target != st.Target() {
			r.Reason = "target changed after the recorded check"
			return r
		}
		r.Source = "last_check"
		r.Reason = c.Reason
		switch {
		case c.Outcome == "failed":
			r.State = "check_failed"
		case c.DryRun || c.Outcome == "dry_run":
			r.State = "dry_run"
		case c.Outcome != "succeeded":
			r.State, r.Reason = "unknown", "recorded check outcome is unrecognized"
		case c.ObservedTarget != st.Target():
			r.State, r.Reason = "not_observed", "the current target was not observed by the recorded check"
		case c.AwaitingInput:
			r.State = "recorded_awaiting_input"
		default:
			switch c.ObservedState {
			case "active", "idle", "pending", "stopping", "error":
				r.State = "recorded_" + c.ObservedState
			default:
				r.State = "not_observed"
			}
		}
	}
	return r
}

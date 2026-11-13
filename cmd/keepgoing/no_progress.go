package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The no-progress flag marks a loop that keeps being prompted without a
// recorded change to its progress repositories. It is built from recorded
// facts only: the time of the last recorded change (state's LastProgressAt),
// and the retained incident history since then. The transcript is never read
// and no quality is judged. It only informs a person: it is never a need,
// never part of an all-clear, never sent in a report, and it never pauses,
// moves or stops anything. status and the panel both show this one value.
//
// Lanes are not flagged: keepgoing records a loop's lanes only as counts and
// hashed keys, with no sends or progress of their own.

// noProgressView is the flag and the facts behind it.
type noProgressView struct {
	// Availability is flagged, clear, off (both thresholds are 0) or
	// not_available. Reason says why it is not available: no_progress_source,
	// no_check (no check has fingerprinted the repositories yet) or
	// history_unavailable.
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
	// Since is the last time the repositories are recorded as changed, or
	// first fingerprinted: no change is recorded after it.
	Since        *time.Time `json:"since,omitempty"`
	SinceSeconds *int64     `json:"sinceSeconds,omitempty"`
	// Sends are successful wakes, nudges and re-prompts after Since; Errors
	// are checks after Since that found an error or retried the turn. Held is
	// the time held for a person after Since.
	Sends  *noProgressFact `json:"sends,omitempty"`
	Errors *noProgressFact `json:"errors,omitempty"`
	Held   *noProgressHeld `json:"held,omitempty"`
	// Text is the flag in one sentence; Lines are the facts, one sentence
	// each.
	Text  string   `json:"text"`
	Lines []string `json:"lines"`
}

// noProgressFact is a count. AtLeast: the history no longer reaches back to
// Since for this kind of record, so the count is a lower bound. FlagAfter is
// the configured threshold (0: this fact never raises the flag).
type noProgressFact struct {
	Count     int  `json:"count"`
	AtLeast   bool `json:"atLeast"`
	FlagAfter int  `json:"flagAfter"`
	Raised    bool `json:"raised"`
}

type noProgressHeld struct {
	Seconds       int64 `json:"seconds"`
	WindowSeconds int64 `json:"windowSeconds"`
	AtLeast       bool  `json:"atLeast"`
}

// noProgressOf builds the flag. j is the incident journal, nil when it could
// not be read; ctl is the control record now.
func noProgressOf(cfg *config.Config, st state.File, ctl control.File, j *incident.Journal, now time.Time) noProgressView {
	v := noProgressView{Lines: []string{}}
	p := cfg.Loop.Progress
	switch {
	case len(p.Repositories) == 0:
		v.Availability, v.Reason, v.Text = "not_available", "no_progress_source", "Not available: no progress source is configured."
		return v
	case st.LastProgressAt.IsZero() || st.Fingerprint == "":
		v.Availability, v.Reason, v.Text = "not_available", "no_check", "Not available: no check has recorded its progress repositories yet."
		return v
	case j == nil:
		v.Availability, v.Reason, v.Text = "not_available", "history_unavailable", "Not available: its history could not be read."
		return v
	}
	since := st.LastProgressAt.UTC()
	v.Since, v.SinceSeconds = &since, secondsSince(now, since)
	window := now.Sub(since)
	sends := &noProgressFact{FlagAfter: p.SendsFlag()}
	errs := &noProgressFact{FlagAfter: p.ErrorsFlag()}
	// Whether retention removed records of a tier after Since, which makes a
	// count from that tier a lower bound. Records an older writer dropped have
	// no tier: they are older than every retained record.
	prunedAfter := func(tiers ...string) bool {
		for _, t := range tiers {
			var tp incident.TierPruned
			switch t {
			case incident.TierKey:
				tp = j.Pruned.Key
			case incident.TierCondition:
				tp = j.Pruned.Condition
			case incident.TierActivity:
				tp = j.Pruned.Activity
			case incident.TierRoutine:
				tp = j.Pruned.Routine
			}
			if tp.Count > 0 && (tp.NewestAt == nil || !tp.NewestAt.Before(since)) {
				return true
			}
		}
		tiered := j.Pruned.Key.Count + j.Pruned.Condition.Count + j.Pruned.Activity.Count + j.Pruned.Routine.Count
		return j.Dropped > tiered && (len(j.Records) == 0 || j.Records[0].CheckStartedAt.After(since))
	}
	// A send is never a repeatable check, so it is never in the routine
	// tier; a check that found an error and sent nothing can be.
	sends.AtLeast = prunedAfter(incident.TierKey, incident.TierCondition, incident.TierActivity)
	errs.AtLeast = prunedAfter(incident.TierKey, incident.TierCondition, incident.TierActivity, incident.TierRoutine)
	for _, r := range j.Records {
		if r.Kind != "tick" || !r.CheckStartedAt.After(since) || r.DryRun || r.Outcome == "dry_run" {
			continue
		}
		// A folded run of identical checks counts each one, when the whole run
		// is after Since; otherwise the record counts once and the count is a
		// lower bound.
		n := 1
		if c := r.Collapsed; c != nil && c.Count > 0 {
			if c.FirstAt.After(since) {
				n += int(c.Count)
			}
		}
		partial := r.Collapsed != nil && r.Collapsed.Count > 0 && !r.Collapsed.FirstAt.After(since)
		if (r.Action == "wake" || r.Action == "nudge" || r.Action == "reprompt") && r.Outcome == "succeeded" {
			sends.Count += n
			sends.AtLeast = sends.AtLeast || partial
		}
		if r.Observation.State == "error" || r.Action == "reprompt" {
			errs.Count += n
			errs.AtLeast = errs.AtLeast || partial
		}
	}
	sends.Raised = sends.FlagAfter > 0 && sends.Count >= sends.FlagAfter
	errs.Raised = errs.FlagAfter > 0 && errs.Count >= errs.FlagAfter
	v.Sends, v.Errors = sends, errs
	v.Held = noProgressHeldOf(j, ctl, since, now, prunedAfter(incident.TierKey))

	span := dockSpan(window)
	count := func(f *noProgressFact, one, many string) string {
		s := dockPlural(f.Count, one, many)
		if f.AtLeast {
			s = "at least " + s
		}
		return s
	}
	flagAt := func(f *noProgressFact) string {
		if f.FlagAfter == 0 {
			return "this does not raise the flag"
		}
		return fmt.Sprintf("the flag is raised at %d", f.FlagAfter)
	}
	v.Lines = append(v.Lines,
		"No change to its progress repositories is recorded in the last "+span+".",
		dockCapital(count(sends, "send", "sends"))+" in that time (wakes, nudges and re-prompts); "+flagAt(sends)+".",
		dockCapital(count(errs, "check", "checks"))+" in that time found an error or retried the turn; "+flagAt(errs)+".")
	if h := v.Held; h != nil {
		held := dockSpan(time.Duration(h.Seconds) * time.Second)
		if h.Seconds == 0 {
			held = "no time"
		}
		if h.AtLeast {
			held = "at least " + held
		}
		v.Lines = append(v.Lines, "Held for a person for "+held+" of the last "+span+".")
	}
	raised := []string{}
	if sends.Raised {
		raised = append(raised, count(sends, "send", "sends"))
	}
	if errs.Raised {
		raised = append(raised, count(errs, "check that found an error or retried the turn", "checks that found an error or retried the turn"))
	}
	switch {
	case len(raised) > 0:
		v.Availability = "flagged"
		v.Text = "No progress: " + strings.Join(raised, " and ") + " with no recorded change to its repositories in " + span + "."
	case sends.FlagAfter == 0 && errs.FlagAfter == 0:
		v.Availability, v.Text = "off", "Off: both of its no-progress thresholds are 0."
	default:
		v.Availability = "clear"
		v.Text = "Clear: " + count(sends, "send", "sends") + " and " + count(errs, "check", "checks") + " with an error or retry in " + span + " with no recorded change to its repositories."
	}
	return v
}

// noProgressHeldOf is how long the loop was held for a person after since,
// from the awaiting-user flag before and after each retained record and the
// control record now. atLeast: hold changes after since were pruned.
func noProgressHeldOf(j *incident.Journal, ctl control.File, since, now time.Time, atLeast bool) *noProgressHeld {
	h := &noProgressHeld{WindowSeconds: int64(now.Sub(since) / time.Second), AtLeast: atLeast}
	// The flag's value from since to the first change after it, then each
	// change in turn, ending with the record now.
	type change struct {
		at            time.Time
		before, after bool
	}
	var changes []change
	for _, r := range j.Records {
		if r.ControlBefore == nil || r.ControlAfter == nil || !r.CheckStartedAt.After(since) || r.CheckStartedAt.After(now) {
			continue
		}
		at := r.CheckStartedAt
		if c := r.ControlChange; c != nil && !c.SaveObservedAt.IsZero() {
			at = c.SaveObservedAt
		}
		changes = append(changes, change{at, r.ControlBefore.AwaitingUser, r.ControlAfter.AwaitingUser})
	}
	held := ctl.AwaitingUser
	if len(changes) > 0 {
		held = changes[0].before
	}
	from := since
	var total time.Duration
	for _, c := range changes {
		if held && c.at.After(from) {
			total += c.at.Sub(from)
		}
		from, held = c.at, c.after
	}
	if held && now.After(from) {
		total += now.Sub(from)
	}
	h.Seconds = int64(total / time.Second)
	return h
}

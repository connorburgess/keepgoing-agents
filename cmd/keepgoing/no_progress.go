package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// The no-progress flag marks a loop that keeps being prompted without a
// recorded change to its progress repositories. It is built from recorded
// facts only and says no more than they show:
//   - the window starts at state's ProgressChangedAt, the uncredited time a
//     check last saw the fingerprint change (or first recorded it, a
//     baseline: then no change is known in "at least" that long);
//   - messages are the run graph's send attempts in that window by their own
//     outcome (the adapter returned), except turn retries, which the graph
//     marks returned whatever the provider answered: those count from the
//     incident history's outcome, only when the provider accepted them;
//   - errors are the incident history's checks that found an error or
//     retried the turn;
//   - held time is the time the records cover in which the loop was held for
//     a person.
//
// The transcript is never read and no quality is judged. It only informs a
// person: it is never a need, never part of an all-clear, never sent in a
// report, and it never pauses, moves or stops anything. status and the panel
// both show this one value. Lanes are not flagged: keepgoing records a loop's
// lanes only as counts and hashed keys.

// noProgressView is the flag and the facts behind it.
type noProgressView struct {
	// Availability is flagged, clear, off (both thresholds are 0),
	// not_enough_history (a lower bound below a threshold, with nothing
	// raised), not_shown (the loop is off, stopped or finished) or
	// not_available. Reason says why it is not available: no_progress_source,
	// no_check, clock_behind, history_unavailable, or record_unreadable when
	// status could not read control.json or state.json.
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
	// Since is when the window starts: the last recorded change, or with
	// Baseline the first recorded fingerprint, after which no change is known.
	Since        *time.Time      `json:"since,omitempty"`
	SinceSeconds *int64          `json:"sinceSeconds,omitempty"`
	Baseline     bool            `json:"baseline,omitempty"`
	Messages     *noProgressFact `json:"messages,omitempty"`
	Errors       *noProgressFact `json:"errors,omitempty"`
	Held         *noProgressHeld `json:"held,omitempty"`
	// Text is the flag in one sentence; Lines are the facts, one sentence
	// each.
	Text  string   `json:"text"`
	Lines []string `json:"lines"`
}

// noProgressFact is a count. AtLeast: the records no longer cover the whole
// window for this kind, or one could not be confirmed, so the count is a
// lower bound. FlagAfter is the configured threshold (0: never raises it).
type noProgressFact struct {
	Count     int  `json:"count"`
	AtLeast   bool `json:"atLeast"`
	FlagAfter int  `json:"flagAfter"`
	Raised    bool `json:"raised"`
	// coveredFrom is where the records of this kind begin to cover the window.
	coveredFrom time.Time
}

// noProgressHeld is the time held for a person within the time the records
// cover, CoveredSeconds; AtLeast when that is less than the window.
type noProgressHeld struct {
	Seconds        int64 `json:"seconds"`
	CoveredSeconds int64 `json:"coveredSeconds"`
	AtLeast        bool  `json:"atLeast"`
}

// noProgressOf builds the flag. j is the incident journal and g the run
// graph, nil when they could not be read; ctl is the control record now.
func noProgressOf(cfg *config.Config, st state.File, ctl control.File, j *incident.Journal, g *rungraph.Graph, now time.Time) noProgressView {
	v := noProgressView{Lines: []string{}}
	p := cfg.Loop.Progress
	since := st.ProgressChangedAt.UTC()
	switch {
	case len(p.Repositories) == 0:
		v.Availability, v.Reason, v.Text = "not_available", "no_progress_source", "Not available: no progress source is configured."
		return v
	case !ctl.Enabled || ctl.Stop || ctl.Completed:
		why := "off"
		switch {
		case ctl.Stop:
			why = "stopped"
		case ctl.Completed:
			why = "finished"
		}
		v.Availability, v.Text = "not_shown", "Not shown: the loop is "+why+"."
		return v
	case st.ProgressChangedAt.IsZero():
		v.Availability, v.Reason, v.Text = "not_available", "no_check", "Not available: no check has recorded its progress repositories yet."
		return v
	case since.After(now):
		v.Availability, v.Reason, v.Text = "not_available", "clock_behind", "Not available: the progress time is ahead of this computer's clock."
		return v
	case j == nil || g == nil:
		v.Availability, v.Reason, v.Text = "not_available", "history_unavailable", "Not available: its history could not be read."
		return v
	case noProgressLatest(j, g).After(now):
		// The clock stepped back past records it wrote: no count from them
		// can be exact, and no span measured to now is.
		v.Availability, v.Reason, v.Text = "not_available", "clock_behind", "Not available: some of its records are dated after this computer's clock."
		return v
	}
	v.Since, v.SinceSeconds, v.Baseline = &since, secondsSince(now, since), st.ProgressBaseline
	window := now.Sub(since)
	in := func(t time.Time) bool { return !t.Before(since) && !t.After(now) }

	// Where the incident history covers the window for records of some
	// tiers: after the newest pruned record of each, and, for records an
	// older writer dropped without a tier, from the oldest retained record.
	covered := func(tiers ...string) time.Time {
		from := since
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
			if tp.Count == 0 {
				continue
			}
			if tp.NewestAt == nil {
				return now
			}
			if tp.NewestAt.After(from) || tp.NewestAt.Equal(from) {
				from = *tp.NewestAt
			}
		}
		tiered := j.Pruned.Key.Count + j.Pruned.Condition.Count + j.Pruned.Activity.Count + j.Pruned.Routine.Count
		if j.Dropped > tiered {
			if len(j.Records) == 0 {
				return now
			}
			if first := j.Records[0].CheckStartedAt; first.After(from) {
				from = first
			}
		}
		return from
	}

	// Messages: each send the run graph recorded in the window, by its own
	// outcome. An attempt still at its intent (its result was never saved)
	// may or may not have gone out, which makes the count a lower bound. A
	// send by the check that recorded the change counts, as after it.
	msgs := &noProgressFact{FlagAfter: p.SendsFlag(), coveredFrom: since}
	if g.Dropped.Attempts > 0 {
		switch {
		case len(g.Attempts) == 0:
			msgs.coveredFrom = now
		case g.Attempts[0].RecordedAt.After(since):
			msgs.coveredFrom = g.Attempts[0].RecordedAt
		}
	}
	// The checks that made a send attempt, by the time the graph records it.
	attempted := map[time.Time]bool{}
	for _, a := range g.Attempts {
		attempted[a.RecordedAt] = true
		// A turn retry is recorded as returned whatever the provider answered;
		// it counts below, from the check's recorded outcome.
		if !in(a.RecordedAt) || a.Mode == "turn_retry" {
			continue
		}
		switch a.Outcome {
		case rd.AdapterReturned:
			msgs.Count++
		case rd.Intent:
			msgs.AtLeast = true
		}
	}
	// Errors, and turn retries, from the incident history. A retry is never a
	// repeatable check, so never in the routine tier; a check that found an
	// error and sent nothing can be.
	errs := &noProgressFact{FlagAfter: p.ErrorsFlag(), coveredFrom: covered(incident.TierKey, incident.TierCondition, incident.TierActivity, incident.TierRoutine)}
	if retriesFrom := covered(incident.TierKey, incident.TierCondition, incident.TierActivity); retriesFrom.After(msgs.coveredFrom) {
		msgs.coveredFrom = retriesFrom
	}
	for _, r := range j.Records {
		if r.Kind != "tick" || !in(r.CheckStartedAt) || r.DryRun || r.Outcome == "dry_run" {
			continue
		}
		// A folded run of identical checks counts each one when the whole run
		// is in the window; otherwise the record counts once, as a lower bound.
		n, partial := 1, false
		if c := r.Collapsed; c != nil && c.Count > 0 {
			if in(c.FirstAt) {
				n += int(c.Count)
			} else {
				partial = true
			}
		}
		retried := r.TurnRetry == "retried" || r.TurnRetry == "queued"
		if r.Action == "reprompt" {
			switch {
			case retried:
				// A turn retry the provider accepted prompts the turn again.
				msgs.Count += n
			case r.TurnRetry == "queue_unconfirmed":
				msgs.AtLeast = true
			}
		}
		// A check that recorded a prompt being sent, with no attempt in the
		// run graph (its write failed): that send is not counted above, so the
		// count is a lower bound.
		prompted := r.Action == "wake" || r.Action == "nudge" || (r.Action == "reprompt" && (r.TurnRetry == "none" || r.TurnRetry == "" || r.TurnRetry == "nothing_to_retry" || r.TurnRetry == "unsupported"))
		if prompted && r.Outcome == "succeeded" && !attempted[r.CheckStartedAt] {
			msgs.AtLeast = true
		}
		if r.Observation.State == "error" || retried {
			errs.Count += n
			errs.AtLeast = errs.AtLeast || partial
		}
	}
	msgs.AtLeast = msgs.AtLeast || msgs.coveredFrom.After(since)
	errs.AtLeast = errs.AtLeast || errs.coveredFrom.After(since)
	msgs.Raised = msgs.FlagAfter > 0 && msgs.Count >= msgs.FlagAfter
	errs.Raised = errs.FlagAfter > 0 && errs.Count >= errs.FlagAfter
	v.Messages, v.Errors = msgs, errs
	v.Held = noProgressHeldOf(j, ctl, covered(incident.TierKey), since, now)

	span := dockSpan(window)
	// "the last 3 h", and for a window under a minute "the last minute".
	last := func(d time.Duration) string {
		if d < 45*time.Second {
			return "the last minute"
		}
		return "the last " + dockSpan(d)
	}
	inSpan := "in " + span
	switch {
	case window < 45*time.Second:
		inSpan = "in the last minute"
	case v.Baseline:
		inSpan = "in at least " + span
	}
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
	switch {
	case v.Baseline && window < 45*time.Second:
		v.Lines = append(v.Lines, "No change to its progress repositories since a check first recorded them, just now.")
	case v.Baseline:
		v.Lines = append(v.Lines, "No change to its progress repositories in at least "+span+", since a check first recorded them.")
	default:
		v.Lines = append(v.Lines, "Its progress repositories last changed "+dockAgo(now, since)+".")
	}
	v.Lines = append(v.Lines,
		dockCapital(count(msgs, "message", "messages"))+" sent since then (wakes, nudges, re-prompts, notices about failed lanes, and briefs sent on a move to another route or back); "+flagAt(msgs)+".",
		dockCapital(count(errs, "check", "checks"))+" since then found an error or retried the turn; "+flagAt(errs)+".")
	if h := v.Held; h != nil {
		held := dockSpan(time.Duration(h.Seconds) * time.Second)
		if h.Seconds == 0 {
			held = "no time"
		}
		if h.AtLeast {
			v.Lines = append(v.Lines, "Held for a person for "+held+" of "+last(time.Duration(h.CoveredSeconds)*time.Second)+", which is as far back as its history covers.")
		} else {
			v.Lines = append(v.Lines, "Held for a person for "+held+" of "+last(window)+".")
		}
	}
	raised := []string{}
	if msgs.Raised {
		raised = append(raised, count(msgs, "message", "messages"))
	}
	if errs.Raised {
		raised = append(raised, count(errs, "check that found an error or retried the turn", "checks that found an error or retried the turn"))
	}
	// A lower bound below its threshold cannot say the flag is clear.
	short := (msgs.AtLeast && msgs.FlagAfter > 0 && !msgs.Raised) || (errs.AtLeast && errs.FlagAfter > 0 && !errs.Raised)
	switch {
	case len(raised) > 0:
		v.Availability = "flagged"
		v.Text = "No progress: " + strings.Join(raised, " and ") + " with no recorded change to its repositories " + inSpan + "."
	case msgs.FlagAfter == 0 && errs.FlagAfter == 0:
		v.Availability, v.Text = "off", "Off: both of its no-progress thresholds are 0."
	case short:
		from := since
		for _, f := range []*noProgressFact{msgs, errs} {
			if f.AtLeast && f.FlagAfter > 0 && !f.Raised && f.coveredFrom.After(from) {
				from = f.coveredFrom
			}
		}
		v.Availability = "not_enough_history"
		if from.After(since) {
			v.Text = "Not enough history to tell: the retained history covers only " + last(now.Sub(from)) + "."
		} else {
			v.Text = "Not enough history to tell: whether some messages were sent could not be confirmed."
		}
	default:
		v.Availability = "clear"
		v.Text = "Clear: " + count(msgs, "message", "messages") + " and " + count(errs, "check", "checks") + " with an error or retry " + inSpan + " with no recorded change to its repositories."
	}
	return v
}

// noProgressLatest is the latest time any record the flag reads is dated:
// an incident record, a pruned tier's newest record, or a send attempt.
func noProgressLatest(j *incident.Journal, g *rungraph.Graph) time.Time {
	var latest time.Time
	later := func(t time.Time) {
		if t.After(latest) {
			latest = t
		}
	}
	for _, r := range j.Records {
		later(r.CheckStartedAt)
		if c := r.ControlChange; c != nil {
			later(c.SaveObservedAt)
		}
		if c := r.Collapsed; c != nil {
			later(c.LastAt)
		}
	}
	for _, tp := range []incident.TierPruned{j.Pruned.Key, j.Pruned.Condition, j.Pruned.Activity, j.Pruned.Routine} {
		if tp.NewestAt != nil {
			later(*tp.NewestAt)
		}
	}
	for _, a := range g.Attempts {
		later(a.RecordedAt)
	}
	return latest
}

// noProgressHeldOf is how long the loop was held for a person in the part of
// the window the records cover: from the later of since and coveredFrom (the
// newest pruned hold change) to now. In that span the hold's value before the
// first change is that change's "before", and after the last change it is
// that change's "after"; with no change it is the control record now.
func noProgressHeldOf(j *incident.Journal, ctl control.File, coveredFrom, since, now time.Time) *noProgressHeld {
	from := since
	if coveredFrom.After(from) {
		from = coveredFrom
	}
	h := &noProgressHeld{CoveredSeconds: int64(now.Sub(from) / time.Second), AtLeast: from.After(since)}
	type change struct {
		at            time.Time
		before, after bool
	}
	var changes []change
	for _, r := range j.Records {
		if r.ControlBefore == nil || r.ControlAfter == nil {
			continue
		}
		at := r.CheckStartedAt
		if c := r.ControlChange; c != nil && !c.SaveObservedAt.IsZero() {
			at = c.SaveObservedAt
		}
		if !at.After(from) || at.After(now) {
			continue
		}
		changes = append(changes, change{at, r.ControlBefore.AwaitingUser, r.ControlAfter.AwaitingUser})
	}
	held := ctl.AwaitingUser
	if len(changes) > 0 {
		held = changes[0].before
	}
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

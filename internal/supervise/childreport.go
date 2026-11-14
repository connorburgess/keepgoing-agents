package supervise

import (
	"fmt"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// childErrorEvent is the report event for child threads in error.
const childErrorEvent = "child_error"

// childErrorMaxReports bounds the reports of one episode, which lasts until
// every covered child has been out of error long enough to be forgotten.
// Children failing one after another therefore cannot produce a stream.
const childErrorMaxReports = 3

// childErrorMaxCovered bounds the episode's record. It matches the adapter's
// limit on children inspected in one listing.
const childErrorMaxCovered = 200

// reportChildErrors runs after the decision and its action, on the path where
// the target was observed. It never changes the decision, the action or a
// control. It tells the owner when child threads of the supervised thread are
// first seen in error, and again only when a child newly fails. The notice to
// the parent session is separate.
//
// The episode is saved to state.json before the send, so a crash or a restart
// cannot repeat it: a report is sent at most once, and an interrupted one is
// lost, like any other single-attempt delivery.
func (t *tick) reportChildErrors(target string, obs decide.Observation) {
	// A dry run records nothing. After a handoff or switch-back the children
	// seen belong to the thread the loop has just left. A parent that is gone
	// was not scanned for children at all, so it is no evidence of recovery.
	if t.opts.DryRun || t.res.Target != target || obs.Gone {
		return
	}
	complete := !obs.ChildrenUnverified
	prev := t.st.ChildErrorReport
	var covered []decide.CoveredChild
	reports := 0
	if prev != nil {
		covered, reports = prev.Children, prev.Reports
	}
	// A covered child is forgotten only after it has stayed out of error for
	// min_wake_interval, so a child flapping between error and a provider
	// retry is one failure, not a report per flap.
	kept, unreported := decide.CoveredChildren(decide.ChildHashes(obs.ErroredChildren), decide.ChildHashes(obs.HealthyChildren), covered, complete, t.now, t.loop.Progress.MinWakeInterval.Duration)
	if len(kept) == 0 {
		// Every covered child has been forgotten, so the episode is over,
		// whether or not a check ran in between to see it. A child failing on
		// this same check starts a new one: report 1, with the whole cap.
		reports = 0
		if len(unreported) == 0 {
			t.st.ChildErrorReport = nil
			return
		}
	}
	subscribed := t.loop.Report.ReportsEvent(childErrorEvent)
	outcome := decide.ChildErrorReport(len(unreported), reports, childErrorMaxReports, subscribed)
	if outcome == decide.ChildReportNone {
		if prev != nil {
			prev.Children = kept
		}
		return
	}
	rec := &state.ChildErrorReport{Children: kept, Reports: reports, Outcome: string(outcome), DetectedAt: t.now,
		Errored: len(obs.ErroredChildren), Running: obs.ActiveChildren, Complete: complete}
	switch outcome {
	case decide.ChildReportNotSubscribed:
		// Not covered, so subscribing later sends the report.
		if prev != nil && prev.Outcome == state.ChildReportNotSubscribed {
			rec.DetectedAt = prev.DetectedAt
		}
		t.st.ChildErrorReport = rec
		return
	case decide.ChildReportCapped:
		// Covered without a report, so later checks stay quiet too.
		rec.Children = decide.CoverChildren(kept, unreported, t.now, childErrorMaxCovered)
		t.st.ChildErrorReport = rec
		return
	}
	rec.Children, rec.Reports = decide.CoverChildren(kept, unreported, t.now, childErrorMaxCovered), reports+1
	// A report that cannot be sent in this check for lack of time must not use
	// up its single attempt: nothing is recorded, so the next check sends it.
	if !t.canReport(childErrorEvent) {
		t.warn("report %s put off to the next check: the network is down or no time is left in this one", childErrorEvent)
		return
	}
	// Intent first, as holds and completions are saved before their reports.
	// If it cannot be saved nothing is sent, so an unwritable state directory
	// cannot turn into a report on every check.
	saved, err := state.Load(t.loop.StateDir)
	if err == nil {
		saved.ChildErrorReport = rec
		err = state.Save(t.loop.StateDir, saved)
	}
	if err != nil {
		t.warn("report %s: not sent because the episode could not be recorded", childErrorEvent)
		return
	}
	t.st.ChildErrorReport = rec
	listing := "The child listing was complete."
	if !complete {
		listing = "The child listing was incomplete, so both counts are lower bounds."
	}
	text := fmt.Sprintf("%s: %d direct child thread(s) of the supervised thread are in error and %d are still running. %s keepgoing does not retry, message or stop child threads; whether that work is retried or replaced is up to the supervised session or a person.",
		t.loop.Name, rec.Errored, rec.Running, listing)
	if rec.Reports > 1 {
		text += " More children have failed since the last report for this episode."
	}
	ctx, cancel, ok := t.reportContext(childErrorEvent, text)
	if !ok {
		return // cannot happen: canReport was checked before the attempt was reserved
	}
	defer cancel()
	err = t.opts.Reporter.Report(ctx, report.Event{Type: childErrorEvent, Loop: t.loop.Name, Text: text, At: t.now,
		Data: map[string]any{"erroredChildren": rec.Errored, "runningChildren": rec.Running, "listingComplete": complete, "report": rec.Reports}})
	if err != nil {
		// One attempt: a failed delivery is recorded, not retried.
		rec.Outcome = state.ChildReportFailed
		t.warn("report %s: %v", childErrorEvent, err)
	}
}

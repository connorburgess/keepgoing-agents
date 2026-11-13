package supervise

import (
	"fmt"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// childErrorEvent is the report event for child threads in error.
const childErrorEvent = "child_error"

// childErrorMaxReports bounds the reports of one episode, which lasts while
// any child stays in error. Children failing one after another therefore
// cannot produce a stream.
const childErrorMaxReports = 3

// reportChildErrors runs after the decision and its action, on the path where
// the target was observed. It never changes the decision, the action or a
// control. It tells the owner when child threads of the supervised thread are
// first seen in error, and again only when a child newly fails. The notice to
// the parent session is separate and waits for the parent's next wake.
//
// The episode is saved to state.json before the send, so a crash or a restart
// cannot repeat it: a report is sent at most once, and an interrupted one is
// lost, like any other single-attempt delivery.
func (t *tick) reportChildErrors(target string, obs decide.Observation) {
	// A dry run records nothing. After a handoff or switch-back the children
	// seen belong to the thread the loop has just left.
	if t.opts.DryRun || t.res.Target != target {
		return
	}
	complete := !obs.ChildrenUnverified
	errored := decide.ChildHashes(obs.ErroredChildren)
	prev := t.st.ChildErrorReport
	var reported []string
	reports := 0
	if prev != nil {
		reported, reports = prev.Reported, prev.Reports
	}
	kept, unreported := decide.ChildReportRecord(errored, reported, complete)
	if len(errored) == 0 && complete {
		t.st.ChildErrorReport = nil // every child recovered: the episode is over
		return
	}
	subscribed := t.loop.Report.ReportsEvent(childErrorEvent)
	outcome := decide.ChildErrorReport(len(unreported), reports, childErrorMaxReports, subscribed)
	if outcome == decide.ChildReportNone {
		if prev != nil {
			prev.Reported = kept
		}
		return
	}
	rec := &state.ChildErrorReport{Reported: kept, Reports: reports, Outcome: string(outcome), DetectedAt: t.now,
		Errored: len(obs.ErroredChildren), Running: obs.ActiveChildren, Complete: complete}
	switch outcome {
	case decide.ChildReportNotSubscribed:
		// Not recorded as reported, so subscribing later sends the report.
		if prev != nil && prev.Outcome == state.ChildReportNotSubscribed {
			rec.DetectedAt = prev.DetectedAt
		}
		t.st.ChildErrorReport = rec
		return
	case decide.ChildReportCapped:
		// Covered without a report, so later checks stay quiet too.
		rec.Reported = append(kept, unreported...)
		t.st.ChildErrorReport = rec
		return
	}
	rec.Reported, rec.Reports = append(kept, unreported...), reports+1
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
	text := fmt.Sprintf("%s: %d direct child thread(s) of the supervised thread are in error and %d are still running. %s keepgoing does not retry, message or stop child threads; the supervised session is told when it is next woken, and decides whether to retry or replace that work.",
		t.loop.Name, rec.Errored, rec.Running, listing)
	if rec.Reports > 1 {
		text += " More children have failed since the last report for this episode."
	}
	err = t.opts.Reporter.Report(t.ctx, report.Event{Type: childErrorEvent, Loop: t.loop.Name, Text: text, At: t.now,
		Data: map[string]any{"erroredChildren": rec.Errored, "runningChildren": rec.Running, "listingComplete": complete, "report": rec.Reports}})
	if err != nil {
		// One attempt: a failed delivery is recorded, not retried.
		rec.Outcome = state.ChildReportFailed
		t.warn("report %s: %v", childErrorEvent, err)
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
)

// stopNowWait is how long a stop carried out at once waits for a check that
// holds the loop's check lock before leaving the stop to that loop's checks.
const stopNowWait = 5 * time.Second

// stopNowResult is what carrying out a stop in the command did.
type stopNowResult struct {
	// CarriedOut is "now" when this command ran the stop, through the same
	// stop path a check uses, and recorded it as a check; "left_to_check"
	// when it saved the request and a check carries it out.
	CarriedOut string `json:"carriedOut"`
	// Why says why it was left to a check: check_running (another check held
	// the lock for the whole wait), session_claimed (another loop's live
	// check holds the session), cancelled, sources_changed or
	// lock_unavailable.
	Why string `json:"why,omitempty"`
	// Action and Reason are the stop check's, when it ran.
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Recorded: the stop check was recorded in history, as a check is.
	Recorded bool `json:"recorded,omitempty"`
	// Lanes is the stop of the lanes, when it was asked for and ran.
	Lanes *state.LaneStop `json:"lanes,omitempty"`
	// Warnings are the stop check's, and the command's own.
	Warnings []string `json:"warnings,omitempty"`
}

// failed reports whether the stop was saved but could not be carried out
// or left to a check as it should have been: the command's exit code is 1.
// A stop the check ran and recorded, even one whose observation failed, and
// one left to a running check or to another loop's claim, are not failures.
func (r stopNowResult) failed(err error) bool {
	switch {
	case r.CarriedOut == "now":
		return err != nil && !r.Recorded
	case r.Why == "check_running", r.Why == "session_claimed", r.Why == "bb_not_found":
		return false
	}
	return true
}

// stopNow carries out a requested stop at once, under the loop's check lock,
// by running one check that does nothing but the stop (supervise.Options
// StopOnly). Session claims apply exactly as in any check. It needs no loop
// ownership: the check does only what the next check would do for the saved
// stop, and nothing if a resume has lifted it. If another check holds the
// lock for longer than wait, that check, or the next one, carries the stop
// out. caller is the BB thread the command runs in, or "" for the panel and
// the menu; a thread the stop covers that is the caller's is stopped last.
func stopNow(ctx context.Context, cfg *config.Config, wait time.Duration, caller string) (stopNowResult, error) {
	r := stopNowResult{CarriedOut: "left_to_check"}
	deadline := time.Now().Add(wait)
	var l *lock.Lock
	for {
		var err error
		l, err = lock.Acquire(cfg.Loop.StateDir)
		if err == nil {
			break
		}
		if !errors.Is(err, lock.ErrHeld) {
			r.Why = "lock_unavailable"
			return r, err
		}
		if ctx.Err() != nil {
			r.Why = "cancelled"
			return r, ctx.Err()
		}
		if time.Now().After(deadline) {
			r.Why = "check_running"
			return r, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer l.Release()
	if err := checkTransferSources(cfg, true); err != nil {
		r.Why = "sources_changed"
		return r, err
	}
	if !supervise.StopReachable(cfg) {
		// Said plainly, instead of a check that cannot list or stop anything
		// and a listing tried again for three checks.
		r.Why = "bb_not_found"
		return r, nil
	}
	var rep report.Reporter = report.Nop{}
	if cfg.Loop.Report != nil {
		w, err := report.NewRecorded(*cfg.Loop.Report, cfg.Loop.StateDir)
		if err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("reports disabled: %v", err))
		}
		rep = w
	}
	opts := supervise.Options{Version: version, Reporter: rep, StopOnly: true, CallerThread: caller}
	if deadline, ok := ctx.Deadline(); ok {
		// A caller with less time than a check (the page and the menu give a
		// change 20 seconds) makes that the check's hard limit, so the check
		// ends on its own terms, records itself, and leaves what it could not
		// reach to the next check.
		opts.Deadline = deadline.Add(-2 * time.Second)
	}
	res, err := supervise.Tick(ctx, cfg, opts)
	r.Warnings = append(r.Warnings, res.Warnings...)
	var claimed *session.ClaimedError
	if errors.As(err, &claimed) {
		// Another loop's live check holds the session: as for any stop, the
		// next check tries again.
		r.Why = "session_claimed"
		return r, err
	}
	r.CarriedOut, r.Action, r.Reason, r.Recorded, r.Lanes = "now", res.Action, res.Reason, res.Recorded, res.LaneStop
	return r, err
}

// stopCmd is keepgoing stop: save the request, then with now carry it out.
func stopCmd(e env, cfg *config.Config, note string, lanes, dropQueued, now bool) int {
	code := controlCmdLanes(e, cfg, "stop", note, lanes, dropQueued)
	if code != exitOK || !now {
		if code == exitOK && lanes {
			fmt.Fprintln(e.stdout, "the next check carries out the stop, and stops the lanes")
			if dropQueued {
				fmt.Fprintln(e.stdout, "it also deletes the messages queued in lanes that have not started; their text is not kept")
			}
		}
		return code
	}
	caller := os.Getenv("BB_THREAD_ID")
	if caller != "" {
		// Said before anything is stopped: if the stop covers this command's
		// own thread and ends it, nothing after this is printed.
		fmt.Fprintf(e.stdout, "this command runs in BB thread %s; if the stop covers it, it is stopped last, and if that ends this command the next check finishes the rest\n", caller)
	}
	r, err := stopNow(e.ctx, cfg, stopNowWait, caller)
	printStopNow(e.stdout, cfg, r)
	if r.failed(err) {
		e.errf("stop saved, but not carried out now: %v; the next check carries it out", err)
		return exitFailed
	}
	return exitOK
}

// printStopNow says what a stop carried out at once did.
func printStopNow(w io.Writer, cfg *config.Config, r stopNowResult) {
	if r.CarriedOut == "now" {
		fmt.Fprintf(w, "stop carried out now: %s (%s)\n", r.Action, r.Reason)
	} else {
		fmt.Fprintf(w, "stop saved and left to the loop's checks: %s\n", leftBecause(cfg, r.Why))
	}
	if l := r.Lanes; l != nil {
		fmt.Fprintln(w, describeLaneStop(*l))
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

// leftBecause says in words why a stop was left to a check.
func leftBecause(cfg *config.Config, why string) string {
	switch why {
	case "check_running":
		return fmt.Sprintf("a check of %s was running for all of %s; one that read control.json before the stop was saved does not carry it out, and the next one does", cfg.Loop.Name, stopNowWait)
	case "session_claimed":
		return "another loop's check is acting on this session; the next check stops it"
	case "cancelled":
		return "the command was cancelled before it could take the check lock"
	case "sources_changed":
		return "the loaded configuration or fallbacks changed on disk; the next check, with the current files, carries it out"
	case "lock_unavailable":
		return "the check lock could not be taken"
	case "bb_not_found":
		return "bb could not be found from here; the next check carries out the stop, and stops the lanes if they were asked for"
	}
	return "the next check carries it out"
}

// describeLaneStop is one line about a stop of the lanes, by count.
func describeLaneStop(l state.LaneStop) string {
	switch l.Outcome {
	case "not_applicable":
		return "lanes: not applicable; this adapter has no child conversations"
	case "dry_run":
		return "lanes: would be looked at and stopped (dry run)"
	}
	failed, classes := 0, []string{}
	for c, n := range l.Failed {
		failed += n
		classes = append(classes, fmt.Sprintf("%s %d", c, n))
	}
	sort.Strings(classes)
	notChecked := fmt.Sprint(l.NotChecked)
	if l.NotCheckedAtLeast {
		notChecked = "at least " + notChecked
	}
	s := fmt.Sprintf("lanes: %d stopped, %d already idle, %d left to another loop, %d failed, %s not checked", l.Stopped, l.AlreadyIdle, l.OtherLoop, failed, notChecked)
	if len(classes) > 0 {
		s += " (" + strings.Join(classes, ", ") + ")"
	}
	if l.RetriesCancelled > 0 {
		s += fmt.Sprintf("; %d queued provider retry(s) cancelled", l.RetriesCancelled)
	}
	if l.QueuedNotCancelled > 0 {
		s += fmt.Sprintf("; %d queued retry(s) not cancelled", l.QueuedNotCancelled)
	}
	if l.ListingsUnread > 0 {
		s += fmt.Sprintf("; %d listing(s) could not be read, so nothing below them was checked", l.ListingsUnread)
	}
	if l.NotStarted > 0 {
		s += fmt.Sprintf("; %d not started: %d queued message(s) kept; BB may run them", l.NotStarted, l.QueuedKept)
	}
	if l.Dequeued > 0 {
		s += fmt.Sprintf("; %d queued message(s) deleted from %d lane(s), their text not kept", l.QueuedDeleted, l.Dequeued)
	}
	switch {
	case l.Outcome != "incomplete":
	case l.IncompleteBecause == "listing":
		s += "; a lane listing could not be read, so the next check tries again"
	default:
		s += "; not finished in this check's time, the next check goes on"
	}
	return s
}

// stopWarningCodes are the fixed codes a client is given for the stop
// check's warnings, each with its plain words. A warning's own text can name
// paths and errors, so a client never gets it.
var stopWarningCodes = []struct{ match, code, words string }{
	{"lane stops cannot be recorded", "lane_stops_unrecorded", "lane stops could not be recorded before they were made; they were made anyway"},
	{"a lane stop could not be recorded", "lane_stop_unrecorded", "a lane stop could not be recorded before it was made; a later check may make it again"},
	{"cannot cancel a queued provider retry", "provider_retry_cancel_unsupported", "this BB cannot cancel a queued provider retry"},
	{"not finished in this check's time", "lanes_out_of_time", "the lanes were not all reached in the time given; the next check goes on"},
	{"a lane listing could not be read", "lane_listing_unreadable", "a lane listing could not be read; the next check tries again"},
	{"it is stopped last", "caller_stopped_last", "this command's own thread is stopped last"},
	{"reports disabled", "reports_disabled", "reports could not be set up, so none was sent"},
	{"the stop could not be recorded", "stop_unrecorded", "the stop could not be recorded before it was made"},
	{"session claim", "session_claim_unavailable", "the session claim could not be taken; the stop went ahead without it"},
}

// stopWarningCode is a warning's fixed code; any other warning is
// check_warning, which status shows in full.
func stopWarningCode(w string) string {
	for _, c := range stopWarningCodes {
		if strings.Contains(w, c.match) {
			return c.code
		}
	}
	return "check_warning"
}

// stopWarningWords are a code's plain words.
func stopWarningWords(code string) string {
	for _, c := range stopWarningCodes {
		if c.code == code {
			return c.words
		}
	}
	return "the stop check recorded a warning; keepgoing status shows it"
}

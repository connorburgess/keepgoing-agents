package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/report"
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
	// Why says why it was left to a check: check_running (another check
	// held the lock for the whole wait), sources_changed, lock_unavailable,
	// or not_asked (no --now).
	Why string `json:"why,omitempty"`
	// Action and Reason are the stop check's, when it ran.
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Recorded: the stop check was recorded in history, as a check is.
	Recorded bool `json:"recorded,omitempty"`
	// Lanes is the stop of the lanes, when it was asked for and ran.
	Lanes *state.LaneStop `json:"lanes,omitempty"`
}

// stopNow carries out a requested stop at once, under the loop's check lock,
// by running one check that does nothing but the stop (supervise.Options
// StopOnly). Session claims apply exactly as in any check. It needs no loop
// ownership: anyone who may ask for a stop may have it carried out, and the
// check does nothing a stop does not. If another check holds the lock for
// longer than wait, that check, or the next one, carries the stop out.
func stopNow(ctx context.Context, cfg *config.Config, wait time.Duration) (stopNowResult, error) {
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
		if time.Now().After(deadline) || ctx.Err() != nil {
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
	var rep report.Reporter = report.Nop{}
	if cfg.Loop.Report != nil {
		if w, err := report.NewRecorded(*cfg.Loop.Report, cfg.Loop.StateDir); err == nil {
			rep = w
		}
	}
	res, err := supervise.Tick(ctx, cfg, supervise.Options{Version: version, Reporter: rep, StopOnly: true})
	r.CarriedOut, r.Action, r.Reason, r.Recorded, r.Lanes = "now", res.Action, res.Reason, res.Recorded, res.LaneStop
	return r, err
}

// stopCmd is keepgoing stop: save the request, then with now carry it out.
func stopCmd(e env, cfg *config.Config, note string, lanes, now bool) int {
	code := controlCmdLanes(e, cfg, "stop", note, lanes)
	if code != exitOK || !now {
		if code == exitOK && lanes {
			fmt.Fprintln(e.stdout, "the next check carries out the stop, and stops the lanes")
		}
		return code
	}
	r, err := stopNow(e.ctx, cfg, stopNowWait)
	printStopNow(e.stdout, cfg, r)
	if err != nil && r.CarriedOut != "now" {
		e.errf("stop not carried out now: %v; the next check carries it out", err)
		return exitFailed
	}
	if err != nil && !r.Recorded {
		e.errf("stop: %v", err)
		return exitFailed
	}
	return exitOK
}

// printStopNow says what a stop carried out at once did.
func printStopNow(w io.Writer, cfg *config.Config, r stopNowResult) {
	switch r.CarriedOut {
	case "now":
		fmt.Fprintf(w, "stop carried out now: %s (%s)\n", r.Action, r.Reason)
	default:
		switch r.Why {
		case "check_running":
			fmt.Fprintf(w, "a check of %s was running for all of %s; the stop is left to the checks: one that read control.json before the stop was saved does not carry it out, and the next one does\n", cfg.Loop.Name, stopNowWait)
		default:
			fmt.Fprintln(w, "the stop was not carried out now; the next check carries it out")
		}
	}
	if l := r.Lanes; l != nil {
		fmt.Fprintln(w, describeLaneStop(*l))
	}
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
	if l.Outcome == "incomplete" {
		s += "; not finished in this check's time, the next check goes on"
	}
	return s
}

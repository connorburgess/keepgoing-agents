package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const (
	// laneStopAction is a lane stop in sends.json. Like the supervised
	// conversation's own stop it is recorded but never counted against
	// loop.limits.max_sends.
	laneStopAction = "stop:lane"
	// laneStopReport is the report key of a stop that covered the lanes.
	laneStopReport = "stop_lanes"
	// laneStopMargin: no further thread is looked at with less than this left
	// of the check's time. The next check goes on from where this one ended.
	laneStopMargin = 10 * time.Second
	// laneStopCall bounds one bb command of the walk.
	laneStopCall = 20 * time.Second
)

// stopLanes carries out a stop that covers the lanes (keepgoing stop
// --lanes). It walks the supervised conversation's verified descendants, at
// most decide.LaneStopMaxDepth deep and decide.LaneStopMaxThreads in all, and
// stops those that are running or in error with a retry waiting. It sends a
// lane nothing but a stop. A lane another loop supervises is left to that
// loop, and so is everything below it.
//
// Every stop is recorded in sends.json before it is made, under the lane's
// keyed hash and this request. A check killed in the middle is followed by
// one that reads those records: a recorded lane no longer running was
// stopped and is not stopped again; one still running is stopped.
//
// It runs on the stop path, so it does not depend on the loop being enabled,
// unpaused or free of holds: stop wins. It is a no-op once this request is
// done, and in a dry run.
func (t *tick) stopLanes(a adapter.Adapter, parentStopped bool) {
	if t.stopLanesFor.IsZero() {
		return
	}
	prev := t.st.LaneStop
	if prev != nil && !prev.For.Equal(t.stopLanesFor) {
		prev = nil
	}
	if !decide.LaneStopsDue(decide.Control{Stop: true}, true, prev != nil && prev.Outcome != "incomplete", t.opts.DryRun) {
		if t.opts.DryRun {
			t.res.LaneStop = &state.LaneStop{For: t.stopLanesFor, At: t.now, Outcome: "dry_run"}
		}
		return
	}
	t.laneStopRecords()
	rec := &state.LaneStop{For: t.stopLanesFor, At: t.now, Outcome: "done", Failed: map[string]int{}}
	if prev != nil {
		// An incomplete pass goes on: what it stopped is counted again below
		// from the recorded stops, and only the report flag carries over.
		rec.ParentStopped, rec.Reported = prev.ParentStopped, prev.Reported
	}
	rec.ParentStopped = rec.ParentStopped || parentStopped
	t.st.LaneStop, t.res.LaneStop = rec, rec
	defer func() {
		if len(rec.Failed) == 0 {
			rec.Failed = nil
		}
		t.reportLaneStop(rec)
	}()
	ls, ok := a.(adapter.LaneStopper)
	if !ok {
		rec.Outcome = "not_applicable"
		return
	}
	key, err := state.LaneKey(t.loop.StateDir)
	if err != nil {
		// Without the key no stop can be recorded under a name that a later
		// check would recognise. A stop is still made: stopping a lane twice
		// is harmless, and not stopping it when asked is the worse failure.
		t.warn("lane stops cannot be recorded before they are made (%v); they are made anyway", err)
	}
	name := func(id string) string {
		if key == nil {
			return ""
		}
		return state.LaneHash(key, id)
	}
	budget := decide.LaneStopMaxThreads
	level := []string{ls.LaneRoot()}
	seen := map[string]bool{ls.LaneRoot(): true}
	warnedCancel := false
	for depth := 1; len(level) > 0; depth++ {
		var next []string
		for _, p := range level {
			if t.left() < laneStopMargin {
				// Out of time: the next check goes on with this request.
				rec.Outcome = "incomplete"
				rec.NotCheckedAtLeast = true
				t.warn("the stop of the lanes was not finished in this check's time; the next check goes on with it")
				return
			}
			if depth > decide.LaneStopMaxDepth || budget <= 0 {
				// Past a bound: count the children, look at none of them.
				// What is below those is not known at all.
				_, n, err := t.laneChildren(ls, p, 0)
				rec.NotChecked += n
				if err != nil || n > 0 {
					rec.NotCheckedAtLeast = true
				}
				continue
			}
			kids, notChecked, err := t.laneChildren(ls, p, budget)
			if err != nil {
				rec.Failed["listing_"+laneErrorClass(err)]++
				continue
			}
			rec.NotChecked += notChecked
			if notChecked > 0 {
				rec.NotCheckedAtLeast = true // and below those, unknown
			}
			for _, k := range kids {
				if seen[k.ID] {
					continue // listed twice, or under itself: looked at once
				}
				seen[k.ID] = true
				budget--
				h := name(k.ID)
				n := decide.LaneNode{Verified: k.Verified, Status: k.Status, Queued: k.Queued, StopRecorded: h != "" && t.laneStopsRecorded[h]}
				n.OtherLoop = k.Verified && t.otherLoopLane(k.ID)
				action := decide.LaneStopFor(n)
				switch action {
				case decide.LaneStopUnverified, decide.LaneStopUnknown:
					rec.Failed[string(action)]++
				case decide.LaneStopOtherLoop:
					rec.OtherLoop++
				case decide.LaneStopIdle:
					rec.AlreadyIdle++
				case decide.LaneStopStoppedEarlier:
					rec.Stopped++
				case decide.LaneStopRunning, decide.LaneStopQueued:
					if action == decide.LaneStopQueued {
						cancelled, err := t.cancelLaneRetry(ls, k.ID)
						switch {
						case cancelled:
							rec.RetriesCancelled++
						case errors.As(err, new(adapter.ErrUnsupported)):
							rec.QueuedNotCancelled++
							if !warnedCancel {
								warnedCancel = true
								t.warn("this BB cannot cancel a queued provider retry; lanes in error with a retry queued are stopped, and their queued retries are not cancelled")
							}
						default:
							// No provider retry was waiting (the queued row is
							// something else), or the cancel failed.
							rec.QueuedNotCancelled++
						}
					}
					if h != "" {
						if _, err := t.reserveSend(laneStopAction, h, t.stopLanesFor.Format(time.RFC3339Nano)); err != nil {
							t.warn("a lane stop could not be recorded before it was made, so a later check may make it again: %v", err)
						}
					}
					if err := t.stopLane(ls, k.ID); err != nil {
						rec.Failed[laneErrorClass(err)]++
					} else {
						rec.Stopped++
					}
				}
				if decide.LaneStopDescend(action) {
					next = append(next, k.ID)
				}
			}
		}
		if depth > decide.LaneStopMaxDepth || budget <= 0 && len(next) == 0 {
			break
		}
		level = next
	}
}

// laneStopRecords reads the lane stops, and the report, that an earlier check
// recorded for this request without saving what happened. Such a stop is
// never taken as made: the lane is looked at again and stopped only if it is
// still running (decide.LaneStopFor). The report is taken as sent: it is
// sent at most once. t.savedSends is where the records this check found
// unsettled begin, whether or not it has settled them since.
func (t *tick) laneStopRecords() {
	l, err := state.LoadLedger(t.loop.StateDir)
	if err != nil {
		return
	}
	want := t.stopLanesFor.Format(time.RFC3339Nano)
	for _, p := range l.Unrecorded(max(t.savedSends, l.Settled)) {
		if p.Key != want {
			continue
		}
		switch p.Action {
		case laneStopAction:
			if t.laneStopsRecorded == nil {
				t.laneStopsRecorded = map[string]bool{}
			}
			t.laneStopsRecorded[p.Target] = true
		case "report:" + laneStopReport:
			t.laneStopReportRecorded = true
		}
	}
}

func (t *tick) laneCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(t.ctx, max(time.Second, min(laneStopCall, t.left()-laneStopMargin/2)))
}

func (t *tick) laneChildren(ls adapter.LaneStopper, parent string, budget int) ([]adapter.LaneInfo, int, error) {
	ctx, cancel := t.laneCtx()
	defer cancel()
	return ls.LaneChildren(ctx, parent, budget)
}

func (t *tick) stopLane(ls adapter.LaneStopper, id string) error {
	ctx, cancel := t.laneCtx()
	defer cancel()
	return ls.StopLane(ctx, id)
}

func (t *tick) cancelLaneRetry(ls adapter.LaneStopper, id string) (bool, error) {
	ctx, cancel := t.laneCtx()
	defer cancel()
	return ls.CancelLaneRetry(ctx, id)
}

// laneErrorClass is a failed lane command's class from a structured source.
func laneErrorClass(err error) string {
	var e *adapter.LaneStopError
	if errors.As(err, &e) && e.Class != "" {
		return e.Class
	}
	return "failed"
}

// otherLoopLane reports whether another keepgoing loop supervises the lane,
// by the session records every loop keeps (#195), or holds its session claim
// now. That loop's own stop is the way to stop it.
func (t *tick) otherLoopLane(id string) bool {
	dir, err := t.sessionDirectory()
	if err != nil {
		return false
	}
	sid := session.Identity{Kind: "bb", ID: id}
	mine := session.CleanPath(t.loop.StateDir)
	for _, s := range session.Known(dir, sid) {
		if s.StateDir != "" && s.StateDir != mine {
			return true
		}
	}
	if h := session.Peek(dir, sid); h != nil && h.PID != os.Getpid() && h.Loop != t.loop.Name {
		return true
	}
	return false
}

// reportLaneStop sends the one report of a stop that covered the lanes, when
// the walk is finished. It is recorded before it is sent, so a check killed
// while sending it does not send it again.
func (t *tick) reportLaneStop(rec *state.LaneStop) {
	if rec.Outcome == "incomplete" || rec.Reported || t.laneStopReportRecorded || t.opts.DryRun {
		rec.Reported = rec.Reported || t.laneStopReportRecorded
		return
	}
	if _, err := t.reserveSend("report:"+laneStopReport, t.res.Target, t.stopLanesFor.Format(time.RFC3339Nano)); err != nil {
		t.warn("the stop report is not sent, because the record of sending it could not be saved: %v", err)
		return
	}
	rec.Reported = true
	failed := 0
	classes := make([]string, 0, len(rec.Failed))
	for c, n := range rec.Failed {
		failed += n
		classes = append(classes, fmt.Sprintf("%s %d", c, n))
	}
	sort.Strings(classes)
	parent := t.res.Target + " was not running"
	switch {
	case rec.ParentStopped:
		parent = "stopped " + t.res.Target
	case t.st.StopRequestedSent:
		parent = t.res.Target + " was stopped by an earlier check for this stop"
	}
	var text string
	if rec.Outcome == "not_applicable" {
		text = fmt.Sprintf("%s: %s; lanes: not applicable, this adapter has no child conversations", t.loop.Name, parent)
	} else {
		notChecked := fmt.Sprint(rec.NotChecked)
		if rec.NotCheckedAtLeast {
			notChecked = "at least " + notChecked
		}
		text = fmt.Sprintf("%s: %s; lanes: %d stopped, %d already idle, %d left to another loop, %d failed, %s not checked (past %d levels or %d threads)",
			t.loop.Name, parent, rec.Stopped, rec.AlreadyIdle, rec.OtherLoop, failed, notChecked, decide.LaneStopMaxDepth, decide.LaneStopMaxThreads)
		if len(classes) > 0 {
			text += " [" + strings.Join(classes, ", ") + "]"
		}
		if rec.QueuedNotCancelled > 0 {
			text += fmt.Sprintf("; %d queued retry(s) were not cancelled", rec.QueuedNotCancelled)
		}
	}
	lanes := map[string]any{"outcome": rec.Outcome, "stopped": rec.Stopped, "alreadyIdle": rec.AlreadyIdle, "otherLoop": rec.OtherLoop,
		"failed": rec.Failed, "notChecked": rec.NotChecked, "notCheckedAtLeast": rec.NotCheckedAtLeast,
		"retriesCancelled": rec.RetriesCancelled, "queuedNotCancelled": rec.QueuedNotCancelled}
	t.report("stop", text, map[string]any{"target": t.res.Target, "parentStopped": rec.ParentStopped, "lanes": lanes})
}

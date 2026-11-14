package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/config"
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
	laneStopMargin = 3 * time.Second
	// laneStopCall bounds one bb command of the walk.
	laneStopCall = 20 * time.Second
)

// laneStopHook is a test seam: it runs just before a lane is stopped.
var laneStopHook func(id string)

// Parent states of a stop that covers the lanes (state.LaneStop.Parent).
const (
	parentStopped        = "stopped"
	parentStopFailed     = "stop_failed"
	parentStoppedEarlier = "stopped_earlier"
	parentNotRunning     = "not_running"
	parentNotObserved    = "not_observed"
)

// stopLanes carries out a stop that covers the lanes (keepgoing stop
// --lanes). It walks the supervised conversation's verified descendants, at
// most decide.LaneStopMaxDepth deep and decide.LaneStopMaxThreads in all. It
// stops those that are running or in error with a retry waiting, after
// cancelling a provider retry waiting in one, and deletes the messages
// queued in those that have not started (pending) or are idle with a message
// queued, so they do not start. That is all it does to a lane. A lane another
// loop supervises is left to that loop, and so is everything below it; so is
// one whose ownership cannot be established.
//
// Every stop and deletion is recorded in sends.json before it is made, under
// the lane's keyed hash and this request. A check killed in the middle is
// followed by one that reads those records: a recorded lane no longer
// running was stopped and is not stopped again; one still running is
// stopped. A pass that is continued by a later check (it ran out of time, or
// a listing could not be read) counts the lanes the earlier pass stopped or
// emptied as it found them, whatever they are doing now, and never retries a
// lane whose stop failed.
//
// A check that has not read the send records (one that could not observe its
// target) settles nothing: the records it adds here stay unsettled beside
// the ones it did not read, for the next check that does.
//
// It runs on the stop path, so it does not depend on the loop being enabled,
// unpaused or free of holds: stop wins. It is a no-op once this request is
// done, and in a dry run. The report is sent by finishLaneStop, once the
// supervised conversation's own stop is known.
func (t *tick) stopLanes(a adapter.Adapter) {
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
	doneBefore, dequeuedBefore, failedBefore := map[string]bool{}, map[string]bool{}, map[string]string{}
	if prev != nil {
		rec.Reported, rec.ListingTries, rec.Parent = prev.Reported, prev.ListingTries, prev.Parent
		for _, h := range prev.StoppedLanes {
			doneBefore[h] = true
		}
		for _, h := range prev.DequeuedLanes {
			dequeuedBefore[h] = true
		}
		for h, c := range prev.FailedLanes {
			failedBefore[h] = c
		}
	}
	t.st.LaneStop, t.laneStop = rec, rec
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
	add := func(list *[]string, h string) {
		if h != "" && !slices.Contains(*list, h) {
			*list = append(*list, h)
		}
	}
	failed := func(h, class string) {
		rec.Failed[class]++
		if h != "" {
			if rec.FailedLanes == nil {
				rec.FailedLanes = map[string]string{}
			}
			rec.FailedLanes[h] = class
		}
	}
	record := func(h string) {
		if h == "" {
			return
		}
		if _, err := t.reserveSend(laneStopAction, h, t.stopLanesFor.Format(time.RFC3339Nano)); err != nil {
			t.warn("a lane stop could not be recorded before it was made, so a later check may make it again: %v", err)
		}
	}
	warnedCancel := false
	act := func(k adapter.LaneInfo, h string, action decide.LaneStopAction) {
		if action == decide.LaneStopDequeue {
			record(h)
			if laneStopHook != nil {
				laneStopHook(k.ID)
			}
			n, err := t.deleteLaneQueue(ls, k.ID)
			switch {
			case err != nil:
				failed(h, laneErrorClass(err))
			case n > 0:
				rec.Dequeued++
				add(&rec.DequeuedLanes, h)
			case k.Status == "pending":
				// Nothing queued to delete, and no session to stop: BB may
				// still run its first message.
				rec.NotStarted++
			default:
				rec.AlreadyIdle++
			}
			return
		}
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
				// No provider retry was waiting (the queued row is something
				// else), or the cancel failed.
				rec.QueuedNotCancelled++
			}
		}
		record(h)
		if laneStopHook != nil {
			laneStopHook(k.ID)
		}
		if err := t.stopLane(ls, k.ID); err != nil {
			failed(h, laneErrorClass(err))
		} else {
			rec.Stopped++
			add(&rec.StoppedLanes, h)
		}
	}
	// The caller's own thread, when the command runs inside one the stop
	// covers, is acted on last: its stop may end the command.
	var own *adapter.LaneInfo
	var ownAction decide.LaneStopAction
	listingFailed := false
	budget := decide.LaneStopMaxThreads
	root := ls.LaneRoot()
	level := []string{root}
	seen := map[string]bool{root: true}
	outOfTime := func() bool {
		if t.left() >= laneStopMargin {
			return false
		}
		rec.Outcome, rec.IncompleteBecause, rec.NotCheckedAtLeast = "incomplete", "time", true
		t.warn("the stop of the lanes was not finished in this check's time; the next check goes on with it")
		return true
	}
walk:
	for depth := 1; len(level) > 0; depth++ {
		var next []string
		for _, p := range level {
			if outOfTime() {
				break walk
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
				// Nothing below it could be looked at. The request is not
				// used up by it: see below.
				listingFailed = true
				if rec.ListingTries+1 >= state.LaneStopListingTries {
					rec.ListingsUnread++
				}
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
				if k.Verified {
					n.OtherLoop, n.OwnershipUnknown = t.otherLoopLane(k.ID)
				}
				action := decide.LaneStopFor(n)
				switch {
				case h != "" && doneBefore[h] && decide.LaneStopDescend(action):
					// Stopped by an earlier pass of this request: counted so,
					// whatever it is doing now. A person may have restarted it.
					rec.Stopped++
					add(&rec.StoppedLanes, h)
					action = decide.LaneStopIdle
				case h != "" && dequeuedBefore[h] && decide.LaneStopDescend(action):
					rec.Dequeued++
					add(&rec.DequeuedLanes, h)
					action = decide.LaneStopIdle
				case h != "" && failedBefore[h] != "" && (action == decide.LaneStopRunning || action == decide.LaneStopQueued || action == decide.LaneStopDequeue):
					// Its stop failed in an earlier pass: not tried again.
					failed(h, failedBefore[h])
					action = decide.LaneStopIdle
				default:
					switch action {
					case decide.LaneStopUnverified, decide.LaneStopUnknown, decide.LaneStopOwnershipUnknown:
						rec.Failed[string(action)]++
					case decide.LaneStopOtherLoop:
						rec.OtherLoop++
					case decide.LaneStopIdle:
						rec.AlreadyIdle++
					case decide.LaneStopStoppedEarlier:
						rec.Stopped++
						add(&rec.StoppedLanes, h)
					case decide.LaneStopRunning, decide.LaneStopQueued, decide.LaneStopDequeue:
						if t.opts.CallerThread != "" && k.ID == t.opts.CallerThread {
							kk := k
							own, ownAction = &kk, action
						} else {
							if outOfTime() {
								break walk
							}
							act(k, h, action)
						}
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
	if listingFailed && rec.Outcome != "incomplete" {
		rec.ListingTries++
		if rec.ListingTries < state.LaneStopListingTries {
			// Nothing below a listing that could not be read was looked at:
			// the next check tries again.
			rec.Outcome, rec.IncompleteBecause = "incomplete", "listing"
			t.warn("a lane listing could not be read; the next check tries the stop of the lanes again (%d of %d)", rec.ListingTries, state.LaneStopListingTries)
		} else {
			rec.NotCheckedAtLeast = true
		}
	}
	// Last of all, and only once the walk has finished or been given up.
	if own != nil && rec.Outcome != "incomplete" {
		t.warn("this command runs in a lane the stop covers; it is stopped last")
		act(*own, name(own.ID), ownAction)
	}
}

// finishLaneStop records what is known of the supervised conversation's
// own stop and sends the one report, once the walk is finished. It is called
// after that stop has been made or not.
func (t *tick) finishLaneStop(parent string) {
	rec := t.laneStop
	if rec == nil {
		return
	}
	if rec.Parent == "" || parent == parentStopped || parent == parentStopFailed {
		rec.Parent = parent
	} else if parent == parentStoppedEarlier && rec.Parent != parentStopped && rec.Parent != parentStopFailed {
		rec.Parent = parent
	}
	if len(rec.Failed) == 0 {
		rec.Failed = nil
	}
	if len(rec.FailedLanes) == 0 {
		rec.FailedLanes = nil
	}
	view := rec.Counts()
	t.res.LaneStop = &view
	t.reportLaneStop(rec)
	t.res.LaneStop.Reported = rec.Reported
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

func (t *tick) deleteLaneQueue(ls adapter.LaneStopper, id string) (int, error) {
	ctx, cancel := t.laneCtx()
	defer cancel()
	return ls.DeleteLaneQueue(ctx, id)
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
// by the session records every loop keeps, or holds its session claim now.
// That loop's own stop is the way to stop it. unknown: that could not be
// established (no session directory, or a record that cannot be read), and
// the lane is left alone: it might be another loop's.
func (t *tick) otherLoopLane(id string) (other, unknown bool) {
	dir, err := t.sessionDirectory()
	if err != nil {
		return false, true
	}
	sid := session.Identity{Kind: "bb", ID: id}
	known, err := session.Recorded(dir, sid)
	if err != nil {
		return false, true
	}
	mine := session.CleanPath(t.loop.StateDir)
	for _, s := range known {
		if s.StateDir != mine {
			return true, false
		}
	}
	if h := session.Peek(dir, sid); h != nil && h.PID != os.Getpid() && h.Loop != t.loop.Name {
		return true, false
	}
	return false, false
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
	parent := map[string]string{
		parentStopped:        "stopped " + t.res.Target,
		parentStopFailed:     "the stop of " + t.res.Target + " failed",
		parentStoppedEarlier: t.res.Target + " was stopped by an earlier check for this stop",
		parentNotRunning:     t.res.Target + " was not running",
		parentNotObserved:    t.res.Target + " could not be observed, so it is not known whether it was running",
	}[rec.Parent]
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
		if rec.Dequeued > 0 {
			text += fmt.Sprintf("; %d not started had their queued messages deleted", rec.Dequeued)
		}
		if rec.NotStarted > 0 {
			text += fmt.Sprintf("; %d not started, with nothing queued to delete: BB may still run its first message, so stop them in BB", rec.NotStarted)
		}
		if len(classes) > 0 {
			text += " [" + strings.Join(classes, ", ") + "]"
		}
		if rec.QueuedNotCancelled > 0 {
			text += fmt.Sprintf("; %d queued retry(s) were not cancelled", rec.QueuedNotCancelled)
		}
		if rec.ListingsUnread > 0 {
			text += fmt.Sprintf("; %d listing(s) could not be read in %d checks, so nothing below them was checked", rec.ListingsUnread, state.LaneStopListingTries)
		}
	}
	lanes := map[string]any{"outcome": rec.Outcome, "stopped": rec.Stopped, "alreadyIdle": rec.AlreadyIdle, "otherLoop": rec.OtherLoop,
		"failed": rec.Failed, "notChecked": rec.NotChecked, "notCheckedAtLeast": rec.NotCheckedAtLeast,
		"retriesCancelled": rec.RetriesCancelled, "queuedNotCancelled": rec.QueuedNotCancelled, "dequeued": rec.Dequeued, "notStarted": rec.NotStarted}
	lanes["listingsUnread"] = rec.ListingsUnread
	t.report("stop", text, map[string]any{"target": t.res.Target, "parent": rec.Parent, "lanes": lanes})
}

// LaneCount is a read-only look at a loop's lanes: how many a stop that
// covers the lanes would stop now. It is what a review offers.
type LaneCount struct {
	// Applicable: the loop's active target has lanes at all (a BB thread).
	Applicable bool
	// Running counts the lanes, within the stop's bounds, that are running or
	// in error with a retry queued and are not another loop's.
	Running int
	// Complete: every listing inside the bounds was read and nothing was
	// left out by them, so Running is the whole count and not a lower bound.
	Complete bool
	// Unreachable: bb could not be found from this process, configured or as
	// the latest check recorded it. Nothing was counted.
	Unreachable bool
}

// CountLanes walks the loop's lanes as a stop with --lanes would, with the
// same bounds and rules, and stops nothing, records nothing and sends
// nothing. It reads state.json and the session records, and runs bb's thread
// list and thread show. opts supplies NewAdapter and SessionDir; their zero
// values are the real ones.
func CountLanes(ctx context.Context, cfg *config.Config, opts Options) LaneCount {
	if opts.NewAdapter == nil {
		opts.NewAdapter = factory.New
	}
	t := &tick{ctx: ctx, cfg: cfg, loop: cfg.Loop, fb: cfg.FallbackSet, opts: opts, now: time.Now().UTC(), res: &Result{}}
	if opts.SessionDir == "" {
		// Read-only: the claim directory is used as it is, not repaired.
		if dir, _, err := session.Dir(); err == nil {
			t.sessionDir = dir
		}
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return LaneCount{}
	}
	t.st = st
	target, agent := t.resolve(st.Target())
	agent, reachable := stopAgent(agent, st)
	if !reachable {
		return LaneCount{Applicable: true, Unreachable: true}
	}
	a, err := opts.NewAdapter(agent, cfg.Loop.StateDir, target)
	if err != nil {
		return LaneCount{}
	}
	ls, ok := a.(adapter.LaneStopper)
	if !ok {
		return LaneCount{}
	}
	out := LaneCount{Applicable: true, Complete: true}
	root := ls.LaneRoot()
	level, seen, budget := []string{root}, map[string]bool{root: true}, decide.LaneStopMaxThreads
	for depth := 1; len(level) > 0; depth++ {
		if depth > decide.LaneStopMaxDepth || budget <= 0 {
			out.Complete = false // something may be below the bounds
			break
		}
		var next []string
		for _, p := range level {
			if ctx.Err() != nil {
				out.Complete = false
				return out
			}
			kids, notChecked, err := ls.LaneChildren(ctx, p, budget)
			if err != nil || notChecked > 0 {
				out.Complete = false
			}
			for _, k := range kids {
				if seen[k.ID] {
					continue
				}
				seen[k.ID] = true
				budget--
				n := decide.LaneNode{Verified: k.Verified, Status: k.Status, Queued: k.Queued}
				if k.Verified {
					n.OtherLoop, n.OwnershipUnknown = t.otherLoopLane(k.ID)
				}
				action := decide.LaneStopFor(n)
				if action == decide.LaneStopRunning || action == decide.LaneStopQueued {
					out.Running++
				}
				if decide.LaneStopDescend(action) {
					next = append(next, k.ID)
				}
			}
		}
		level = next
	}
	return out
}

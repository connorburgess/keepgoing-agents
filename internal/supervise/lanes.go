package supervise

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// laneRecoveryEvent is the report event for lanes given up or left alone
// because of what failed.
const laneRecoveryEvent = "child_recovery"

// laneRetryAction is the run-graph action of a lane retry.
const laneRetryAction = state.LaneRetryAction

const (
	// laneMargin: a lane is not started with less than this left in the
	// check, after what its mandatory steps have reserved. It is put off to
	// the next check, which takes it first.
	laneMargin = 30 * time.Second
	// laneTimeout bounds one lane's inspection and retry.
	laneTimeout = 40 * time.Second
	// laneMaxReports bounds the child_recovery reports of one episode.
	laneMaxReports = 3
	// laneDay is the window of a lane's per-day retry count.
	laneDay = 24 * time.Hour
	// laneMaxTracked bounds each record. It matches the adapter's limit on
	// children inspected in one listing.
	laneMaxTracked = 200
)

// laneWontAct are the reasons a lane was left alone that keepgoing will not
// get past on its own. A lane left for one of them is the session's to deal
// with, so it is announced like any other errored child.
var laneWontAct = map[string]bool{
	string(decide.LaneRateLimited): true, string(decide.LaneAuth): true, string(decide.LaneDeclined): true, string(decide.LaneUnverified): true,
	string(decide.LaneWaitingPerson): true, laneOtherLoop: true, laneOwnershipUnknown: true,
}

// A lane another loop supervises, or holds the session claim of, is that
// loop's to recover or stop; one whose ownership cannot be established is
// treated the same. Neither is ever retried here.
const (
	laneOtherLoop        = "other_loop"
	laneOwnershipUnknown = "ownership_unconfirmed"
)

// laneSendReserve is how many sends of loop.limits.max_sends lane retries
// never touch: a whole recovery of the supervised session (its re-prompts)
// and one notice. The session comes first.
func (t *tick) laneSendReserve() int64 {
	return int64(max(t.loop.Errors.MaxReprompts, 1) + 1)
}

// lanesPerDay bounds one lane's retries in 24 hours, across recoveries: a
// lane that works for a while after each retry and then fails again gets
// two rounds of attempts a day, not a round an hour.
func (t *tick) lanesPerDay() int {
	return 2 * max(t.loop.Errors.MaxReprompts, 1)
}

// recoverLanes is the only place the supervisor acts on a lane: a direct
// child conversation of the supervised session. It runs only when the active
// target has bb.children.retry_errored set, and all it ever does to a lane is
// ask the provider to retry that lane's failed turn by reference.
//
// It runs before the check's decision, on the path where the target was
// observed, so stop, pause and every standing hold have already won. d is
// what the engine decides before lane work; its only effect on the final
// decision is that a lane keepgoing is recovering is not announced to the
// session. decide.LanesAllowed keeps it out of a check that raises a hold,
// stops the session, moves the loop to another conversation, falls outside
// the window, or finds the session waiting on a person, stopping or
// unreadable. Control is read again before each lane, so a stop or pause
// asked for during the check ends lane work at once. It does nothing in a dry
// run or for a target that is gone, and it waits while the network is down.
//
// A lane is tried only if an earlier check saw it working, it has attempts
// left with their spacing passed, and a fresh inspection establishes every
// fact decide.LaneVerdictFor needs. Anything unknown leaves it alone.
func (t *tick) recoverLanes(a adapter.Adapter, agent config.Agent, obs decide.Observation, d decide.Decision, inWindow bool) {
	if t.opts.DryRun {
		return
	}
	lr, canRecover := a.(adapter.LaneRecoverer)
	if !agent.BB.RetryErroredLanes() {
		// Switched off: nothing is remembered, here or by the adapter.
		if t.st.Lanes != nil {
			if canRecover {
				lr.ForgetLanes()
			}
			t.st.Lanes = nil
		}
		return
	}
	t.laneRecoveryOn = true
	if !canRecover || obs.Gone {
		return
	}
	key, err := state.LaneKey(t.loop.StateDir)
	if err != nil {
		t.warn("lane recovery: the lane key cannot be read or made (%v); no lane was looked at", err)
		return
	}
	rec := copyLanes(t.st.Lanes)
	t.st.Lanes = rec
	hash := func(id string) string { return state.LaneHash(key, id) }
	set := func(ids []string) map[string]bool {
		out := map[string]bool{}
		for _, id := range ids {
			out[hash(id)] = true
		}
		return out
	}
	complete := !obs.ChildrenUnverified
	active, errored, healthy := set(obs.ActiveChildIDs), set(obs.ErroredChildren), set(obs.HealthyChildren)
	allowed, retried := false, 0
	defer func() {
		// The lanes keepgoing is recovering or will recover: seen working, not
		// given up, and not left alone for a reason it cannot get past. They
		// are withheld from the notice to the session.
		// The set is held for this check only, under the names the notice
		// record uses.
		t.lanesInRecovery = map[string]bool{}
		for _, id := range obs.ErroredChildren {
			h := hash(id)
			r := rec.Retries[h]
			_, seen := rec.SeenActive[h]
			if rec.Blocked == "" && (seen || r.Attempts > 0) && !r.GivenUp && !laneWontAct[r.Outcome] {
				t.lanesInRecovery[decide.ChildHashes([]string{id})[0]] = true
			}
		}
		t.lanesRetried = len(t.lanesInRecovery)
		if allowed {
			t.reportLanes(rec, retried, errored)
		}
		endLaneEpisode(rec) // after this check's give-ups and their report
	}()

	// Seen working: remembered until a complete listing no longer shows the lane.
	for h := range active {
		rec.SeenActive[h] = t.now
	}
	for h := range rec.SeenActive {
		if complete && !active[h] && !errored[h] && !healthy[h] {
			delete(rec.SeenActive, h)
		}
	}
	trimOldest(rec.SeenActive, laneMaxTracked)

	// A lane's attempts are forgotten once it has stayed out of error for
	// min_wake_interval, the rule the child-error report uses, so a lane that
	// fails again straight after a retry spends the same attempts. Its count
	// for the day outlasts that, so a lane that fails again an hour after
	// each retry is bounded too.
	for h, r := range rec.Retries {
		if !r.DayStart.IsZero() && t.now.Sub(r.DayStart) >= laneDay {
			r.DayStart, r.DayRetries = time.Time{}, 0
		}
		switch {
		case errored[h]:
			r.HealthySince = time.Time{}
		case healthy[h] || complete:
			if r.HealthySince.IsZero() {
				r.HealthySince = t.now
			}
			if t.now.Sub(r.HealthySince) >= t.loop.Progress.MinWakeInterval.Duration {
				if r.DayRetries == 0 {
					delete(rec.Retries, h)
					continue
				}
				// A new failure gets new attempts, unless the lane has used
				// its retries for the day: then it stays given up.
				spent := r.DayRetries >= t.lanesPerDay()
				r = state.LaneRetry{HealthySince: r.HealthySince, DayStart: r.DayStart, DayRetries: r.DayRetries, GivenUp: spent && r.GivenUp, Reported: spent && r.Reported}
			}
		}
		rec.Retries[h] = r
	}
	trimLanes(rec.Retries, errored, laneMaxTracked)
	endLaneEpisode(rec)
	if !decide.LanesAllowed(d.Action, obs.State, inWindow, obs.AwaitingInput) {
		rec.Deferred = nil
		return
	}
	allowed = true
	if len(obs.ErroredChildren) == 0 {
		rec.Deferred = nil
		return
	}

	ids := map[string]string{}
	deferred := map[string]bool{}
	for _, h := range rec.Deferred {
		deferred[h] = true
	}
	var lanes []decide.Lane
	for _, id := range obs.ErroredChildren {
		h := hash(id)
		if _, dup := ids[h]; dup {
			continue
		}
		ids[h] = id
		_, seen := rec.SeenActive[h]
		r := rec.Retries[h]
		lanes = append(lanes, decide.Lane{Hash: h, SeenWorking: seen, Attempts: r.Attempts, LastAt: r.LastAt, LookedAt: r.LookedAt, GivenUp: r.GivenUp,
			NoOps: r.NoOps, DayRetries: r.DayRetries, Deferred: deferred[h]})
	}
	t.lanesPerEpisode = agent.BB.LanesPerEpisode()
	plan := decide.PlanLanes(lanes, t.now, decide.LaneLimits{
		MaxPerCheck: agent.BB.LanesPerCheck(), MaxAttempts: t.loop.Errors.MaxReprompts, Backoff: t.loop.Errors.Backoff.Duration,
		EpisodeLeft: t.lanesPerEpisode - rec.EpisodeRetries, MaxNoOps: turnRetryMaxNoOps, MaxPerDay: t.lanesPerDay(),
	})
	check := &state.LaneCheck{At: t.now, NotRetried: map[string]int{}}
	for _, h := range plan.GiveUp {
		r := rec.Retries[h]
		r.GivenUp = true
		rec.Retries[h] = r
		check.GivenUp++
	}
	rec.Deferred = nil
	switch {
	case len(plan.Try) == 0:
	case t.networkDown():
		// Nothing is asked of BB and nothing is counted into a dead network.
		check.Waiting = "network"
	default:
	lanes:
		for i, h := range plan.Try {
			switch {
			case t.left() < laneMargin:
				rec.Deferred = append([]string{}, plan.Try[i:]...)
				check.Deferred = len(rec.Deferred)
				t.warn("%d lane(s) put off to the next check: too little time is left in this one", check.Deferred)
				break lanes
			case !t.lanesStillAllowed():
				// Stop, pause or a hold was asked for while this check ran.
				check.Waiting = "control"
				break lanes
			case !t.lanesMaySend():
				// The supervised session's own recovery comes first.
				rec.Blocked = "send_allowance"
				check.NotRetried["send_allowance"]++
				t.warn("lane retries stopped: the last %d sends of loop.limits.max_sends are kept for the supervised session", t.laneSendReserve())
				break lanes
			}
			if rec.Blocked == "send_allowance" {
				rec.Blocked = ""
			}
			if !t.tryLane(lr, rec, check, h, ids[h]) {
				break
			}
		}
	}
	retried = check.Retried
	if len(check.NotRetried) == 0 {
		check.NotRetried = nil
	}
	if check.Retried == 0 && check.GivenUp == 0 && check.Deferred == 0 && check.NotRetried == nil && check.Waiting == "" {
		return
	}
	rec.Last, t.res.Lanes = check, check
	t.incidentLanes.Retried, t.incidentLanes.GivenUp, t.incidentLanes.Deferred = check.Retried, check.GivenUp, check.Deferred
}

// endLaneEpisode ends the episode when no lane is still being recovered. An
// episode is one failure being recovered: it is over once each of its lanes
// has been forgotten or given up, so a lane that stays given up and in error
// does not hold it open, and the next failure has the whole allowance of
// retries and reports.
func endLaneEpisode(rec *state.LaneRecovery) {
	for _, r := range rec.Retries {
		if r.Attempts > 0 && !r.GivenUp {
			return
		}
	}
	rec.EpisodeRetries, rec.Reports = 0, 0
}

// lanesStillAllowed reads the control file again. Stop and pause win on the
// next check everywhere else; between two lanes they win at once.
func (t *tick) lanesStillAllowed() bool {
	ctl, err := control.Load(t.loop.StateDir)
	if err != nil {
		return false
	}
	g := ctl.Gate()
	return g.Enabled && !g.Stop && !g.Paused && !g.AwaitingUser && !g.Completed
}

// lanesMaySend reports whether a lane retry would leave the supervised
// session the sends its own recovery needs. Without a limit it always does.
func (t *tick) lanesMaySend() bool {
	limit := t.loop.Limits.MaxSends
	if limit <= 0 {
		return true
	}
	used, err := state.LoadSends(t.loop.StateDir)
	return err == nil && limit-used > t.laneSendReserve()
}

// tryLane inspects one lane and, if every fact allows it, asks the provider
// to retry its failed turn. It reports false when lane work must stop for
// this check.
func (t *tick) tryLane(lr adapter.LaneRecoverer, rec *state.LaneRecovery, check *state.LaneCheck, hash, id string) bool {
	ctx, cancel := context.WithTimeout(t.ctx, min(laneTimeout, t.left()))
	defer cancel()
	r := rec.Retries[hash]
	defer func() { rec.Retries[hash] = r }()
	// Another loop's lane is that loop's: retrying it could undo that loop's
	// stop or pause. The same check a stop of the lanes uses.
	switch other, unknown := t.otherLoopLane(id); {
	case unknown:
		r.Outcome, r.LookedAt = laneOwnershipUnknown, t.now
		check.NotRetried[laneOwnershipUnknown]++
		return true
	case other:
		r.Outcome, r.LookedAt = laneOtherLoop, t.now
		check.NotRetried[laneOtherLoop]++
		return true
	}
	verdict := decide.LaneVerdictFor(lr.InspectLane(ctx, id))
	if verdict != decide.LaneRetry {
		r.Outcome, r.LookedAt = string(verdict), t.now
		check.NotRetried[string(verdict)]++
		return true
	}
	if !t.lanesStillAllowed() {
		// A stop, pause or hold saved while the lane was being looked at.
		check.Waiting = "control"
		return false
	}
	// Recorded before it is made, under the lane's keyed hash: a check that
	// cannot save what happened leaves the record, and the next check counts
	// the retry as made (countUnrecordedSends), so it is spaced and capped,
	// never repeated blindly.
	seq, err := t.reserveSend(laneRetryAction, t.res.Target, hash)
	if err != nil {
		// The allowance is spent or the record cannot be saved. Lanes raise
		// no hold of their own; the session's next send does.
		rec.Blocked = "send_allowance"
		check.NotRetried["send_allowance"]++
		t.warn("lane retries stopped: %v", err)
		return false
	}
	// Recorded like every other send, against the target whose lane it is.
	// The lane itself is not named.
	attempt := t.graphBegin(t.res.Target, laneRetryAction, turnRetryMode, prompt.Message{})
	res, err := lr.RetryLane(ctx, id)
	var unsupported adapter.ErrUnsupported
	refund := func() {
		if err := state.RefundSend(t.loop.StateDir, t.loop.Limits.MaxSends, seq); err != nil {
			t.warn("lane retry: %v", err)
		}
	}
	counted := func(outcome string) {
		if r.DayStart.IsZero() {
			r.DayStart = t.now
		}
		r.Attempts, r.DayRetries, r.LastAt, r.Outcome, r.NoOps = r.Attempts+1, r.DayRetries+1, t.now, outcome, 0
		rec.EpisodeRetries++
	}
	switch {
	case errors.As(err, &unsupported):
		// This provider version cannot retry a turn. Nothing else is tried,
		// and while that lasts every lane is the session's to deal with.
		refund()
		t.graphDiscard(attempt)
		r.Outcome, r.LookedAt, rec.Blocked = turnRetryUnsupported, t.now, turnRetryUnsupported
		check.NotRetried[turnRetryUnsupported]++
		t.warn("lane retries are not supported by this provider version")
		return false
	case err != nil:
		// Refused, unreadable or cut off at the time limit: BB may have acted
		// on it, so it is counted as an attempt.
		t.graphFinish(attempt, "adapter_error")
		counted(turnRetryRefused)
		check.NotRetried[turnRetryRefused]++
	case res.Outcome == adapter.TurnRetryNothing || res.Outcome == adapter.TurnRetryAlreadyQueued:
		// BB did nothing for this request, and said so. A lane is never
		// prompted instead. Three such answers in a row and the lane is
		// given up, as for the supervised session's own turn.
		refund()
		t.graphDiscard(attempt)
		r.Outcome, r.LookedAt, r.NoOps = string(res.Outcome), t.now, r.NoOps+1
		check.NotRetried[string(res.Outcome)]++
	default:
		t.graphFinish(attempt, "adapter_returned")
		counted(string(res.Outcome))
		rec.Blocked = ""
		check.Retried++
	}
	return true
}

// reportLanes tells the owner, once per lane, that lanes were given up or
// were left alone because their failure is a rate limit, an auth error, or a
// refusal or restriction by the provider.
// Counts only. The record is saved before the single attempt, like the other
// reports that say a person may be needed.
func (t *tick) reportLanes(rec *state.LaneRecovery, retried int, errored map[string]bool) {
	var fresh []string
	givenUp, rateLimited, auth, declined := 0, 0, 0, 0
	for h, r := range rec.Retries {
		if !errored[h] {
			continue
		}
		class := r.Outcome == string(decide.LaneRateLimited) || r.Outcome == string(decide.LaneAuth) || r.Outcome == string(decide.LaneDeclined)
		if !r.GivenUp && !class {
			continue
		}
		switch {
		case r.GivenUp:
			givenUp++
		case r.Outcome == string(decide.LaneRateLimited):
			rateLimited++
		case r.Outcome == string(decide.LaneDeclined):
			declined++
		default:
			auth++
		}
		if !r.Reported {
			fresh = append(fresh, h)
		}
	}
	if len(fresh) == 0 {
		return
	}
	mark := func() {
		for _, h := range fresh {
			r := rec.Retries[h]
			r.Reported = true
			rec.Retries[h] = r
		}
	}
	switch {
	case !t.loop.Report.ReportsEvent(laneRecoveryEvent):
		return // not covered, so subscribing later sends it
	case rec.Reports >= laneMaxReports:
		mark() // covered without a report, so later checks stay quiet too
		return
	case !t.canReport(laneRecoveryEvent):
		t.warn("report %s put off to the next check: the network is down or no time is left in this one", laneRecoveryEvent)
		return
	}
	mark()
	rec.Reports++
	saved, err := state.Load(t.loop.StateDir)
	if err == nil {
		saved.Lanes = rec
		err = state.Save(t.loop.StateDir, saved)
	}
	if err != nil {
		t.warn("report %s: not sent because the lane record could not be saved", laneRecoveryEvent)
		return
	}
	text := fmt.Sprintf("%s: lane recovery. %d direct child thread(s) of the supervised thread were given up (a lane gets at most %d retries, and one failure at most %d across all lanes); %d were not retried because they are rate limited, %d because they need a login and %d because the provider refused or restricted them. keepgoing asked BB to retry %d on this check. Lane recovery has not messaged, stopped or created any child thread; whether that work is retried or replaced is up to the supervised session or a person.",
		t.loop.Name, givenUp, t.loop.Errors.MaxReprompts, t.lanesPerEpisode, rateLimited, auth, declined, retried)
	ctx, cancel, ok := t.reportContext(laneRecoveryEvent, text)
	if !ok {
		return
	}
	defer cancel()
	err = t.opts.Reporter.Report(ctx, report.Event{Type: laneRecoveryEvent, Loop: t.loop.Name, Text: text, At: t.now,
		Data: map[string]any{"givenUp": givenUp, "rateLimited": rateLimited, "authFailed": auth, "declined": declined, "retried": retried, "report": rec.Reports}})
	if err != nil {
		t.warn("report %s: %v", laneRecoveryEvent, err) // one attempt, not retried
	}
}

// lanesNotInRecovery drops the lanes keepgoing is recovering from a list of
// lane hashes.
func (t *tick) lanesNotInRecovery(hashes []string) []string {
	if len(t.lanesInRecovery) == 0 {
		return hashes
	}
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if !t.lanesInRecovery[h] {
			out = append(out, h)
		}
	}
	return out
}

func copyLanes(l *state.LaneRecovery) *state.LaneRecovery {
	out := &state.LaneRecovery{SeenActive: map[string]time.Time{}, Retries: map[string]state.LaneRetry{}}
	if l == nil {
		return out
	}
	out.Blocked, out.EpisodeRetries, out.Reports, out.Last = l.Blocked, l.EpisodeRetries, l.Reports, l.Last
	out.Deferred = append([]string{}, l.Deferred...)
	for h, at := range l.SeenActive {
		out.SeenActive[h] = at
	}
	for h, r := range l.Retries {
		out.Retries[h] = r
	}
	return out
}

// trimLanes bounds the lane records. Lanes no longer in error go first, then
// the ones longest untouched.
func trimLanes(m map[string]state.LaneRetry, errored map[string]bool, limit int) {
	if len(m) <= limit {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	last := func(r state.LaneRetry) time.Time {
		if r.LookedAt.After(r.LastAt) {
			return r.LookedAt
		}
		return r.LastAt
	}
	sort.Slice(keys, func(i, j int) bool {
		if errored[keys[i]] != errored[keys[j]] {
			return errored[keys[i]]
		}
		return last(m[keys[i]]).After(last(m[keys[j]]))
	})
	for _, k := range keys[limit:] {
		delete(m, k)
	}
}

// trimOldest keeps the most recent max entries.
func trimOldest(m map[string]time.Time, max int) {
	if len(m) <= max {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]].After(m[keys[j]]) })
	for _, k := range keys[max:] {
		delete(m, k)
	}
}

package supervise

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// laneRecoveryEvent is the report event for lanes given up or left alone
// because of what failed.
const laneRecoveryEvent = "child_recovery"

// laneRetryAction is the run-graph action of a lane retry.
const laneRetryAction = "child_retry"

const (
	// laneMargin: a lane is not started with less than this left in the
	// check, after what its mandatory steps have reserved. It is put off to
	// the next check, which takes it first.
	laneMargin = 30 * time.Second
	// laneTimeout bounds one lane's inspection and retry.
	laneTimeout = 40 * time.Second
	// laneMaxReports bounds the child_recovery reports of one episode.
	laneMaxReports = 3
	// laneMaxTracked bounds each record. It matches the adapter's limit on
	// children inspected in one listing.
	laneMaxTracked = 200
)

// recoverLanes is the only place the supervisor acts on a lane: a direct
// child conversation of the supervised session. It runs only when the active
// target has bb.children.retry_errored set, and all it ever does to a lane is
// ask the provider to retry that lane's failed turn by reference.
//
// It runs before the check's decision, on the path where the target was
// observed, so stop, pause and every standing hold have already won. d is
// what the engine decides before lane work; its only effect on the final
// decision is that a lane being recovered is not announced to the session.
// Stop, pause and holds are never affected, and
// decide.LanesAllowed keeps it out of a check that raises a hold, stops the
// session, moves the loop to another conversation, falls outside the window
// or finds the session waiting on a person. It does nothing in a dry run or for a target that is gone. It waits
// while the network is down.
//
// A lane is tried only if an earlier check saw it working, it has attempts
// left with their spacing passed, and a fresh inspection establishes every
// fact decide.LaneVerdictFor needs. Anything unknown leaves it alone.
func (t *tick) recoverLanes(a adapter.Adapter, agent config.Agent, obs decide.Observation, d decide.Decision, inWindow bool) {
	if t.opts.DryRun {
		return
	}
	if !agent.BB.RetryErroredLanes() {
		t.st.Lanes = nil // switched off: nothing is remembered
		return
	}
	t.laneRecoveryOn = true
	lr, ok := a.(adapter.LaneRecoverer)
	if !ok || obs.Gone {
		return
	}
	rec := copyLanes(t.st.Lanes)
	t.st.Lanes = rec
	complete := !obs.ChildrenUnverified
	active, errored, healthy := hashSet(obs.ActiveChildIDs), hashSet(obs.ErroredChildren), hashSet(obs.HealthyChildren)
	defer func() {
		t.lanesInRecovery = map[string]bool{}
		for h := range errored {
			if r, ok := rec.Retries[h]; ok && r.Attempts > 0 && !r.GivenUp && !r.LookedAt.After(r.LastAt) {
				t.lanesInRecovery[h] = true
			}
		}
		t.lanesRetried = len(t.lanesInRecovery)
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

	// A lane's record is dropped once it has stayed out of error for
	// min_wake_interval, the rule the child-error report uses, so a lane that
	// fails again straight after a retry spends the same attempts.
	for h, r := range rec.Retries {
		switch {
		case errored[h]:
			r.HealthySince = time.Time{}
		case healthy[h] || complete:
			if r.HealthySince.IsZero() {
				r.HealthySince = t.now
			}
			if t.now.Sub(r.HealthySince) >= t.loop.Progress.MinWakeInterval.Duration {
				delete(rec.Retries, h)
				continue
			}
		}
		rec.Retries[h] = r
	}
	if len(rec.Retries) == 0 {
		rec.EpisodeRetries, rec.Reports = 0, 0
	}
	// A check that raises a hold, stops the session or moves the loop leaves
	// every lane alone, and so does a check outside the loop's window.
	if !decide.LanesAllowed(d.Action, inWindow, obs.AwaitingInput) || len(obs.ErroredChildren) == 0 {
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
		h := decide.ChildHashes([]string{id})[0]
		if _, dup := ids[h]; dup {
			continue
		}
		ids[h] = id
		_, seen := rec.SeenActive[h]
		r := rec.Retries[h]
		lanes = append(lanes, decide.Lane{Hash: h, SeenWorking: seen, Attempts: r.Attempts, LastAt: r.LastAt, LookedAt: r.LookedAt, GivenUp: r.GivenUp, Deferred: deferred[h]})
	}
	plan := decide.PlanLanes(lanes, t.now, decide.LaneLimits{
		MaxPerCheck: agent.BB.LanesPerCheck(), MaxAttempts: t.loop.Errors.MaxReprompts, Backoff: t.loop.Errors.Backoff.Duration,
		EpisodeLeft: agent.BB.LanesPerEpisode() - rec.EpisodeRetries,
	})
	t.lanesPerEpisode = agent.BB.LanesPerEpisode()
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
		for i, h := range plan.Try {
			if t.left() < laneMargin {
				rec.Deferred = append([]string{}, plan.Try[i:]...)
				check.Deferred = len(rec.Deferred)
				t.warn("%d lane(s) put off to the next check: too little time is left in this one", check.Deferred)
				break
			}
			if !t.tryLane(lr, rec, check, h, ids[h]) {
				break
			}
		}
	}
	if len(check.NotRetried) == 0 {
		check.NotRetried = nil
	}
	if check.Retried == 0 && check.GivenUp == 0 && check.Deferred == 0 && check.NotRetried == nil && check.Waiting == "" {
		return
	}
	rec.Last, t.res.Lanes = check, check
	t.incidentLanes.Retried, t.incidentLanes.GivenUp, t.incidentLanes.Deferred = check.Retried, check.GivenUp, check.Deferred
	t.reportLanes(rec, check, errored)
}

// tryLane inspects one lane and, if every fact allows it, asks the provider
// to retry its failed turn. It reports false when lane work must stop for
// this check.
func (t *tick) tryLane(lr adapter.LaneRecoverer, rec *state.LaneRecovery, check *state.LaneCheck, hash, id string) bool {
	ctx, cancel := context.WithTimeout(t.ctx, min(laneTimeout, t.left()))
	defer cancel()
	r := rec.Retries[hash]
	defer func() { rec.Retries[hash] = r }()
	verdict := decide.LaneVerdictFor(lr.InspectLane(ctx, id))
	if verdict != decide.LaneRetry {
		r.Outcome, r.LookedAt = string(verdict), t.now
		check.NotRetried[string(verdict)]++
		return true
	}
	if err := state.ReserveSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
		// The allowance is spent or unreadable. Lanes raise no hold of their
		// own; the session's next send does.
		r.Outcome = "send_allowance"
		check.NotRetried["send_allowance"]++
		t.warn("lane retries stopped: %v", err)
		return false
	}
	// Recorded like every other send, against the target whose lane it is.
	// The lane itself is not named.
	attempt := t.graphBegin(t.res.Target, laneRetryAction, turnRetryMode, prompt.Message{})
	res, err := lr.RetryLane(ctx, id)
	graphOutcome := "adapter_returned"
	if err != nil {
		graphOutcome = "adapter_error"
	}
	t.graphFinish(attempt, graphOutcome)
	var unsupported adapter.ErrUnsupported
	refund := func() {
		if err := state.RefundSend(t.loop.StateDir, t.loop.Limits.MaxSends); err != nil {
			t.warn("lane retry: %v", err)
		}
	}
	switch {
	case errors.As(err, &unsupported):
		// This provider version cannot retry a turn. Nothing else is tried.
		refund()
		r.Outcome = turnRetryUnsupported
		check.NotRetried[turnRetryUnsupported]++
		t.warn("lane retries are not supported by this provider version")
		return false
	case err != nil:
		// Refused or unreadable: it may have been acted on, so it is counted.
		r.Attempts, r.LastAt, r.Outcome = r.Attempts+1, t.now, turnRetryRefused
		rec.EpisodeRetries++
		check.NotRetried[turnRetryRefused]++
	case res.Outcome == adapter.TurnRetryNothing || res.Outcome == adapter.TurnRetryAlreadyQueued:
		// BB did nothing for this request. A lane is never prompted instead.
		refund()
		r.Outcome, r.LookedAt = string(res.Outcome), t.now
		check.NotRetried[string(res.Outcome)]++
	default:
		r.Attempts, r.LastAt, r.Outcome = r.Attempts+1, t.now, string(res.Outcome)
		rec.EpisodeRetries++
		check.Retried++
	}
	return true
}

// reportLanes tells the owner, once per lane, that lanes were given up or
// were left alone because their failure is a rate limit or an auth error.
// Counts only. The record is saved before the single attempt, like the other
// reports that say a person may be needed.
func (t *tick) reportLanes(rec *state.LaneRecovery, check *state.LaneCheck, errored map[string]bool) {
	var fresh []string
	givenUp, rateLimited, auth := 0, 0, 0
	for h, r := range rec.Retries {
		if !errored[h] {
			continue
		}
		class := r.Outcome == string(decide.LaneRateLimited) || r.Outcome == string(decide.LaneAuth)
		if !r.GivenUp && !class {
			continue
		}
		switch {
		case r.GivenUp:
			givenUp++
		case r.Outcome == string(decide.LaneRateLimited):
			rateLimited++
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
	text := fmt.Sprintf("%s: lane recovery. %d direct child thread(s) of the supervised thread were given up (a lane gets at most %d retries, and one failure at most %d across all lanes); %d were not retried because they are rate limited and %d because they need a login. keepgoing asked BB to retry %d on this check. It has not messaged, stopped or created any child thread; whether that work is retried or replaced is up to the supervised session or a person.",
		t.loop.Name, givenUp, t.loop.Errors.MaxReprompts, t.lanesPerEpisode, rateLimited, auth, check.Retried)
	ctx, cancel, ok := t.reportContext(laneRecoveryEvent, text)
	if !ok {
		return
	}
	defer cancel()
	err = t.opts.Reporter.Report(ctx, report.Event{Type: laneRecoveryEvent, Loop: t.loop.Name, Text: text, At: t.now,
		Data: map[string]any{"givenUp": givenUp, "rateLimited": rateLimited, "authFailed": auth, "retried": check.Retried, "report": rec.Reports}})
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
	out.EpisodeRetries, out.Reports, out.Last = l.EpisodeRetries, l.Reports, l.Last
	out.Deferred = append([]string{}, l.Deferred...)
	for h, at := range l.SeenActive {
		out.SeenActive[h] = at
	}
	for h, r := range l.Retries {
		out.Retries[h] = r
	}
	return out
}

func hashSet(ids []string) map[string]bool {
	set := map[string]bool{}
	for _, h := range decide.ChildHashes(ids) {
		set[h] = true
	}
	return set
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

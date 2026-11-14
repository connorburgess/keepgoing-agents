package supervise

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// laneAdapter is a fake provider whose lanes can be inspected and retried.
// Every call on a lane is recorded, so a test can assert that nothing else
// was ever done to one.
type laneAdapter struct {
	*fakeAdapter
	facts     map[string]decide.LaneFacts // per lane; absent means a retryable network failure
	answer    func(id string) (adapter.TurnRetry, error)
	inspected []string
	retried   []string
	onInspect func()
	forgotten int
}

var retryable = decide.LaneFacts{Verified: true, Errored: true, InputKnown: true, ClassKnown: true, Class: decide.ErrNetwork}

func (l *laneAdapter) InspectLane(_ context.Context, id string) decide.LaneFacts {
	l.inspected = append(l.inspected, id)
	if l.onInspect != nil {
		l.onInspect()
	}
	if f, ok := l.facts[id]; ok {
		return f
	}
	return retryable
}

func (l *laneAdapter) ForgetLanes() { l.forgotten++ }

func (l *laneAdapter) RetryLane(_ context.Context, id string) (adapter.TurnRetry, error) {
	l.retried = append(l.retried, id)
	if l.answer != nil {
		return l.answer(id)
	}
	return adapter.TurnRetry{Outcome: adapter.TurnRetried, Attempt: 2}, nil
}

type laneWorld struct {
	*world
	a      *laneAdapter
	online func(context.Context) bool
	clock  func() time.Time
	dryRun bool
}

func laneIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("thr_lane_%02d", i)
	}
	return ids
}

// newLaneWorld is a loop with lane recovery on, max 3 attempts per lane ten
// minutes apart, whose session is working.
func newLaneWorld(t *testing.T) *laneWorld {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Errors.MaxReprompts = 3
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	w.cfg.Loop.Agent.BB.Children = &config.BBChildren{RetryErrored: true}
	w.cfg.Loop.Limits.MaxSends = 1000 // sends are counted only under a limit
	f := w.primary(decide.Observation{State: decide.Active})
	return &laneWorld{world: w, a: &laneAdapter{fakeAdapter: f, facts: map[string]decide.LaneFacts{}}}
}

func (w *laneWorld) tick(t *testing.T, at time.Time) Result {
	t.Helper()
	opts := w.opts(at)
	inner := opts.NewAdapter
	opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
		if target == state.Primary {
			return w.a, nil
		}
		return inner(a, dir, target)
	}
	opts.Online, opts.DryRun = w.online, w.dryRun
	if w.clock != nil {
		opts.Clock = w.clock
	}
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return res
}

// working shows the lanes active on one check; failed puts them in error.
func (w *laneWorld) working(t *testing.T, at time.Time, ids ...string) {
	t.Helper()
	w.a.obs.ActiveChildren, w.a.obs.ActiveChildIDs, w.a.obs.ErroredChildren = len(ids), ids, nil
	w.tick(t, at)
}

func (w *laneWorld) failed(ids ...string) {
	w.a.obs.ActiveChildren, w.a.obs.ActiveChildIDs, w.a.obs.ErroredChildren = 0, nil, ids
}

func (w *laneWorld) lanes(t *testing.T) *state.LaneRecovery {
	t.Helper()
	return savedState(t, w.dir).Lanes
}

// laneReports are the child_recovery reports sent so far. The world also
// receives the child_error report that says children are in error at all.
func (w *laneWorld) laneReports() []report.Event {
	var out []report.Event
	for _, e := range w.reports {
		if e.Type == laneRecoveryEvent {
			out = append(out, e)
		}
	}
	return out
}

// Off by default: with children in error after an outage nothing is
// inspected, retried or remembered. Switching the option off again forgets.
func TestLaneRecoveryIsOffUnlessConfigured(t *testing.T) {
	for _, children := range []*config.BBChildren{nil, {}, {MaxPerCheck: 5, MaxPerEpisode: 20}} {
		w := newLaneWorld(t)
		w.cfg.Loop.Agent.BB.Children = children
		ids := laneIDs(3)
		w.working(t, t0, ids...)
		w.failed(ids...)
		res := w.tick(t, t0.Add(time.Minute))
		if len(w.a.inspected)+len(w.a.retried) != 0 || res.Lanes != nil || w.lanes(t) != nil || res.ErroredChildren != 3 {
			t.Fatalf("%+v: %v %v %+v", children, w.a.inspected, w.a.retried, res.Lanes)
		}
		sendsUsed(t, w.world, 0)
		// A loop that never opts in writes nothing an older build cannot read.
		if g, _ := rungraph.Load(w.dir); len(g.Attempts) != 0 || w.a.forgotten != 0 {
			t.Fatalf("%+v: graph attempts %+v, forgotten %d", children, g.Attempts, w.a.forgotten)
		}
		if kept := everythingKept(t, w.world); strings.Contains(kept, "child_retry") {
			t.Fatalf("%+v: lane work was recorded with the option off", children)
		}
	}
	w := newLaneWorld(t)
	ids := laneIDs(2)
	w.working(t, t0, ids...)
	w.failed(ids...)
	if res := w.tick(t, t0.Add(time.Minute)); res.Lanes == nil || res.Lanes.Retried != 2 || w.lanes(t) == nil {
		t.Fatalf("%+v", res.Lanes)
	}
	w.cfg.Loop.Agent.BB.Children.RetryErrored = false
	if res := w.tick(t, t0.Add(20*time.Minute)); res.Lanes != nil || w.lanes(t) != nil || len(w.a.retried) != 2 || w.a.forgotten != 1 {
		t.Fatalf("switched off: %+v %+v forgotten=%d", res.Lanes, w.lanes(t), w.a.forgotten)
	}
	w.tick(t, t0.Add(21*time.Minute))
	if w.a.forgotten != 1 {
		t.Fatal("the adapter was told to forget on every check")
	}
}

// A lane is retried only if an earlier check, with the option on, saw it
// working. A lane already in error when the loop first sees it is left alone,
// including when the option is switched on in the middle of an outage.
func TestLaneMustHaveBeenSeenWorkingFirst(t *testing.T) {
	w := newLaneWorld(t)
	w.cfg.Loop.Agent.BB.Children = nil
	ids := laneIDs(3)
	w.working(t, t0, ids...) // seen working, but with the option off: not remembered
	w.failed(ids...)
	w.tick(t, t0.Add(time.Minute))
	w.cfg.Loop.Agent.BB.Children = &config.BBChildren{RetryErrored: true} // switched on mid-outage
	for i := 2; i < 30; i += 11 {
		if res := w.tick(t, t0.Add(time.Duration(i)*time.Minute)); res.Lanes != nil || len(w.a.inspected) != 0 {
			t.Fatalf("a lane never seen working was touched: %+v %v", res.Lanes, w.a.inspected)
		}
	}
	// One of them is retried by its owner and seen working, then fails again.
	w.working(t, t0.Add(40*time.Minute), ids[1])
	w.failed(ids...)
	res := w.tick(t, t0.Add(41*time.Minute))
	if res.Lanes == nil || res.Lanes.Retried != 1 || strings.Join(w.a.retried, ",") != ids[1] {
		t.Fatalf("%+v %v", res.Lanes, w.a.retried)
	}
	// A lane that leaves the listing is forgotten; coming back in error it is new.
	w.a.obs.ErroredChildren = ids[:1]
	w.tick(t, t0.Add(42*time.Minute))
	if _, kept := w.lanes(t).SeenActive[laneName(t, w.world, ids[1])]; kept {
		t.Fatal("a lane no longer listed is still remembered as seen working")
	}
}

// Six lanes fail together. Five are asked per check; each lane gets three
// attempts ten minutes apart; then it is given up and reported once, by count.
func TestLaneRetriesAreBoundedPerCheckPerLaneAndReportedOnce(t *testing.T) {
	w := newLaneWorld(t)
	ids := laneIDs(6)
	w.working(t, t0, ids...)
	w.failed(ids...)
	m := time.Minute
	for _, step := range []struct {
		at             time.Duration
		retried, total int
		givenUp        int
	}{
		{1 * m, 5, 5, 0},
		{2 * m, 1, 6, 0},  // the sixth; the first five are inside their spacing
		{3 * m, 0, 6, 0},  // all inside their spacing
		{10 * m, 0, 6, 0}, // nine minutes: still inside
		{11 * m, 5, 11, 0},
		{12 * m, 1, 12, 0},
		{21 * m, 5, 17, 0},
		{22 * m, 1, 18, 0},
		{31 * m, 0, 18, 5}, // three attempts each, the spacing has passed: given up
		{32 * m, 0, 18, 1},
		{45 * m, 0, 18, 0},
		{24 * 60 * m, 0, 18, 0},
	} {
		res := w.tick(t, t0.Add(step.at))
		got := state.LaneCheck{}
		if res.Lanes != nil {
			got = *res.Lanes
		}
		if got.Retried != step.retried || got.GivenUp != step.givenUp || len(w.a.retried) != step.total {
			t.Fatalf("at %s: %+v, %d retries in all", step.at, got, len(w.a.retried))
		}
		if res.Action != "none" || res.State != "active" {
			t.Fatalf("at %s: lane work changed the decision: %+v", step.at, res)
		}
	}
	perLane := map[string]int{}
	for _, id := range w.a.retried {
		perLane[id]++
	}
	for _, id := range ids {
		if perLane[id] != 3 {
			t.Fatalf("%s was retried %d times", id, perLane[id])
		}
	}
	// One report when the first lanes are given up, one for the rest, no more.
	reports := w.laneReports()
	if len(reports) != 2 {
		t.Fatal(w.reportTypes())
	}
	e := reports[1]
	if e.Data["givenUp"] != 6 || e.Data["rateLimited"] != 0 || e.Data["authFailed"] != 0 || !strings.Contains(e.Text, "6 direct child thread(s) of the supervised thread were given up (a lane gets at most 3 retries, and one failure at most 20 across all lanes)") || !strings.Contains(e.Text, "has not messaged, stopped or created any child thread") {
		t.Fatalf("%+v", e)
	}
	// Each retry spent one send from the loop's allowance; nothing was sent or stopped.
	sendsUsed(t, w.world, 18)
	if len(w.a.sent) != 0 || w.a.stopped != 0 {
		t.Fatalf("sent=%v stopped=%d", w.a.sent, w.a.stopped)
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("giving lanes up raised a hold")
	}
	if kept := everythingKept(t, w.world); strings.Contains(kept, "thr_lane_") {
		t.Fatal("a lane ID was kept or reported")
	}
	// The incident history and the saved record say what was done.
	j, err := incident.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	retried, givenUp := 0, 0
	for _, r := range j.Records {
		retried, givenUp = retried+r.Lanes.Retried, givenUp+r.Lanes.GivenUp
	}
	if retried != 18 || givenUp != 6 {
		t.Fatalf("history: %d retried, %d given up", retried, givenUp)
	}
	// Every lane is given up, so the episode is over: its count starts again
	// for the next failure, and the given-up lanes stay given up.
	l := w.lanes(t)
	if l.EpisodeRetries != 0 || len(l.Retries) != 6 {
		t.Fatalf("%+v", l)
	}
	for h, r := range l.Retries {
		if !r.GivenUp || !r.Reported || r.Attempts != 3 {
			t.Fatalf("%s: %+v", h, r)
		}
	}
	// What was recorded as sent: eighteen lane retries, each a child_retry
	// against the loop's own target, with no lane named.
	g, availability := rungraph.Load(w.dir)
	laneSends := 0
	for _, a := range g.Attempts {
		if a.Action == "child_retry" && a.Mode == "turn_retry" && a.Target == state.Primary {
			laneSends++
		}
	}
	if availability != rungraph.Available || laneSends != 18 {
		t.Fatalf("graph %s: %d lane sends", availability, laneSends)
	}
}

// The episode cap bounds the whole failure: forty lanes, twenty retries.
func TestLaneRetriesAreBoundedPerEpisode(t *testing.T) {
	w := newLaneWorld(t)
	w.cfg.Loop.Agent.BB.Children.MaxPerCheck = 20
	ids := laneIDs(40)
	w.working(t, t0, ids...)
	w.failed(ids...)
	for i := 1; i <= 60; i += 3 {
		w.tick(t, t0.Add(time.Duration(i)*time.Minute))
	}
	if len(w.a.retried) != 20 {
		t.Fatalf("%d retries in one episode", len(w.a.retried))
	}
	// Lanes the spent episode will not retry are given up and reported, not
	// left in error unmentioned.
	reports := w.laneReports()
	if len(reports) == 0 || len(reports) > laneMaxReports || reports[len(reports)-1].Data["givenUp"] != 40 {
		t.Fatalf("%d reports: %+v", len(reports), reports)
	}
	// The lanes recover and stay healthy for min_wake_interval (one hour): a
	// later failure is a new episode.
	w.working(t, t0.Add(2*time.Hour), ids...)
	w.working(t, t0.Add(3*time.Hour+time.Minute), ids...)
	w.failed(ids[:2]...)
	if res := w.tick(t, t0.Add(3*time.Hour+2*time.Minute)); res.Lanes == nil || res.Lanes.Retried != 2 {
		t.Fatalf("%+v", res.Lanes)
	}
}

// A lane that fails again straight after a retry spends the same attempts:
// its record is kept until it has stayed out of error for min_wake_interval.
func TestLaneThatFailsAgainSpendsTheSameAttempts(t *testing.T) {
	w := newLaneWorld(t)
	id := laneIDs(1)
	w.working(t, t0, id...)
	for i := 0; i < 8; i++ {
		at := t0.Add(time.Duration(i*22+1) * time.Minute)
		w.failed(id...)
		w.tick(t, at)
		w.working(t, at.Add(11*time.Minute), id...) // the retry worked for a while
	}
	if len(w.a.retried) != 3 {
		t.Fatalf("%d retries of a lane that keeps failing", len(w.a.retried))
	}
}

// With too little of the check's time left, the remaining lanes are put off,
// recorded as deferred and taken first on the next check.
func TestLanesAreDeferredWhenTheCheckIsShortOfTime(t *testing.T) {
	w := newLaneWorld(t)
	ids := laneIDs(12)
	w.working(t, t0, ids...)
	w.failed(ids...)
	wall := t0
	w.clock = func() time.Time { return wall }
	w.a.onInspect = func() { wall = wall.Add(40 * time.Second) }
	res := w.tick(t, t0.Add(time.Minute))
	if res.Lanes == nil || res.Lanes.Retried == 0 || res.Lanes.Deferred == 0 || res.Lanes.Retried+res.Lanes.Deferred != 5 {
		t.Fatalf("%+v", res.Lanes)
	}
	first := append([]string{}, w.a.retried...)
	deferred := w.lanes(t).Deferred
	if len(deferred) != res.Lanes.Deferred {
		t.Fatalf("%v", deferred)
	}
	if j, _ := incident.Load(w.dir); j.Records[len(j.Records)-1].Lanes.Deferred != res.Lanes.Deferred {
		t.Fatalf("%+v", j.Records[len(j.Records)-1].Lanes)
	}
	wall = t0.Add(time.Hour)
	w.a.onInspect = nil
	res = w.tick(t, t0.Add(2*time.Minute))
	next := w.a.retried[len(first):]
	if res.Lanes.Retried != 5 || res.Lanes.Deferred != 0 || len(next) != 5 {
		t.Fatalf("%+v %v", res.Lanes, next)
	}
	name := func(id string) string { return laneName(t, w.world, id) }
	for i, h := range deferred { // the deferred lanes lead the next check, in order
		if name(next[i]) != h {
			t.Fatalf("deferred lanes were not taken first: %v then %v", deferred, next)
		}
	}
	// One lane that uses up the check leaves the rest for the next one.
	w2 := newLaneWorld(t)
	w2.working(t, t0, ids...)
	w2.failed(ids...)
	wall = t0.Add(2 * time.Hour)
	w2.clock = func() time.Time { return wall }
	w2.a.onInspect = func() { wall = wall.Add(10 * time.Minute) }
	if res := w2.tick(t, t0.Add(time.Minute)); len(w2.a.inspected) != 1 || res.Lanes == nil || res.Lanes.Deferred != 4 {
		t.Fatalf("%+v %v", res.Lanes, w2.a.inspected)
	}
}

// An outage of any length: nothing is asked of BB and nothing is spent while
// the network is down, no hold is raised, and the first check after it
// returns retries each lane once.
func TestLanesWaitOutAnOutageOfAnyLength(t *testing.T) {
	for _, outage := range []time.Duration{20 * time.Minute, 4 * time.Hour, 3 * 24 * time.Hour, 21 * 24 * time.Hour} {
		w := newLaneWorld(t)
		ids := laneIDs(3)
		w.working(t, t0, ids...)
		w.failed(ids...)
		online := false
		w.online = func(context.Context) bool { return online }
		step := outage / 12
		for at := time.Minute; at < outage; at += step {
			res := w.tick(t, t0.Add(at))
			if len(w.a.inspected)+len(w.a.retried) != 0 || res.Lanes == nil || res.Lanes.Waiting != "network" || res.Lanes.Retried != 0 {
				t.Fatalf("%s offline, at %s: %+v %v", outage, at, res.Lanes, w.a.inspected)
			}
		}
		sendsUsed(t, w.world, 0)
		if l := w.lanes(t); l.EpisodeRetries != 0 || len(l.Retries) != 0 {
			t.Fatalf("%s: counted while offline: %+v", outage, l)
		}
		online = true
		res := w.tick(t, t0.Add(outage))
		if res.Lanes == nil || res.Lanes.Retried != 3 || res.Lanes.Waiting != "" || strings.Join(w.a.retried, ",") != strings.Join(ids, ",") && len(w.a.retried) != 3 {
			t.Fatalf("%s: first check online: %+v %v", outage, res.Lanes, w.a.retried)
		}
		sendsUsed(t, w.world, 3)
		res = w.tick(t, t0.Add(outage+time.Minute))
		if len(w.a.retried) != 3 || res.Lanes != nil {
			t.Fatalf("%s: retried again a minute later: %v %+v", outage, w.a.retried, res.Lanes)
		}
		if ctl, _ := control.Load(w.dir); ctl.AwaitingUser || len(w.laneReports()) != 0 {
			t.Fatalf("%s: hold=%v reports=%s", outage, ctl.AwaitingUser, w.reportTypes())
		}
	}
}

// A turn that failed long ago may no longer be retryable. BB then answers
// that there is nothing to retry: the lane is not prompted instead, no
// attempt or send is spent, and it is not asked again inside the spacing.
func TestLaneWithNothingToRetryIsLeftAlone(t *testing.T) {
	w := newLaneWorld(t)
	id := laneIDs(1)
	w.working(t, t0, id...)
	w.failed(id...)
	for i, outcome := range []adapter.TurnRetryOutcome{adapter.TurnRetryNothing, adapter.TurnRetryAlreadyQueued} {
		w.a.answer = func(string) (adapter.TurnRetry, error) { return adapter.TurnRetry{Outcome: outcome}, nil }
		at := t0.Add(time.Duration(i*11+1) * time.Minute)
		res := w.tick(t, at)
		// It is not asked again inside the spacing.
		if again := w.tick(t, at.Add(9*time.Minute)); again.Lanes != nil || len(w.a.retried) != i+1 {
			t.Fatalf("%s: asked again inside the spacing: %+v", outcome, again.Lanes)
		}
		if res.Lanes == nil || res.Lanes.Retried != 0 || res.Lanes.NotRetried[string(outcome)] != 1 {
			t.Fatalf("%s: %+v", outcome, res.Lanes)
		}
	}
	sendsUsed(t, w.world, 0)
	if l := w.lanes(t); l.EpisodeRetries != 0 || len(w.a.sent) != 0 || len(w.laneReports()) != 0 {
		t.Fatalf("%+v sent=%v reports=%s", l, w.a.sent, w.reportTypes())
	}
	// A refused or unreadable answer may have been acted on: it is counted.
	w.a.answer = func(string) (adapter.TurnRetry, error) { return adapter.TurnRetry{}, fmt.Errorf("exit status 1") }
	res := w.tick(t, t0.Add(30*time.Minute))
	if res.Lanes.NotRetried[turnRetryRefused] != 1 || w.lanes(t).EpisodeRetries != 1 {
		t.Fatalf("%+v", res.Lanes)
	}
	sendsUsed(t, w.world, 1)
	// A provider version without the command stops lane work for the check.
	w2 := newLaneWorld(t)
	ids := laneIDs(4)
	w2.working(t, t0, ids...)
	w2.failed(ids...)
	w2.a.answer = func(string) (adapter.TurnRetry, error) {
		return adapter.TurnRetry{}, adapter.ErrUnsupported{Op: "retry"}
	}
	if res := w2.tick(t, t0.Add(time.Minute)); len(w2.a.retried) != 1 || res.Lanes.NotRetried[turnRetryUnsupported] != 1 {
		t.Fatalf("%+v %v", res.Lanes, w2.a.retried)
	}
	sendsUsed(t, w2.world, 0)
}

// Only a lane whose every fact is established is retried. A lane waiting on
// a person, already queued, retrying on its own, or of unknown state is left
// alone silently; a rate-limited one and one that needs a login are left
// alone and named, by count only, in one owner report.
func TestOnlyALaneWithEveryFactEstablishedIsRetried(t *testing.T) {
	w := newLaneWorld(t)
	ids := laneIDs(11)
	w.working(t, t0, ids...)
	w.failed(ids...)
	w.cfg.Loop.Agent.BB.Children.MaxPerCheck = 20
	f := retryable
	set := func(i int, change func(f *decide.LaneFacts)) {
		c := f
		change(&c)
		w.a.facts[ids[i]] = c
	}
	set(1, func(f *decide.LaneFacts) { f.WaitingOnPerson = true })
	set(2, func(f *decide.LaneFacts) { f.InputKnown = false })
	set(3, func(f *decide.LaneFacts) { f.Class = decide.ErrRateLimit })
	set(4, func(f *decide.LaneFacts) { f.Class = decide.ErrAuth })
	set(5, func(f *decide.LaneFacts) { f.Queued = 1 })
	set(6, func(f *decide.LaneFacts) { f.Retrying = true })
	set(7, func(f *decide.LaneFacts) { f.ClassKnown = false })
	set(8, func(f *decide.LaneFacts) { f.Verified = false })
	set(9, func(f *decide.LaneFacts) { f.Class = decide.ErrRestricted })
	set(10, func(f *decide.LaneFacts) { f.Class = decide.ErrRefused })
	res := w.tick(t, t0.Add(time.Minute))
	if strings.Join(w.a.retried, ",") != ids[0] || res.Lanes.Retried != 1 {
		t.Fatalf("retried %v: %+v", w.a.retried, res.Lanes)
	}
	want := map[string]int{"waiting_on_person": 2, "rate_limit": 1, "auth": 1, "declined": 2, "queued": 1, "provider_retrying": 1, "unknown": 1, "unverified": 1}
	for k, n := range want {
		if res.Lanes.NotRetried[k] != n {
			t.Fatalf("%s: %+v", k, res.Lanes.NotRetried)
		}
	}
	if len(w.laneReports()) != 1 {
		t.Fatal(w.reportTypes())
	}
	e := w.laneReports()[0]
	if e.Data["rateLimited"] != 1 || e.Data["authFailed"] != 1 || e.Data["declined"] != 2 || e.Data["givenUp"] != 0 || e.Data["retried"] != 1 || !strings.Contains(e.Text, "1 were not retried because they are rate limited, 1 because they need a login and 2 because the provider refused or restricted them") {
		t.Fatalf("%+v", e)
	}
	// However long they stay that way they are never retried, and reported once.
	for i := 2; i < 200; i += 11 {
		w.tick(t, t0.Add(time.Duration(i)*time.Minute))
	}
	for _, id := range w.a.retried {
		if id != ids[0] {
			t.Fatalf("%s was retried", id)
		}
	}
	if len(w.laneReports()) != 2 { // the second: the first lane given up
		t.Fatal(w.reportTypes())
	}
	if kept := everythingKept(t, w.world); strings.Contains(kept, "thr_lane_") {
		t.Fatal("a lane ID was kept or reported")
	}
}

// Stop, pause, a hold, a dry run and a check outside the window all win:
// no lane is inspected or retried.
func TestStopPauseHoldsAndDryRunWinOverLaneRecovery(t *testing.T) {
	for name, block := range map[string]func(w *laneWorld){
		"stop":     func(w *laneWorld) { control.Update(w.dir, func(f *control.File) { f.Stop = true }) },
		"pause":    func(w *laneWorld) { control.Update(w.dir, func(f *control.File) { f.Paused = true }) },
		"hold":     func(w *laneWorld) { control.Update(w.dir, func(f *control.File) { f.AwaitingUser = true }) },
		"disabled": func(w *laneWorld) { control.Update(w.dir, func(f *control.File) { f.Enabled = false }) },
		"complete": func(w *laneWorld) { control.Update(w.dir, func(f *control.File) { f.Completed = true }) },
		"dry run":  func(w *laneWorld) { w.dryRun = true },
		"outside the window": func(w *laneWorld) {
			w.cfg.Loop.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Sat": {"01:00", "02:00"}}}
		},
		"a hold raised on this check": func(w *laneWorld) {
			w.a.obs.State, w.a.obs.ErrorClass = decide.Error, decide.ErrAuth
		},
		"the session is waiting on a person": func(w *laneWorld) { w.a.obs.AwaitingInput = true },
		"a lane is waiting on a person": func(w *laneWorld) {
			w.a.obs.AwaitingChildren = []string{"thr_lane_00"}
		},
		"the session is gone":            func(w *laneWorld) { w.a.obs.Gone = true },
		"the session is stopping":        func(w *laneWorld) { w.a.obs.State = decide.Stopping },
		"the session's state is unknown": func(w *laneWorld) { w.a.obs.State = decide.Unknown },
	} {
		w := newLaneWorld(t)
		ids := laneIDs(3)
		w.working(t, t0, ids...)
		w.failed(ids...)
		block(w)
		for i := 1; i < 40; i += 12 {
			w.tick(t, t0.Add(time.Duration(i)*time.Minute))
		}
		if len(w.a.inspected)+len(w.a.retried) != 0 {
			t.Errorf("%s: inspected %v, retried %v", name, w.a.inspected, w.a.retried)
		}
		used, _ := state.LoadSends(w.dir)
		if name != "a lane is waiting on a person" && name != "a hold raised on this check" && used != 0 {
			t.Errorf("%s: %d sends used", name, used)
		}
	}
}

// After the loop moves to a different conversation no lane of the old one is
// retried: not on the check that moves it, and not afterwards.
func TestNoLaneOfTheOldConversationIsRetriedAfterAMove(t *testing.T) {
	w := newLaneWorld(t)
	fb := newWorld(t, true, true)
	w.cfg.FallbackSet = fb.cfg.FallbackSet
	for i := range w.cfg.FallbackSet.Candidates {
		w.cfg.FallbackSet.Candidates[i].Agent.BB.Children = &config.BBChildren{RetryErrored: true}
		w.probeOK[w.cfg.FallbackSet.Candidates[i].ID] = true
	}
	ids := laneIDs(3)
	w.working(t, t0, ids...)
	// The session hits its limit while its lanes are in error: the loop moves.
	w.failed(ids...)
	w.a.obs.State, w.a.obs.ErrorClass = decide.Error, decide.ErrRateLimit
	res := w.tick(t, t0.Add(time.Minute))
	if res.Action != "handoff" || res.Handoff == nil || res.Handoff.To == "" {
		t.Fatalf("%+v", res)
	}
	if len(w.a.inspected)+len(w.a.retried) != 0 || w.lanes(t) != nil {
		t.Fatalf("the moving check touched lanes: %v %v %+v", w.a.inspected, w.a.retried, w.lanes(t))
	}
	// On the new conversation, which has no lanes of its own, the old lanes
	// stay in error on the old one. Nothing reaches them.
	for i := 2; i < 60; i += 11 {
		res := w.tick(t, t0.Add(time.Duration(i)*time.Minute))
		if res.Target == state.Primary || res.Lanes != nil {
			t.Fatalf("%+v", res)
		}
	}
	if len(w.a.inspected)+len(w.a.retried) != 0 {
		t.Fatalf("a lane of the old conversation was touched: %v %v", w.a.inspected, w.a.retried)
	}
}

// The session is told only about lanes keepgoing will not act on. Ten lanes
// fail together and five are retried per check: the other five are waiting
// their turn, not the session's to restart, so nothing is announced.
func TestLanesWaitingTheirTurnAreNotHandedToTheSession(t *testing.T) {
	w := newLaneWorld(t)
	ids := laneIDs(10)
	w.working(t, t0, ids...)
	w.failed(ids...)
	w.a.obs.State = decide.Idle
	for i, total := range []int{5, 10} {
		res := w.tick(t, t0.Add(time.Duration(i+1)*time.Minute))
		if res.Action != "wake" || res.ChildErrorNote || len(w.a.retried) != total || w.a.sent[i] != "keep going" {
			t.Fatalf("check %d: %+v retried=%d sent=%q", i, res, len(w.a.retried), w.a.sent[i])
		}
	}
	// A lane whose log is too long to read in one check is unknown at first.
	// It is still keepgoing's to recover, so it is not announced either.
	w = newLaneWorld(t)
	w.working(t, t0, ids[:2]...)
	w.failed(ids[:2]...)
	w.a.obs.State = decide.Idle
	unread := retryable
	unread.ClassKnown = false
	w.a.facts[ids[0]] = unread
	if res := w.tick(t, t0.Add(time.Minute)); res.ChildErrorNote || len(w.a.retried) != 1 || res.Lanes.NotRetried["unknown"] != 1 {
		t.Fatalf("%+v %+v", res, res.Lanes)
	}
}

// When some lanes are keepgoing's and some are not, the notice says how many
// of each, and never which.
func TestNoticeSaysHowManyLanesAreBeingRecovered(t *testing.T) {
	w := newLaneWorld(t)
	w.cfg.Loop.Agent.BB.Children.MaxPerCheck = 20
	ids := laneIDs(7)
	w.working(t, t0, ids...)
	w.failed(ids...)
	w.a.obs.State = decide.Idle
	limited, login := retryable, retryable
	limited.Class, login.Class = decide.ErrRateLimit, decide.ErrAuth
	w.a.facts[ids[2]], w.a.facts[ids[5]] = limited, login
	res := w.tick(t, t0.Add(time.Minute))
	if res.Action != "wake" || !res.ChildErrorNote || res.Lanes.Retried != 5 || len(w.a.sent) != 1 {
		t.Fatalf("%+v", res)
	}
	text := w.a.sent[0]
	for _, want := range []string{"[keepgoing:child-errors count=7 recovering=5]", "keepgoing is recovering 5 of them itself: it asks BB to retry their failed turn, by reference and without sending a message", "The other 2 are yours", "check its current state"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "thr_lane_") {
		t.Fatalf("the notice names a lane:\n%s", text)
	}
	sendsUsed(t, w.world, 6) // five retries and the wake
	// Nothing new is said while those five are retried again.
	if res := w.tick(t, t0.Add(12*time.Minute)); res.ChildErrorNote || len(w.a.retried) != 10 {
		t.Fatalf("%+v", res)
	}
}

// The supervised session comes first. Lane retries never use the sends its
// own recovery needs (errors.max_reprompts and one notice), and raise no hold.
func TestLaneRetriesLeaveTheSessionItsSends(t *testing.T) {
	w := newLaneWorld(t) // max_reprompts 3: four sends are kept back
	w.cfg.Loop.Limits.MaxSends = 6
	ids := laneIDs(4)
	w.working(t, t0, ids...)
	w.failed(ids...)
	res := w.tick(t, t0.Add(time.Minute))
	if len(w.a.retried) != 2 || res.Lanes.Retried != 2 || res.Lanes.NotRetried["send_allowance"] != 1 || len(w.a.inspected) != 2 {
		t.Fatalf("%+v retried=%v inspected=%v", res.Lanes, w.a.retried, w.a.inspected)
	}
	sendsUsed(t, w.world, 2)
	// With two sends left and the session itself in error, the session is
	// recovered and no lane is: before, the lanes took both and the loop held.
	w = newLaneWorld(t)
	w.cfg.Loop.Limits.MaxSends = 10
	w.working(t, t0, ids...)
	for i := 0; i < 8; i++ {
		if _, err := state.Reserve(w.dir, 10, 0, state.SendIntent{At: t0, Action: "wake", Target: state.Primary}); err != nil {
			t.Fatal(err)
		}
	}
	w.failed(ids...)
	w.a.obs.State, w.a.obs.ErrorClass = decide.Error, decide.ErrNetwork
	res = w.tick(t, t0.Add(time.Minute))
	if res.Action != "reprompt" || len(w.a.retried) != 0 || len(w.a.sent) == 0 || !strings.Contains(w.a.sent[0], "keep going") {
		t.Fatalf("%+v retried=%v sent=%q", res, w.a.retried, w.a.sent)
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("the loop was held")
	}
	// The lanes cannot be recovered for now, so they are the session's: the
	// re-prompt carries the ordinary notice for all four.
	if !strings.Contains(w.a.sent[0], "[keepgoing:child-errors count=4]") {
		t.Fatalf("%q", w.a.sent[0])
	}
}

// Counters survive a restart (every check here loads its state from disk),
// and state written before lane recovery existed loads with nothing remembered.
func TestLaneRecordPersistsAndLegacyStateLoads(t *testing.T) {
	w := newLaneWorld(t)
	legacy := `{"activeTarget":"primary","lastState":"active","reprompts":1}`
	if err := os.WriteFile(filepath.Join(w.dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := laneIDs(2)
	w.working(t, t0, ids...)
	st := savedState(t, w.dir)
	if st.Reprompts != 1 || st.Lanes == nil || len(st.Lanes.SeenActive) != 2 {
		t.Fatalf("%+v %+v", st, st.Lanes)
	}
	w.failed(ids...)
	w.tick(t, t0.Add(time.Minute))
	b, err := os.ReadFile(filepath.Join(w.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Lanes struct {
			Retries        map[string]struct{ Attempts int }
			EpisodeRetries int
		}
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.Lanes.EpisodeRetries != 2 || len(raw.Lanes.Retries) != 2 {
		t.Fatalf("%v %s", err, b)
	}
	for h, r := range raw.Lanes.Retries {
		if len(h) != 24 || r.Attempts != 1 {
			t.Fatalf("%s %+v", h, r)
		}
	}
}

// A lane keepgoing is recovering is not announced to the session: there is
// nothing for it to do yet. The session hears of it once it is given up.
func TestLaneBeingRecoveredIsAnnouncedOnlyWhenGivenUp(t *testing.T) {
	w := newLaneWorld(t)
	ids := laneIDs(2)
	w.working(t, t0, ids...)
	w.failed(ids...)
	w.a.obs.State = decide.Idle
	var noted []int
	for i := 1; i <= 45; i += 11 {
		res := w.tick(t, t0.Add(time.Duration(i)*time.Minute))
		if res.Action != "wake" {
			t.Fatalf("minute %d: %+v", i, res)
		}
		if res.ChildErrorNote {
			noted = append(noted, i)
		}
	}
	// Retried at minutes 1, 12 and 23; given up at minute 34, and announced then.
	if len(w.a.retried) != 6 || len(noted) != 1 || noted[0] != 34 {
		t.Fatalf("retries %d, notices at %v", len(w.a.retried), noted)
	}
	for i, text := range w.a.sent {
		if has := strings.Contains(text, "[keepgoing:child-errors count=2]"); has != (i == 3) {
			t.Fatalf("send %d: %q", i, text)
		}
	}
}

// BB answering "nothing to retry" or "already queued" is not repeated for
// ever. After three such answers in a row the lane is given up and reported,
// and none of them is recorded as a send.
func TestLaneNoOpAnswersAreBoundedAndNotRecordedAsSends(t *testing.T) {
	for _, outcome := range []adapter.TurnRetryOutcome{adapter.TurnRetryNothing, adapter.TurnRetryAlreadyQueued} {
		w := newLaneWorld(t)
		id := laneIDs(1)
		w.working(t, t0, id...)
		w.failed(id...)
		w.a.answer = func(string) (adapter.TurnRetry, error) { return adapter.TurnRetry{Outcome: outcome}, nil }
		for minute := 1; minute <= 24*60; minute += 11 {
			w.tick(t, t0.Add(time.Duration(minute)*time.Minute))
		}
		if len(w.a.retried) != 3 {
			t.Fatalf("%s: BB was asked %d times in a day", outcome, len(w.a.retried))
		}
		reports := w.laneReports()
		if len(reports) != 1 || reports[0].Data["givenUp"] != 1 {
			t.Fatalf("%s: %+v", outcome, reports)
		}
		sendsUsed(t, w.world, 0)
		if g, _ := rungraph.Load(w.dir); len(g.Attempts) != 0 {
			t.Fatalf("%s: a no-op was recorded as a send: %+v", outcome, g.Attempts)
		}
	}
}

// One lane that is given up and stays in error does not keep the episode
// open: lanes that fail on later days get their own retries, however many
// retries there have been before.
func TestGivenUpLaneDoesNotKeepTheEpisodeOpen(t *testing.T) {
	w := newLaneWorld(t)
	abandoned := "thr_lane_abandoned"
	w.working(t, t0, abandoned)
	w.failed(abandoned)
	for minute := 1; minute <= 34; minute += 11 {
		w.tick(t, t0.Add(time.Duration(minute)*time.Minute))
	}
	if len(w.a.retried) != 3 || !strings.Contains(w.reportTypes(), "child_recovery") {
		t.Fatalf("%v %s", w.a.retried, w.reportTypes())
	}
	for day := 1; day <= 11; day++ {
		at := t0.Add(time.Duration(day) * 24 * time.Hour)
		lane := fmt.Sprintf("thr_lane_day%02d", day)
		w.a.obs.ActiveChildren, w.a.obs.ActiveChildIDs, w.a.obs.ErroredChildren = 1, []string{lane}, []string{abandoned}
		w.tick(t, at)
		w.failed(abandoned, lane)
		before := len(w.a.retried)
		for minute := 1; minute <= 34; minute += 11 {
			w.tick(t, at.Add(time.Duration(minute)*time.Minute))
		}
		if got := len(w.a.retried) - before; got != 3 {
			t.Fatalf("day %d: %d retries of that day's lane (episode count %d)", day, got, w.lanes(t).EpisodeRetries)
		}
		if n := w.lanes(t).EpisodeRetries; n != 0 {
			t.Fatalf("day %d: the episode was left open at %d", day, n)
		}
	}
	for _, id := range w.a.retried[3:] {
		if id == abandoned {
			t.Fatal("the given-up lane was retried again")
		}
	}
	if len(w.a.retried) != 36 {
		t.Fatalf("%d retries", len(w.a.retried))
	}
}

// A lane that works for more than min_wake_interval after each retry and
// then fails again is a new failure each time, with new attempts. Its
// retries are still bounded per day: twice errors.max_reprompts, then it is
// given up and reported.
func TestLaneThatKeepsFailingAgainIsBoundedPerDay(t *testing.T) {
	w := newLaneWorld(t) // max_reprompts 3: six retries a day
	id := laneIDs(1)
	w.working(t, t0, id...)
	at := t0
	for at.Sub(t0) < 24*time.Hour {
		w.failed(id...)
		at = at.Add(time.Minute)
		w.tick(t, at)
		// The retry works: the lane is seen working, and again 61 minutes later.
		at = at.Add(time.Minute)
		w.working(t, at, id...)
		at = at.Add(61 * time.Minute)
		w.working(t, at, id...)
	}
	if len(w.a.retried) != 6 {
		t.Fatalf("%d retries in 24 hours", len(w.a.retried))
	}
	if reports := w.laneReports(); len(reports) != 1 || reports[0].Data["givenUp"] != 1 {
		t.Fatalf("%+v", reports)
	}
	// A new day, and a lane that has been healthy: it is recoverable again.
	at = at.Add(24 * time.Hour)
	w.working(t, at, id...)
	w.failed(id...)
	if res := w.tick(t, at.Add(time.Minute)); res.Lanes == nil || res.Lanes.Retried != 1 {
		t.Fatalf("%+v", res.Lanes)
	}
}

// Stop or pause requested while a check is working through its lanes is seen
// before the next lane: nothing more is retried in that check.
func TestStopDuringACheckEndsLaneWorkAtOnce(t *testing.T) {
	for name, ask := range map[string]func(f *control.File){
		"stop":  func(f *control.File) { f.Stop = true },
		"pause": func(f *control.File) { f.Paused = true },
		"hold":  func(f *control.File) { f.AwaitingUser = true },
	} {
		w := newLaneWorld(t)
		ids := laneIDs(5)
		w.working(t, t0, ids...)
		w.failed(ids...)
		w.a.answer = func(string) (adapter.TurnRetry, error) {
			if _, err := control.Update(w.dir, ask); err != nil {
				t.Fatal(err)
			}
			return adapter.TurnRetry{Outcome: adapter.TurnRetried}, nil
		}
		res := w.tick(t, t0.Add(time.Minute))
		if len(w.a.retried) != 1 || len(w.a.inspected) != 1 || res.Lanes == nil || res.Lanes.Waiting != "control" {
			t.Fatalf("%s: retried %v inspected %v %+v", name, w.a.retried, w.a.inspected, res.Lanes)
		}
	}
}

// A report that could not be sent when lanes were given up, because the loop
// was not subscribed or the network was down, is sent by a later check.
func TestLaneReportPutOffIsSentLater(t *testing.T) {
	w := newLaneWorld(t)
	w.cfg.Loop.Report = &config.Report{Events: []string{"await_user"}}
	id := laneIDs(1)
	w.working(t, t0, id...)
	w.failed(id...)
	for minute := 1; minute <= 45; minute += 11 {
		w.tick(t, t0.Add(time.Duration(minute)*time.Minute))
	}
	if len(w.a.retried) != 3 || len(w.laneReports()) != 0 {
		t.Fatalf("%v %s", w.a.retried, w.reportTypes())
	}
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	online := false
	w.online = func(context.Context) bool { return online }
	w.tick(t, t0.Add(50*time.Minute))
	if len(w.laneReports()) != 0 {
		t.Fatal("reported into a dead network")
	}
	online = true
	w.tick(t, t0.Add(51*time.Minute))
	w.tick(t, t0.Add(52*time.Minute))
	if reports := w.laneReports(); len(reports) != 1 || reports[0].Data["givenUp"] != 1 {
		t.Fatalf("%+v", reports)
	}
}

// laneName is a lane's name in the loop's records: its ID hashed with the
// loop's lanes.key.
func laneName(t *testing.T, w *world, id string) string {
	t.Helper()
	key, err := state.LaneKey(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	return state.LaneHash(key, id)
}

// plantLaneRetry records a lane retry the way a check does just before it
// asks BB, and leaves it unsettled: the check was killed before it could
// save what happened.
func plantLaneRetry(t *testing.T, w *laneWorld, id string, at time.Time) {
	t.Helper()
	if _, err := state.Reserve(w.dir, w.cfg.Loop.Limits.MaxSends, 0, state.SendIntent{At: at, Action: laneRetryAction, Target: state.Primary, Key: laneName(t, w.world, id)}); err != nil {
		t.Fatal(err)
	}
}

// A check that recorded a lane retry and was killed before it saved what
// happened. The next check counts that retry once, as made: one attempt,
// one for the day and the episode, and the spacing from when it was
// recorded. It does not retry that lane again in that check; the lane that
// was not reached is retried as usual.
func TestRecordedLaneRetryIsCountedOnceAfterAKilledCheck(t *testing.T) {
	w := newLaneWorld(t) // three attempts, ten minutes apart
	ids := laneIDs(2)
	w.working(t, t0, ids...)
	w.failed(ids...)
	killedAt := t0.Add(time.Minute)
	plantLaneRetry(t, w, ids[0], killedAt)
	res := w.tick(t, killedAt.Add(time.Minute))
	if strings.Join(w.a.retried, ",") != ids[1] || res.Lanes == nil || res.Lanes.Retried != 1 {
		t.Fatalf("retried %v, %+v", w.a.retried, res.Lanes)
	}
	l := w.lanes(t)
	r := l.Retries[laneName(t, w.world, ids[0])]
	if r.Attempts != 1 || r.DayRetries != 1 || !r.LastAt.Equal(killedAt) || !r.DayStart.Equal(killedAt) || l.EpisodeRetries != 2 {
		t.Fatalf("%+v, episode %d", r, l.EpisodeRetries)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "a lane retry was recorded at") {
		t.Fatalf("%v", res.Warnings)
	}
	// Counted once: the next check, inside the spacing, neither retries it
	// nor counts the record again.
	w.tick(t, killedAt.Add(5*time.Minute))
	if len(w.a.retried) != 1 || w.lanes(t).Retries[laneName(t, w.world, ids[0])].Attempts != 1 || w.lanes(t).EpisodeRetries != 2 {
		t.Fatalf("retried %v, %+v", w.a.retried, w.lanes(t))
	}
	// Ten minutes after it was recorded, its second attempt (the other lane's
	// spacing has run out too, and it gets its second).
	w.tick(t, killedAt.Add(11*time.Minute))
	first := 0
	for _, id := range w.a.retried {
		if id == ids[0] {
			first++
		}
	}
	if first != 1 || len(w.a.retried) != 3 || w.lanes(t).Retries[laneName(t, w.world, ids[0])].Attempts != 2 {
		t.Fatalf("retried %v, %+v", w.a.retried, w.lanes(t))
	}
}

// With lane recovery switched off since the killed check, its record counts
// for nothing: no lane record, no warning, and not a send to the session.
func TestRecordedLaneRetryCountsForNothingOnceSwitchedOff(t *testing.T) {
	w := newLaneWorld(t)
	ids := laneIDs(1)
	w.working(t, t0, ids...)
	w.failed(ids...)
	killedAt := t0.Add(time.Minute)
	plantLaneRetry(t, w, ids[0], killedAt)
	before := savedState(t, w.dir).LastWakeAt
	w.cfg.Loop.Agent.BB.Children = nil
	res := w.tick(t, killedAt.Add(time.Minute))
	st := savedState(t, w.dir)
	if st.Lanes != nil || len(w.a.retried) != 0 || !st.LastWakeAt.Equal(before) {
		t.Fatalf("%+v retried %v wake %v", st.Lanes, w.a.retried, st.LastWakeAt)
	}
	if strings.Contains(strings.Join(res.Warnings, "\n"), "a lane retry was recorded at") {
		t.Fatalf("%v", res.Warnings)
	}
}

// 1. Another loop's lane, by its session record or by its live claim, is
// that loop's: it is never retried here, and it is counted.
func TestAnotherLoopsLaneIsNeverRetried(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, w *laneWorld, id string) func(){
		"recorded for another loop": func(t *testing.T, w *laneWorld, id string) func() {
			other := t.TempDir()
			if err := session.Note(w.sessions, session.Identity{Kind: "bb", ID: id}, session.Supervisor{Loop: "other-loop", Config: filepath.Join(other, "keepgoing.yaml"), StateDir: filepath.Join(other, "state"), SeenAt: t0}, time.Second); err != nil {
				t.Fatal(err)
			}
			return func() {}
		},
		"claimed by another loop's live check": func(t *testing.T, w *laneWorld, id string) func() {
			c, err := session.Take(w.sessions, session.Identity{Kind: "bb", ID: id}, session.Holder{Loop: "other-loop", PID: 1, Since: t0, Until: t0.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			return c.Release
		},
	} {
		w := newLaneWorld(t)
		ids := laneIDs(2)
		w.working(t, t0, ids...)
		w.failed(ids...)
		release := plant(t, w, ids[0])
		res := w.tick(t, t0.Add(time.Minute))
		release()
		if strings.Join(w.a.retried, ",") != ids[1] || res.Lanes.NotRetried["other_loop"] != 1 {
			t.Fatalf("%s: retried %v, %+v", name, w.a.retried, res.Lanes)
		}
		for _, id := range w.a.inspected {
			if id == ids[0] {
				t.Fatalf("%s: another loop's lane was inspected", name)
			}
		}
	}
}

// 2. A stop that covers the lanes forgets what lane recovery knew of them.
// After a resume, a lane still in error is not retried until a check has
// seen it working again.
func TestStopWithLanesThenResumeDoesNotRestartALane(t *testing.T) {
	w := newLaneWorld(t)
	id := laneIDs(1)
	w.working(t, t0, id...)
	w.failed(id...)
	w.tick(t, t0.Add(time.Minute))
	if len(w.a.retried) != 1 {
		t.Fatalf("%v", w.a.retried)
	}
	at := t0.Add(2 * time.Minute)
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop, f.StopLanesAt = true, &at }); err != nil {
		t.Fatal(err)
	}
	w.tick(t, t0.Add(3*time.Minute))
	if w.lanes(t) != nil {
		t.Fatalf("lane records kept through a stop of the lanes: %+v", w.lanes(t))
	}
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop, f.StopLanesAt = false, nil }); err != nil {
		t.Fatal(err)
	}
	for _, m := range []int{20, 40, 60} {
		w.tick(t, t0.Add(time.Duration(m)*time.Minute))
	}
	if len(w.a.retried) != 1 {
		t.Fatalf("a lane was restarted after the stop: %v", w.a.retried)
	}
	// Seen working again, then failing: recoverable as before.
	w.working(t, t0.Add(70*time.Minute), id...)
	w.failed(id...)
	w.tick(t, t0.Add(71*time.Minute))
	if len(w.a.retried) != 2 {
		t.Fatalf("%v", w.a.retried)
	}
}

// 3. A stop saved while a lane is being inspected ends lane work before
// that lane's retry.
func TestStopSavedDuringInspectionStopsThatRetry(t *testing.T) {
	w := newLaneWorld(t)
	id := laneIDs(1)
	w.working(t, t0, id...)
	w.failed(id...)
	w.a.onInspect = func() {
		if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
			t.Fatal(err)
		}
	}
	res := w.tick(t, t0.Add(time.Minute))
	if len(w.a.retried) != 0 || len(w.a.inspected) != 1 || res.Lanes == nil || res.Lanes.Waiting != "control" {
		t.Fatalf("retried %v, %+v", w.a.retried, res.Lanes)
	}
	sendsUsed(t, w.world, 0)
}

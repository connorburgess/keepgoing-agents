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

// everythingKept is all keepgoing wrote for the loop, to search for what
// must never be kept.
func everythingKept(t *testing.T, w *world) string {
	t.Helper()
	var all strings.Builder
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join(w.dir, e.Name())); err == nil {
			all.Write(b)
		}
	}
	reports, _ := json.Marshal(w.reports)
	all.Write(reports)
	return all.String()
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
	}
	w := newLaneWorld(t)
	ids := laneIDs(2)
	w.working(t, t0, ids...)
	w.failed(ids...)
	if res := w.tick(t, t0.Add(time.Minute)); res.Lanes == nil || res.Lanes.Retried != 2 || w.lanes(t) == nil {
		t.Fatalf("%+v", res.Lanes)
	}
	w.cfg.Loop.Agent.BB.Children.RetryErrored = false
	if res := w.tick(t, t0.Add(20*time.Minute)); res.Lanes != nil || w.lanes(t) != nil || len(w.a.retried) != 2 {
		t.Fatalf("switched off: %+v %+v", res.Lanes, w.lanes(t))
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
	if _, kept := w.lanes(t).SeenActive[decide.ChildHashes(ids[1:2])[0]]; kept {
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
	if l := w.lanes(t); l.EpisodeRetries != 18 || len(l.Retries) != 6 || l.Reports != 2 {
		t.Fatalf("%+v", l)
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
	for i, h := range deferred { // the deferred lanes lead the next check, in order
		if decide.ChildHashes(next[i : i+1])[0] != h {
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
// attempt or send is spent, it is not asked again inside the spacing, and it
// is never given up or reported.
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
	ids := laneIDs(9)
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
	res := w.tick(t, t0.Add(time.Minute))
	if strings.Join(w.a.retried, ",") != ids[0] || res.Lanes.Retried != 1 {
		t.Fatalf("retried %v: %+v", w.a.retried, res.Lanes)
	}
	want := map[string]int{"waiting_on_person": 2, "rate_limit": 1, "auth": 1, "queued": 1, "provider_retrying": 1, "unknown": 1, "unverified": 1}
	for k, n := range want {
		if res.Lanes.NotRetried[k] != n {
			t.Fatalf("%s: %+v", k, res.Lanes.NotRetried)
		}
	}
	if len(w.laneReports()) != 1 {
		t.Fatal(w.reportTypes())
	}
	e := w.laneReports()[0]
	if e.Data["rateLimited"] != 1 || e.Data["authFailed"] != 1 || e.Data["givenUp"] != 0 || e.Data["retried"] != 1 || !strings.Contains(e.Text, "1 were not retried because they are rate limited and 1 because they need a login") {
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
		"the session is gone": func(w *laneWorld) { w.a.obs.Gone = true },
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

// The notice to an idle session says how many lanes BB was asked to retry.
func TestNoticeSaysHowManyLanesWereRetried(t *testing.T) {
	w := newLaneWorld(t)
	ids := laneIDs(7)
	w.working(t, t0, ids...)
	w.failed(ids...)
	w.a.obs.State = decide.Idle
	res := w.tick(t, t0.Add(time.Minute))
	if res.Action != "wake" || !res.ChildErrorNote || res.Lanes.Retried != 5 || len(w.a.sent) != 1 {
		t.Fatalf("%+v", res)
	}
	text := w.a.sent[0]
	for _, want := range []string{"[keepgoing:child-errors count=7", "retried=5", "keepgoing has asked BB to retry the failed turn of 5 of them, by reference and without sending a message", "check each child before you retry or replace it"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "thr_lane_") {
		t.Fatalf("the notice names a lane:\n%s", text)
	}
	sendsUsed(t, w.world, 6) // five retries and the wake
}

// Lane retries stop at the loop's send allowance and raise no hold of their own.
func TestLaneRetriesStopAtTheSendAllowance(t *testing.T) {
	w := newLaneWorld(t)
	w.cfg.Loop.Limits.MaxSends = 2
	ids := laneIDs(4)
	w.working(t, t0, ids...)
	w.failed(ids...)
	res := w.tick(t, t0.Add(time.Minute))
	if len(w.a.retried) != 2 || res.Lanes.Retried != 2 || res.Lanes.NotRetried["send_allowance"] != 1 {
		t.Fatalf("%+v %v", res.Lanes, w.a.retried)
	}
	sendsUsed(t, w.world, 2)
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

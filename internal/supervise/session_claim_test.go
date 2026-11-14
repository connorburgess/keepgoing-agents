package supervise

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// otherLoopClaims holds a session's claim as another loop's check would.
func otherLoopClaims(t *testing.T, w *world, thread string) *session.Claim {
	t.Helper()
	c, err := session.Take(w.sessions, session.Identity{Kind: "bb", ID: thread}, session.Holder{Loop: "example-loop-b", PID: os.Getpid(), Since: time.Now(), Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Release)
	return c
}

// While another loop's check holds the session, this loop's check observes
// nothing, sends nothing and records nothing: it is skipped as a busy check.
func TestCheckIsSkippedWhileAnotherLoopActsOnTheSession(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	claim := otherLoopClaims(t, w, "thr_primary")
	res, err := Tick(context.Background(), w.cfg, w.opts(t0))
	var claimed *session.ClaimedError
	if !errors.As(err, &claimed) || !errors.Is(err, lock.ErrHeld) || !strings.Contains(err.Error(), "loop example-loop-b") {
		t.Fatalf("%v", err)
	}
	if res.Action != "none" || res.Reason != "another loop is acting on this session" || res.State != "not observed" {
		t.Fatalf("%+v", res)
	}
	if p.observed != 0 || len(p.sent) != 0 || len(w.probes) != 0 || len(w.reports) != 0 {
		t.Fatalf("observed %d, sent %v, probes %v, reports %d", p.observed, p.sent, w.probes, len(w.reports))
	}
	if st, _ := state.Load(w.dir); st.LastCheck != nil || st.Nudges != 0 || st.Reprompts != 0 {
		t.Fatalf("the skipped check was recorded or spent something: %+v", st)
	}
	// Once the other loop is done, the next check acts normally, and the
	// claim it took is released when it ends.
	claim.Release()
	if res := runTick(t, w, t0.Add(time.Minute)); res.Action != "wake" || len(p.sent) != 1 {
		t.Fatalf("after release: %+v", res)
	}
	c, err := session.Take(w.sessions, session.Identity{Kind: "bb", ID: "thr_primary"}, session.Holder{Loop: "example-loop-b", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("the check kept its claim after it ended: %v", err)
	}
	c.Release()
}

// A stop request is also an action on the session: it waits for the claim.
func TestStopWaitsForTheSessionClaim(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Active})
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	otherLoopClaims(t, w, "thr_primary")
	if _, err := Tick(context.Background(), w.cfg, w.opts(t0)); !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("%v", err)
	}
	if p.stopped != 0 || p.observed != 0 {
		t.Fatalf("stopped %d, observed %d", p.stopped, p.observed)
	}
}

// A fallback whose session another loop is acting on is not sent a brief;
// the next one is.
func TestHandoffSkipsAFallbackWhoseSessionIsClaimed(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.FallbackSet.Redundancy.MinHealthy = 1
	w.primary(limited)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	otherLoopClaims(t, w, "thr_claude-other")
	res := runTick(t, w, t0)
	if res.Action != "handoff" || res.Target != "codex-main" {
		t.Fatalf("%+v", res)
	}
	if fb := w.adapters["claude-other"]; fb != nil && len(fb.sent) != 0 {
		t.Fatalf("the claimed fallback was sent %v", fb.sent)
	}
	if res.Handoff == nil || !strings.Contains(strings.Join(res.Handoff.Skipped, ";"), "claude-other: another loop is acting on its session") {
		t.Fatalf("%+v", res.Handoff)
	}
}

// Returning to the primary waits while another loop acts on its session.
func TestSwitchBackWaitsWhileThePrimarySessionIsClaimed(t *testing.T) {
	w, p, fb := switchBackWorld(t)
	w.probeOK["primary"] = true
	p.obs = decide.Observation{State: decide.Idle}
	claim := otherLoopClaims(t, w, "thr_primary")
	res := runTick(t, w, t0.Add(2*time.Hour+time.Minute))
	if res.Action == "switch_back" || res.Target != "claude-other" || len(p.sent) != 0 || !strings.Contains(strings.Join(res.Warnings, ";"), "switch-back waits") {
		t.Fatalf("%+v, primary sent %v", res, p.sent)
	}
	_ = fb
	claim.Release()
	if res := runTick(t, w, t0.Add(3*time.Hour)); res.Action != "switch_back" || len(p.sent) != 1 {
		t.Fatalf("after release: %+v", res)
	}
}

const unprotected = "this check is not protected against another loop acting on the same session at the same moment"

func reportsOf(w *world, typ string) int {
	n := 0
	for _, e := range w.reports {
		if e.Type == typ && e.Data["sessionClaim"] != nil {
			n++
		}
	}
	return n
}

// A claim directory that is something else (here a file planted at its path)
// cannot be repaired safely: the check acts on nothing and fails, naming the
// path and the fix.
func TestAnUnsafeClaimDirectoryActsOnNothing(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	planted := w.sessions + "/sessions"
	if err := os.WriteFile(planted, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.DirEnv, planted)
	opts := w.opts(t0)
	opts.SessionDir = "" // the default, which is planted
	res, err := Tick(context.Background(), w.cfg, opts)
	if err == nil || errors.Is(err, lock.ErrHeld) || !strings.Contains(err.Error(), planted) || !strings.Contains(err.Error(), session.DirEnv) {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Reason != "session claim unsafe; no action taken" || p.observed != 0 || len(p.sent) != 0 {
		t.Fatalf("acted: %+v, observed %d", res, p.observed)
	}
	if st, _ := state.Load(w.dir); st.LastCheck == nil || st.LastCheck.Outcome != "failed" {
		t.Fatalf("not recorded as a failed check: %+v", st.LastCheck)
	}
}

// This user's directory that is merely too open is made private, and the
// check goes ahead protected, saying so once.
func TestATooOpenClaimDirectoryIsMadePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes differ")
	}
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	open := w.sessions + "/open"
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(session.DirEnv, open)
	opts := w.opts(t0)
	opts.SessionDir = ""
	res, err := Tick(context.Background(), w.cfg, opts)
	warned := strings.Contains(strings.Join(res.Warnings, ";"), "was not private; it is now 0700")
	if err != nil || res.Action != "wake" || len(p.sent) != 1 || !warned || strings.Contains(strings.Join(res.Warnings, ";"), unprotected) {
		t.Fatalf("%+v %v", res, err)
	}
	if info, _ := os.Stat(open); info.Mode().Perm() != 0o700 {
		t.Fatalf("%v", info.Mode())
	}
	opts = w.opts(t0.Add(2 * time.Hour))
	opts.SessionDir = ""
	if res, _ := Tick(context.Background(), w.cfg, opts); strings.Contains(strings.Join(res.Warnings, ";"), "was not private") {
		t.Fatal("said it twice")
	}
}

// With no user state directory at all (no HOME and no XDG_STATE_HOME), the
// check acts unprotected. The start and the end of that stretch are each
// reported once.
func TestChecksWithoutAStateDirectoryAreUnprotectedAndReportedOnce(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	p := w.primary(decide.Observation{State: decide.Idle})
	noStateHome(t)
	opts := func(at time.Time) Options {
		o := w.opts(at)
		o.SessionDir = "" // the default, which has nowhere to be
		return o
	}
	for i := 0; i < 3; i++ {
		res, err := Tick(context.Background(), w.cfg, opts(t0.Add(time.Duration(i)*2*time.Hour)))
		if err != nil || res.Action != "wake" || !strings.Contains(strings.Join(res.Warnings, ";"), unprotected) {
			t.Fatalf("check %d: %+v %v", i, res, err)
		}
	}
	if len(p.sent) != 3 || reportsOf(w, "degraded") != 1 || reportsOf(w, "recovered") != 0 {
		t.Fatalf("sent %d, degraded %d, recovered %d", len(p.sent), reportsOf(w, "degraded"), reportsOf(w, "recovered"))
	}
	if st, _ := state.Load(w.dir); !st.ClaimUnavailable {
		t.Fatal("the stretch was not recorded")
	}
	// The directory is back: the next check is protected again, and the end
	// is reported once.
	for i := 3; i < 5; i++ {
		res, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*2*time.Hour)))
		if err != nil || strings.Contains(strings.Join(res.Warnings, ";"), unprotected) {
			t.Fatalf("check %d: %+v %v", i, res, err)
		}
	}
	if reportsOf(w, "degraded") != 1 || reportsOf(w, "recovered") != 1 {
		t.Fatalf("degraded %d, recovered %d", reportsOf(w, "degraded"), reportsOf(w, "recovered"))
	}
	if st, _ := state.Load(w.dir); st.ClaimUnavailable {
		t.Fatal("the stretch was not ended")
	}
}

// A live claim still means no action, whatever else is unavailable.
func TestALiveClaimStillMeansNoAction(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	otherLoopClaims(t, w, "thr_primary")
	_, err := Tick(context.Background(), w.cfg, w.opts(t0))
	if !errors.Is(err, lock.ErrHeld) || p.observed != 0 || len(p.sent) != 0 {
		t.Fatalf("%v, observed %d, sent %d", err, p.observed, len(p.sent))
	}
}

// A handoff is not stopped by there being no user state directory.
func TestHandoffIsNotStoppedWithoutAStateDirectory(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.FallbackSet.Redundancy.MinHealthy = 1
	w.primary(limited)
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	noStateHome(t)
	opts := w.opts(t0)
	opts.SessionDir = ""
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Action != "handoff" || res.Target != "claude-other" {
		t.Fatalf("%+v %v", res, err)
	}
}

// Stop wins: if the claim cannot be taken for any reason other than another
// loop's live check, the stop goes ahead without it and says so. Pause takes
// no claim at all.
func TestStopProceedsWithoutAnUnusableClaim(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Active})
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	file := w.sessions + "/not-a-directory"
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := w.opts(t0)
	opts.SessionDir = file
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Action != "stop" || p.stopped != 1 || !strings.Contains(strings.Join(res.Warnings, ";"), "stop proceeds without the session claim") {
		t.Fatalf("%+v %v, stopped %d", res, err, p.stopped)
	}
}

func TestPauseTakesNoClaim(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = true }); err != nil {
		t.Fatal(err)
	}
	otherLoopClaims(t, w, "thr_primary")
	res, err := Tick(context.Background(), w.cfg, w.opts(t0))
	if err != nil || res.Reason != "paused" || p.observed != 0 || len(p.sent) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func noStateHome(t *testing.T) {
	t.Helper()
	t.Setenv(session.DirEnv, "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", "")
		t.Setenv("APPDATA", "")
	}
}

// flakyReporter fails the first n session claim reports it is given.
type flakyReporter struct {
	w    *world
	fail int
}

func (r *flakyReporter) Report(ctx context.Context, e report.Event) error {
	if r.fail > 0 && e.Data["sessionClaim"] != nil {
		r.fail--
		return errors.New("fictional delivery failure")
	}
	return r.w.Report(ctx, e)
}

// A degraded report that is not delivered is tried again at the next check,
// at most three times in all; it is not marked sent when delivery failed.
func TestClaimReportsAreRetriedUntilDelivered(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	w.primary(decide.Observation{State: decide.Idle})
	noStateHome(t)
	rep := &flakyReporter{w: w, fail: 2}
	tick := func(i int) {
		opts := w.opts(t0.Add(time.Duration(i) * 2 * time.Hour))
		opts.SessionDir, opts.Reporter = "", rep
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
	}
	tick(0)
	tick(1)
	if reportsOf(w, "degraded") != 0 {
		t.Fatal("a failed delivery was counted")
	}
	tick(2)
	tick(3)
	if reportsOf(w, "degraded") != 1 {
		t.Fatalf("delivered %d times", reportsOf(w, "degraded"))
	}
	// Three failures in a row: given up, never sent more than three times.
	w2 := newWorld(t, true, false)
	w2.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	w2.primary(decide.Observation{State: decide.Idle})
	never := &flakyReporter{w: w2, fail: 100}
	for i := 0; i < 5; i++ {
		opts := w2.opts(t0.Add(time.Duration(i) * 2 * time.Hour))
		opts.SessionDir, opts.Reporter = "", never
		_, _ = Tick(context.Background(), w2.cfg, opts)
	}
	if never.fail != 97 {
		t.Fatalf("tried %d times", 100-never.fail)
	}
}

// otherLoopStuck holds a session's claim as a check that is past its own
// time limit would.
func otherLoopStuck(t *testing.T, w *world, thread string) {
	t.Helper()
	c, err := session.Take(w.sessions, session.Identity{Kind: "bb", ID: thread}, session.Holder{Loop: "example-loop-b", PID: os.Getpid(), Since: time.Now().Add(-time.Hour), Until: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Release)
}

// A stop does not wait for a holder past its own time limit: that holder is
// stuck, and a second stop is harmless. The stuck holder is reported once.
func TestStopProceedsPastAStuckClaim(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	p := w.primary(decide.Observation{State: decide.Active})
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	otherLoopStuck(t, w, "thr_primary")
	res, err := Tick(context.Background(), w.cfg, w.opts(t0))
	if err != nil || res.Action != "stop" || p.stopped != 1 || !strings.Contains(strings.Join(res.Warnings, ";"), "stop proceeds past a stuck session claim") {
		t.Fatalf("%+v %v, stopped %d", res, err, p.stopped)
	}
	if reportsOf(w, "error") != 1 {
		t.Fatalf("stuck reports: %d", reportsOf(w, "error"))
	}
}

// An ordinary check meeting a stuck holder still does nothing, but the
// check is recorded and the stuck holder reported, once.
func TestAStuckClaimIsRecordedAndReportedOnce(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	p := w.primary(decide.Observation{State: decide.Idle})
	otherLoopStuck(t, w, "thr_primary")
	for i := 0; i < 3; i++ {
		res, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*time.Hour)))
		if !errors.Is(err, lock.ErrHeld) || res.Reason != "another loop's stuck check holds this session" {
			t.Fatalf("check %d: %+v %v", i, res, err)
		}
	}
	if p.observed != 0 || len(p.sent) != 0 || reportsOf(w, "error") != 1 {
		t.Fatalf("observed %d, sent %d, reports %d", p.observed, len(p.sent), reportsOf(w, "error"))
	}
	if st, _ := state.Load(w.dir); st.LastCheck == nil || st.StuckClaimReport == nil || !st.StuckClaimReport.Sent {
		t.Fatalf("%+v", st)
	}
}

// claimWatcher fails the test if the session's claim is free while it
// observes or sends: it tries to take the claim as another loop would.
type claimWatcher struct {
	*fakeAdapter
	t        *testing.T
	sessions string
	checked  int
}

func (c *claimWatcher) mustBeClaimed(when string) {
	c.checked++
	if claim, err := session.Take(c.sessions, session.Identity{Kind: "bb", ID: "thr_primary"}, session.Holder{Loop: "example-loop-b", PID: os.Getpid()}); err == nil {
		claim.Release()
		c.t.Errorf("the session was not claimed while %s", when)
	}
}

func (c *claimWatcher) Observe(ctx context.Context) (decide.Observation, error) {
	c.mustBeClaimed("observing")
	return c.fakeAdapter.Observe(ctx)
}

func (c *claimWatcher) Send(ctx context.Context, p string, m adapter.Mode) error {
	c.mustBeClaimed("sending")
	return c.fakeAdapter.Send(ctx, p, m)
}

// The claim is held from before the observation until after the send: a
// claim released in between would let another loop act at that moment.
func TestTheClaimIsHeldFromObservingToSending(t *testing.T) {
	w := newWorld(t, true, false)
	fake := w.primary(decide.Observation{State: decide.Idle})
	watcher := &claimWatcher{fakeAdapter: fake, t: t, sessions: w.sessions}
	opts := w.opts(t0)
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return watcher, nil }
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Action != "wake" || watcher.checked != 2 || len(fake.sent) != 1 {
		t.Fatalf("%+v %v, checked %d", res, err, watcher.checked)
	}
}

// A stop with a brief hold in force stops each briefed conversation under
// the same claim rule as the loop's own stop: it waits only while another
// loop's live check holds that session, and the next check stops it then.
func TestBriefStopWaitsForALiveClaimAndRetries(t *testing.T) {
	w, p, fallbacks := rejectingWorld(t, false)
	if _, err := control.Update(w.dir, func(f *control.File) {
		f.Stop, f.AwaitingUser = true, true
		f.BriefHold = []control.Brief{{Action: "handoff", Target: "claude-other"}}
	}); err != nil {
		t.Fatal(err)
	}
	c := otherLoopClaims(t, w, "thr_claude-other")
	runTick(t, w, t0)
	if p.stopped != 1 || fallbacks["claude-other"].stopped != 0 {
		t.Fatalf("primary %d, claimed fallback %d", p.stopped, fallbacks["claude-other"].stopped)
	}
	c.Release()
	runTick(t, w, t0.Add(time.Minute))
	if fallbacks["claude-other"].stopped != 1 {
		t.Fatalf("the waiting stop was not made once the claim was free: %d", fallbacks["claude-other"].stopped)
	}
	runTick(t, w, t0.Add(2*time.Minute))
	if fallbacks["claude-other"].stopped != 1 || p.stopped != 2 {
		t.Fatalf("stops repeated after the request was done: primary %d, fallback %d", p.stopped, fallbacks["claude-other"].stopped)
	}
}

// The claim directory named twice, differently, by the variable's current
// and earlier names: the check acts on nothing and fails, naming both
// variables and what to do. Choosing either would hide the other's claims.
func TestAClaimDirectoryNamedTwiceActsOnNothing(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	const earlier = "KEEPGOING_SESSION_DIR"
	t.Setenv(session.DirEnv, filepath.Join(t.TempDir(), "sessions"))
	t.Setenv(earlier, filepath.Join(t.TempDir(), "sessions"))
	opts := w.opts(t0)
	opts.SessionDir = "" // the default, which the variables name
	res, err := Tick(context.Background(), w.cfg, opts)
	if err == nil || errors.Is(err, lock.ErrHeld) || !strings.Contains(err.Error(), session.DirEnv) || !strings.Contains(err.Error(), earlier) || !strings.Contains(err.Error(), "unset one of them") {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Reason != "session claim unsafe; no action taken" || p.observed != 0 || len(p.sent) != 0 {
		t.Fatalf("acted: %+v, observed %d", res, p.observed)
	}
	if st, _ := state.Load(w.dir); st.LastCheck == nil || st.LastCheck.Outcome != "failed" {
		t.Fatalf("not recorded as a failed check: %+v", st.LastCheck)
	}
}

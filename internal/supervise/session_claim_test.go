package supervise

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
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

// A claim that cannot be taken at all fails the check without acting.
func TestAnUnusableClaimDirectoryActsOnNothing(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	file := w.sessions + "/not-a-directory"
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := w.opts(t0)
	opts.SessionDir = file
	res, err := Tick(context.Background(), w.cfg, opts)
	if err == nil || errors.Is(err, lock.ErrHeld) || res.Reason != "session claim unavailable; no action taken" || p.observed != 0 || len(p.sent) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

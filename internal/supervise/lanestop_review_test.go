package supervise

import (
	"context"
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
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// J1. A check was killed after recording a handoff brief to claude-other.
// Then a stop is asked for, and the next check cannot observe the primary.
// A plain stop and a stop with lanes must end the same way for the brief:
// claude-other is stopped once and the loop holds for the brief. A check that
// could not read the send records settles none of them.
func TestStopWithLanesKeepsAPendingBriefAsAPlainStopDoes(t *testing.T) {
	for _, lanes := range []bool{false, true} {
		w := &laneStopWorld{world: newWorld(t, true, true), others: true}
		w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
		w.tree = newLaneTree(w.primary(decide.Observation{State: decide.Active}))
		plantLanes(t, w)
		if _, err := state.Reserve(w.dir, 0, 0, state.SendIntent{At: t0, Action: "handoff", Target: "claude-other"}); err != nil {
			t.Fatal(err)
		}
		w.askStop(t, lanes, t0)
		w.tree.obsErr = fmt.Errorf("bb thread show: exit status 1")
		w.tick(t, t0.Add(time.Minute))
		if l, err := state.LoadLedger(w.dir); err != nil || l.Settled != 0 {
			t.Fatalf("lanes %v: a check that read no records settled some: %+v %v", lanes, l, err)
		}
		w.tree.obsErr = nil
		w.tick(t, t0.Add(2*time.Minute))
		w.tick(t, t0.Add(3*time.Minute))
		other := w.adapters["claude-other"]
		ctl, _ := control.Load(w.dir)
		if other == nil || other.stopped != 1 || !ctl.AwaitingUser || len(ctl.BriefHold) != 1 || ctl.BriefHold[0].Target != "claude-other" {
			t.Fatalf("lanes %v: claude-other stopped %v, hold %v %+v", lanes, other, ctl.AwaitingUser, ctl.BriefHold)
		}
		if lanes && sorted(w.tree.laneStops) != "thr_lane_a,thr_lane_b1,thr_lane_c" {
			t.Fatalf("lanes: stopped %v", w.tree.laneStops)
		}
		if !lanes && len(w.tree.laneStops) != 0 {
			t.Fatalf("plain stop stopped lanes %v", w.tree.laneStops)
		}
	}
}

// S1. Each lane stop is in sends.json, under the lane's keyed hash and the
// request, at the moment the lane is stopped.
func TestLaneStopIsRecordedBeforeItIsMade(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.askStop(t, true, t0)
	checked := 0
	laneStopHook = func(id string) {
		key, err := state.LaneKey(w.dir)
		if err != nil {
			t.Fatal(err)
		}
		l, err := state.LoadLedger(w.dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range l.Pending {
			if p.Action == laneStopAction && p.Target == state.LaneHash(key, id) && p.Key == t0.UTC().Format(time.RFC3339Nano) {
				checked++
				return
			}
		}
		t.Errorf("%s was stopped with no record of it in sends.json: %+v", id, l.Pending)
	}
	defer func() { laneStopHook = nil }()
	w.tick(t, t0.Add(time.Minute))
	if checked != 3 || len(w.tree.laneStops) != 3 {
		t.Fatalf("%d of %v checked", checked, w.tree.laneStops)
	}
}

// S3. A listing that cannot be read does not use up the request: the next
// check tries again, up to three checks, and then what is below it is
// reported as not checked. A lane's failed stop is still not retried.
func TestUnreadableListingIsTriedAgainBeforeItIsReported(t *testing.T) {
	// The root listing fails twice, then works: the lanes are stopped on the
	// third check and the one report comes then.
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.listFails[laneRoot] = 2
	w.askStop(t, true, t0)
	for i := 1; i <= 2; i++ {
		res := w.tick(t, t0.Add(time.Duration(i)*time.Minute))
		if res.LaneStop.Outcome != "incomplete" || len(w.stopReports()) != 0 || len(w.tree.laneStops) != 0 {
			t.Fatalf("check %d: %+v", i, res.LaneStop)
		}
	}
	res := w.tick(t, t0.Add(3*time.Minute))
	if res.LaneStop.Outcome != "done" || sorted(w.tree.laneStops) != "thr_lane_a,thr_lane_b1,thr_lane_c" || len(w.stopReports()) != 1 || res.LaneStop.ListingsUnread != 0 {
		t.Fatalf("%+v %v", res.LaneStop, w.tree.laneStops)
	}
	// It never works: after three checks it is reported, as not checked.
	w = newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.listFails[laneRoot] = 99
	w.askStop(t, true, t0)
	for i := 1; i <= 5; i++ {
		w.tick(t, t0.Add(time.Duration(i)*time.Minute))
	}
	st, _ := state.Load(w.dir)
	if l := st.LaneStop; l == nil || l.Outcome != "done" || l.ListingsUnread != 1 || !l.NotCheckedAtLeast || l.Failed != nil || len(w.tree.listed) != 3 {
		t.Fatalf("%+v listed %d times", l, len(w.tree.listed))
	}
	if r := w.stopReports(); len(r) != 1 || !strings.Contains(r[0].Text, "1 listing(s) could not be read in 3 checks, so nothing below them was checked") {
		t.Fatalf("%+v", r)
	}
}

// S5. When it cannot be established whether another loop supervises a
// lane, the lane is left alone and nothing below it is looked at.
func TestLaneOwnershipThatCannotBeConfirmedIsLeftAlone(t *testing.T) {
	for name, plant := range map[string]func(w *laneStopWorld){
		"a session record of another schema": func(w *laneStopWorld) {
			key := session.Identity{Kind: "bb", ID: "thr_lane_a"}.Key()
			os.WriteFile(filepath.Join(w.sessions, key+".json"), []byte(`{"schemaVersion":2,"kind":"bb","id":"thr_lane_a","supervisors":[]}`), 0o600)
		},
		"an oversized session record": func(w *laneStopWorld) {
			key := session.Identity{Kind: "bb", ID: "thr_lane_a"}.Key()
			os.WriteFile(filepath.Join(w.sessions, key+".json"), []byte(strings.Repeat(" ", 70<<10)), 0o600)
		},
	} {
		w := newLaneStopWorld(t)
		w.tree.add(laneRoot, "thr_lane_a", "active", 0)
		w.tree.add("thr_lane_a", "thr_lane_a1", "active", 0)
		plant(w)
		w.askStop(t, true, t0)
		res := w.tick(t, t0.Add(time.Minute))
		if len(w.tree.laneStops) != 0 || res.LaneStop.Failed["ownership_unconfirmed"] != 1 || strings.Contains(strings.Join(w.tree.listed, ","), "thr_lane_a") {
			t.Errorf("%s: stopped %v, listed %v, %+v", name, w.tree.laneStops, w.tree.listed, res.LaneStop)
		}
	}
	// No session directory at all: every lane's ownership is unknown.
	w := newLaneStopWorld(t)
	w.tree.add(laneRoot, "thr_lane_a", "active", 0)
	notADir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(notADir, nil, 0o600)
	w.world.sessions = notADir
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if len(w.tree.laneStops) != 0 || res.LaneStop.Failed["ownership_unconfirmed"] != 1 {
		t.Fatalf("stopped %v, %+v", w.tree.laneStops, res.LaneStop)
	}
}

// S6. The report says what is known of the supervised thread itself.
func TestLaneStopReportSaysWhatIsKnownOfTheThreadItself(t *testing.T) {
	for name, tc := range map[string]struct {
		set    func(w *laneStopWorld)
		parent string
		text   string
	}{
		"it was stopped":       {func(*laneStopWorld) {}, "stopped", "stopped primary; lanes:"},
		"it was not running":   {func(w *laneStopWorld) { w.tree.obs.State = decide.Idle }, "not_running", "primary was not running; lanes:"},
		"it could not be seen": {func(w *laneStopWorld) { w.tree.obsErr = fmt.Errorf("exit status 1") }, "not_observed", "primary could not be observed, so it is not known whether it was running; lanes:"},
		"its stop failed":      {func(w *laneStopWorld) { w.tree.stopFails = fmt.Errorf("bb thread stop: thread_host_offline") }, "stop_failed", "the stop of primary failed; lanes:"},
	} {
		w := newLaneStopWorld(t)
		plantLanes(t, w)
		tc.set(w)
		w.askStop(t, true, t0)
		res := w.tick(t, t0.Add(time.Minute))
		r := w.stopReports()
		if res.LaneStop == nil || res.LaneStop.Parent != tc.parent || len(r) != 1 || !strings.Contains(r[0].Text, tc.text) || r[0].Data["parent"] != tc.parent {
			t.Errorf("%s: %+v %+v", name, res.LaneStop, r)
		}
	}
}

// S7. A pass that runs out of time and is continued by the next check counts
// every lane it stopped once, in whichever check it stopped it.
func TestContinuedLaneStopCountsWhatTheFirstPassStopped(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.obs.State = decide.Idle
	wall := t0
	w.clock = func() time.Time { return wall }
	w.askStop(t, true, t0)
	// Out of time after the second lane stop.
	w.tree.onCall = func() {
		if len(w.tree.laneStops) >= 2 {
			wall = wall.Add(10 * time.Minute)
		}
	}
	if res := w.tick(t, t0.Add(time.Minute)); res.LaneStop.Outcome != "incomplete" || len(w.tree.laneStops) != 2 {
		t.Fatalf("%+v %v", res.LaneStop, w.tree.laneStops)
	}
	wall, w.tree.onCall = t0.Add(time.Hour), nil
	res := w.tick(t, t0.Add(2*time.Minute))
	if l := res.LaneStop; l.Outcome != "done" || l.Stopped != 3 || l.AlreadyIdle != 2 || len(w.tree.laneStops) != 3 {
		t.Fatalf("%+v %v", l, w.tree.laneStops)
	}
	if r := w.stopReports(); len(r) != 1 || !strings.Contains(r[0].Text, "lanes: 3 stopped, 2 already idle") {
		t.Fatalf("%+v", r)
	}
	// A lane whose stop failed in the first pass is not tried again.
	w = newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.obs.State = decide.Idle
	wall = t0
	w.clock = func() time.Time { return wall }
	w.tree.stopErr["thr_lane_a"] = &adapter.LaneStopError{Class: "thread_host_offline", Err: fmt.Errorf("x")}
	w.tree.onCall = func() {
		if len(w.tree.laneStops) >= 1 {
			wall = wall.Add(10 * time.Minute)
		}
	}
	w.askStop(t, true, t0)
	w.tick(t, t0.Add(time.Minute))
	wall, w.tree.onCall = t0.Add(time.Hour), nil
	res = w.tick(t, t0.Add(2*time.Minute))
	tries := 0
	for _, id := range w.tree.laneStops {
		if id == "thr_lane_a" {
			tries++
		}
	}
	if tries != 1 || res.LaneStop.Failed["thread_host_offline"] != 1 || res.LaneStop.Stopped != 2 {
		t.Fatalf("lane a tried %d times, %+v", tries, res.LaneStop)
	}
}

// S8. Run from inside a thread the stop covers, the stop makes that thread's
// stop last, after every other target.
func TestCallersOwnThreadIsStoppedLast(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.caller = "thr_lane_a"
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if got := strings.Join(w.tree.order, ","); got != "parent,thr_lane_c,thr_lane_b1,thr_lane_a" {
		t.Fatalf("order %s", got)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "it is stopped last") {
		t.Fatalf("%v", res.Warnings)
	}
	// The command runs in the supervised thread itself.
	w = newLaneStopWorld(t)
	plantLanes(t, w)
	w.caller = laneRoot
	w.askStop(t, true, t0)
	res = w.tick(t, t0.Add(time.Minute))
	if got := strings.Join(w.tree.order, ","); !strings.HasSuffix(got, ",parent") || len(w.tree.order) != 4 || res.LaneStop.Parent != "stopped" {
		t.Fatalf("order %s, %+v", got, res.LaneStop)
	}
	if r := w.stopReports(); len(r) != 1 || !strings.Contains(r[0].Text, "stopped primary") {
		t.Fatalf("%+v", r)
	}
}

// A check run to carry out a stop at once, after the stop was lifted, says
// nothing about whether checks work: it leaves the failure counts alone.
func TestLiftedStopOnlyCheckKeepsTheFailureCounts(t *testing.T) {
	w := newLaneStopWorld(t)
	st, _ := state.Load(w.dir)
	st.FailedChecks, st.FailedSince = 4, t0.Add(-time.Hour)
	state.Save(w.dir, st)
	opts := w.opts(t0)
	opts.StopOnly = true
	if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
		t.Fatal(err)
	}
	if st, _ := state.Load(w.dir); st.FailedChecks != 4 || !st.FailedSince.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("%d %v", st.FailedChecks, st.FailedSince)
	}
}

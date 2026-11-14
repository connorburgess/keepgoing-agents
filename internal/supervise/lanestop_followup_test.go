package supervise

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// 1. A lane that has not started (pending) has no session to stop, and an
// idle lane with a message queued would start it: their queued messages are
// deleted, each recorded first. A pending lane with nothing queued is
// counted as not started, to be stopped in BB.
func TestNotStartedLanesHaveTheirQueuesDeleted(t *testing.T) {
	w := newLaneStopWorld(t)
	w.tree.add(laneRoot, "thr_lane_p", "pending", 1)
	w.tree.queued["thr_lane_p"] = 1
	w.tree.add(laneRoot, "thr_lane_q", "pending", 0)
	w.tree.add(laneRoot, "thr_lane_i", "idle", 2)
	w.tree.queued["thr_lane_i"] = 2
	w.tree.add(laneRoot, "thr_lane_a", "active", 0)
	w.askStop(t, true, t0)
	recorded := 0
	laneStopHook = func(id string) {
		key, _ := state.LaneKey(w.dir)
		l, _ := state.LoadLedger(w.dir)
		for _, p := range l.Pending {
			if p.Action == laneStopAction && p.Target == state.LaneHash(key, id) {
				recorded++
				return
			}
		}
		t.Errorf("%s acted on before it was recorded", id)
	}
	defer func() { laneStopHook = nil }()
	res := w.tick(t, t0.Add(time.Minute))
	if got := strings.Join(w.tree.dequeued, ","); got != "thr_lane_p,thr_lane_q,thr_lane_i" {
		t.Fatalf("dequeued %s", got)
	}
	if got := strings.Join(w.tree.laneStops, ","); got != "thr_lane_a" || recorded != 4 {
		t.Fatalf("stopped %s, %d recorded", got, recorded)
	}
	l := res.LaneStop
	if l.Dequeued != 2 || l.NotStarted != 1 || l.Stopped != 1 || l.AlreadyIdle != 0 {
		t.Fatalf("%+v", l)
	}
	r := w.stopReports()
	if len(r) != 1 || !strings.Contains(r[0].Text, "2 not started had their queued messages deleted") || !strings.Contains(r[0].Text, "1 not started, with nothing queued to delete: BB may still run its first message, so stop them in BB") {
		t.Fatalf("%+v", r)
	}
}

// 2. Run inside the supervised thread whose own stop then fails, the report
// says the stop failed: it is sent after that stop.
func TestReportFromInsideTheThreadComesAfterItsStop(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.caller = laneRoot
	w.tree.stopFails = fmt.Errorf("bb thread stop: thread_host_offline")
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if got := strings.Join(w.tree.order, ","); !strings.HasSuffix(got, ",parent") {
		t.Fatalf("order %s", got)
	}
	r := w.stopReports()
	if res.LaneStop.Parent != "stop_failed" || len(r) != 1 || !strings.Contains(r[0].Text, "the stop of primary failed") {
		t.Fatalf("%+v %+v", res.LaneStop, r)
	}
}

// 3. A stop of the supervised thread made by a later check, after the lanes'
// report went out, sends its own short report, as a plain stop does: when
// the first check could not observe it, and when its first stop failed.
func TestALaterStopOfTheThreadIsReported(t *testing.T) {
	for name, first := range map[string]func(w *laneStopWorld){
		"not observed first": func(w *laneStopWorld) { w.tree.obsErr = fmt.Errorf("exit status 1") },
		"its stop failed":    func(w *laneStopWorld) { w.tree.stopFails = fmt.Errorf("bb thread stop: thread_host_offline") },
	} {
		w := newLaneStopWorld(t)
		plantLanes(t, w)
		first(w)
		w.askStop(t, true, t0)
		w.tick(t, t0.Add(time.Minute))
		if r := w.stopReports(); len(r) != 1 {
			t.Fatalf("%s: %+v", name, r)
		}
		w.tree.obsErr, w.tree.stopFails = nil, nil
		w.tick(t, t0.Add(2*time.Minute))
		r := w.stopReports()
		if len(r) != 2 || r[1].Text != "example-loop: stopped primary" {
			t.Fatalf("%s: %+v", name, r)
		}
		if len(w.tree.laneStops) != 3 {
			t.Fatalf("%s: lanes stopped again: %v", name, w.tree.laneStops)
		}
	}
}

// 4. Two stop --lanes requests with fifty lanes whose stops fail, and a
// primary that cannot be observed, overflow the pending records. A brief
// recorded before them is kept, and the next check that reads the records
// stops the briefed conversation and holds.
func TestManyLaneStopsDoNotPushABriefOut(t *testing.T) {
	w := &laneStopWorld{world: newWorld(t, true, true), others: true}
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	w.tree = newLaneTree(w.primary(decide.Observation{State: decide.Active}))
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("thr_lane_%02d", i)
		w.tree.add(laneRoot, id, "active", 0)
		w.tree.stopErr[id] = &adapter.LaneStopError{Class: "thread_host_offline", Err: fmt.Errorf("x")}
	}
	if _, err := state.Reserve(w.dir, 0, 0, state.SendIntent{At: t0, Action: "handoff", Target: "claude-other"}); err != nil {
		t.Fatal(err)
	}
	w.tree.obsErr = fmt.Errorf("exit status 1")
	for i := 0; i < 2; i++ {
		w.askStop(t, true, t0.Add(time.Duration(i)*10*time.Minute))
		w.tick(t, t0.Add(time.Duration(i)*10*time.Minute+time.Minute))
	}
	l, _ := state.LoadLedger(w.dir)
	if len(l.Pending) == 0 || l.Pending[0].Action != "handoff" {
		t.Fatalf("the brief was pushed out: %d pending, first %+v", len(l.Pending), l.Pending[0])
	}
	w.tree.obsErr = nil
	w.tick(t, t0.Add(30*time.Minute))
	other := w.adapters["claude-other"]
	ctl, _ := control.Load(w.dir)
	if other == nil || other.stopped != 1 || !ctl.AwaitingUser || len(ctl.BriefHold) != 1 {
		t.Fatalf("claude-other %v, hold %v %+v", other, ctl.AwaitingUser, ctl.BriefHold)
	}
}

// 5. A lane the request's first pass stopped is counted as stopped by a pass
// that goes on, whatever it is doing now: a person who restarted it meant to.
func TestALaneRestartedByAPersonIsNotStoppedAgain(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.obs.State = decide.Idle
	w.tree.listFails["thr_lane_b"] = 1 // the first pass cannot list below b
	w.askStop(t, true, t0)
	if res := w.tick(t, t0.Add(time.Minute)); res.LaneStop.Outcome != "incomplete" || res.LaneStop.IncompleteBecause != "listing" {
		t.Fatalf("%+v", res.LaneStop)
	}
	if got := sorted(w.tree.laneStops); got != "thr_lane_a,thr_lane_c" {
		t.Fatalf("stopped %s", got)
	}
	w.tree.kids[laneRoot][0].Status = "active" // a person restarted thr_lane_a
	res := w.tick(t, t0.Add(2*time.Minute))
	if got := sorted(w.tree.laneStops); got != "thr_lane_a,thr_lane_b1,thr_lane_c" {
		t.Fatalf("stopped %s: a restarted lane was stopped again", got)
	}
	if res.LaneStop.Stopped != 3 || res.LaneStop.Outcome != "done" {
		t.Fatalf("%+v", res.LaneStop)
	}
}

// The default session directory cannot be used at all: every lane's
// ownership is unknown, and no lane is stopped.
func TestNoSessionDirectoryLeavesEveryLaneAlone(t *testing.T) {
	w := newLaneStopWorld(t)
	w.tree.add(laneRoot, "thr_lane_a", "active", 0)
	noStateHome(t)
	w.askStop(t, true, t0)
	opts := w.opts(t0.Add(time.Minute))
	opts.SessionDir = ""
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return w.tree, nil }
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil && !res.Recorded {
		t.Fatal(err)
	}
	if len(w.tree.laneStops) != 0 || res.LaneStop == nil || res.LaneStop.Failed["ownership_unconfirmed"] != 1 {
		t.Fatalf("stopped %v, %+v", w.tree.laneStops, res.LaneStop)
	}
}

// With a brief unsettled and the command running inside the supervised
// thread, that thread is stopped after the briefed conversation and the
// lanes.
func TestCallerLastWithABriefUnsettled(t *testing.T) {
	w := &laneStopWorld{world: newWorld(t, true, true), others: true, caller: laneRoot}
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	w.tree = newLaneTree(w.primary(decide.Observation{State: decide.Active}))
	plantLanes(t, w)
	if _, err := state.Reserve(w.dir, 0, 0, state.SendIntent{At: t0, Action: "handoff", Target: "claude-other"}); err != nil {
		t.Fatal(err)
	}
	w.askStop(t, true, t0)
	w.tick(t, t0.Add(time.Minute))
	if got := strings.Join(w.tree.order, ","); !strings.HasSuffix(got, ",parent") || strings.Count(got, "thr_lane") != 3 {
		t.Fatalf("order %s", got)
	}
	if other := w.adapters["claude-other"]; other == nil || other.stopped != 1 {
		t.Fatalf("claude-other %v", other)
	}
}

// The caller's own lane waits for the walk: a pass that a listing leaves
// incomplete does not stop it; the pass that finishes does, last.
func TestCallersLaneWaitsForTheWalkToFinish(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.obs.State = decide.Idle
	w.caller = "thr_lane_a"
	w.tree.listFails["thr_lane_b"] = 1
	w.askStop(t, true, t0)
	w.tick(t, t0.Add(time.Minute))
	for _, id := range w.tree.laneStops {
		if id == "thr_lane_a" {
			t.Fatalf("the caller's lane was stopped by an incomplete pass: %v", w.tree.laneStops)
		}
	}
	w.tick(t, t0.Add(2*time.Minute))
	if got := w.tree.laneStops; got[len(got)-1] != "thr_lane_a" {
		t.Fatalf("stopped %v", got)
	}
}

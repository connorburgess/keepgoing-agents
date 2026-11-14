package supervise

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const laneRoot = "thr_primary"

// laneTree is a fake provider with lanes: the supervised thread's
// descendants, by parent. Every call is recorded, so a test can assert that
// a lane was sent nothing but a stop.
type laneTree struct {
	*fakeAdapter
	kids      map[string][]adapter.LaneInfo
	stopErr   map[string]error
	pending   map[string]bool // a provider retry waits in the lane
	noCancel  bool            // the provider cannot cancel one
	listed    []string
	laneStops []string
	order     []string       // the supervised thread's stop ("parent") and each lane's, in order
	stopFails error          // the supervised thread's own stop fails with this
	listFails map[string]int // a listing of this parent fails this many more times
	queued    map[string]int // messages queued in a lane that has not started
	dequeued  []string
	cancels   []string
	onCall    func()
}

func newLaneTree(f *fakeAdapter) *laneTree {
	return &laneTree{fakeAdapter: f, kids: map[string][]adapter.LaneInfo{}, stopErr: map[string]error{}, pending: map[string]bool{}, listFails: map[string]int{}, queued: map[string]int{}}
}

func (l *laneTree) add(parent, id, status string, queued int) {
	l.kids[parent] = append(l.kids[parent], adapter.LaneInfo{ID: id, Verified: true, Status: status, Queued: queued})
}

func (l *laneTree) LaneRoot() string { return laneRoot }

func (l *laneTree) Stop(ctx context.Context) error {
	l.order = append(l.order, "parent")
	if l.stopFails != nil {
		return l.stopFails
	}
	return l.fakeAdapter.Stop(ctx)
}

func (l *laneTree) LaneChildren(_ context.Context, parent string, max int) ([]adapter.LaneInfo, int, error) {
	l.listed = append(l.listed, parent)
	if l.onCall != nil {
		l.onCall()
	}
	if l.listFails[parent] > 0 {
		l.listFails[parent]--
		return nil, 0, &adapter.LaneStopError{Class: "timeout", Err: fmt.Errorf("bb thread list: stopped at its time limit")}
	}
	all := l.kids[parent]
	if len(all) > max {
		return append([]adapter.LaneInfo(nil), all[:max]...), len(all) - max, nil
	}
	return append([]adapter.LaneInfo(nil), all...), 0, nil
}

func (l *laneTree) StopLane(_ context.Context, id string) error {
	l.laneStops = append(l.laneStops, id)
	l.order = append(l.order, id)
	if l.onCall != nil {
		l.onCall()
	}
	if err := l.stopErr[id]; err != nil {
		return err
	}
	// The lane is no longer running.
	for p, kids := range l.kids {
		for i := range kids {
			if kids[i].ID == id {
				l.kids[p][i].Status, l.kids[p][i].Queued = "idle", 0
			}
		}
	}
	return nil
}

func (l *laneTree) DeleteLaneQueue(_ context.Context, id string) (int, error) {
	l.dequeued = append(l.dequeued, id)
	l.order = append(l.order, "dequeue:"+id)
	n := l.queued[id]
	delete(l.queued, id)
	for p, kids := range l.kids {
		for i := range kids {
			if kids[i].ID == id && n > 0 {
				l.kids[p][i].Status, l.kids[p][i].Queued = "idle", 0
			}
		}
	}
	return n, nil
}

func (l *laneTree) CancelLaneRetry(_ context.Context, id string) (bool, error) {
	l.cancels = append(l.cancels, id)
	if l.noCancel {
		return false, adapter.ErrUnsupported{Op: "provider retry cancel"}
	}
	ok := l.pending[id]
	delete(l.pending, id)
	return ok, nil
}

type laneStopWorld struct {
	*world
	tree   *laneTree
	clock  func() time.Time
	dryRun bool
	caller string
	others bool // other targets get their own fake adapters
}

// newLaneStopWorld is a loop whose supervised thread is working and has lanes.
func newLaneStopWorld(t *testing.T) *laneStopWorld {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	return &laneStopWorld{world: w, tree: newLaneTree(w.primary(decide.Observation{State: decide.Active}))}
}

// askStop saves a stop, with lanes or not, as the commands do.
func (w *laneStopWorld) askStop(t *testing.T, lanes bool, at time.Time) {
	t.Helper()
	if _, err := control.Update(w.dir, func(f *control.File) {
		f.Stop = true
		if lanes {
			a := at.UTC()
			f.StopLanesAt = &a
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func (w *laneStopWorld) tick(t *testing.T, at time.Time) Result {
	t.Helper()
	opts := w.opts(at)
	inner := opts.NewAdapter
	opts.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
		if w.others && target != state.Primary {
			return inner(a, dir, target)
		}
		return w.tree, nil
	}
	opts.DryRun, opts.CallerThread = w.dryRun, w.caller
	if w.clock != nil {
		opts.Clock = w.clock
	}
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil && !res.Recorded {
		t.Fatalf("tick: %v", err)
	}
	return res
}

func (w *laneStopWorld) stopReports() []report.Event {
	var out []report.Event
	for _, e := range w.reports {
		if e.Type == "stop" {
			out = append(out, e)
		}
	}
	return out
}

// everythingKept is all keepgoing wrote for the loop and reported.
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
	b, _ := json.Marshal(w.reports)
	all.Write(b)
	return all.String()
}

func sorted(s []string) string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return strings.Join(c, ",")
}

// otherLoopSupervises records, as #195's session records do, that another
// loop supervises id.
func otherLoopSupervises(t *testing.T, w *world, id string) {
	t.Helper()
	other := t.TempDir()
	if err := session.Note(w.sessions, session.Identity{Kind: "bb", ID: id}, session.Supervisor{Loop: "other-loop", Config: filepath.Join(other, "keepgoing.yaml"), StateDir: filepath.Join(other, "state"), SeenAt: t0}, time.Second); err != nil {
		t.Fatal(err)
	}
}

// The lanes of a supervised thread, three levels deep:
//
//	thr_primary (working)
//	├── thr_lane_a      active                        stopped
//	├── thr_lane_b      idle                          left alone
//	│   └── thr_lane_b1 active                        stopped
//	├── thr_lane_c      error, provider retry queued   retry cancelled, stopped
//	├── thr_lane_d      error, nothing queued          left alone
//	├── thr_lane_e      active, another loop's         left to that loop
//	│   └── thr_lane_e1 active                        not looked at
//	├── thr_lane_f      not verified                  failed (unverified)
//	└── thr_lane_g      a status this build cannot read  failed (unknown_status)
func plantLanes(t *testing.T, w *laneStopWorld) {
	l := w.tree
	l.add(laneRoot, "thr_lane_a", "active", 0)
	l.add(laneRoot, "thr_lane_b", "idle", 0)
	l.add("thr_lane_b", "thr_lane_b1", "active", 0)
	l.add(laneRoot, "thr_lane_c", "error", 1)
	l.pending["thr_lane_c"] = true
	l.add(laneRoot, "thr_lane_d", "error", 0)
	l.add(laneRoot, "thr_lane_e", "active", 0)
	l.add("thr_lane_e", "thr_lane_e1", "active", 0)
	otherLoopSupervises(t, w.world, "thr_lane_e")
	l.kids[laneRoot] = append(l.kids[laneRoot], adapter.LaneInfo{ID: "thr_lane_f", Status: "active"})
	l.add(laneRoot, "thr_lane_g", "hibernating", 0)
}

func TestStopWithLanesStopsTheRunningLanesOnce(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if res.Action != "stop" || w.tree.stopped != 1 {
		t.Fatalf("the supervised thread was not stopped: %+v", res)
	}
	if got := sorted(w.tree.laneStops); got != "thr_lane_a,thr_lane_b1,thr_lane_c" {
		t.Fatalf("stopped %s", got)
	}
	if got := strings.Join(w.tree.cancels, ","); got != "thr_lane_c" {
		t.Fatalf("cancelled %s", got)
	}
	// Never a message to anyone: the only sends are stops.
	if len(w.tree.sent) != 0 {
		t.Fatalf("sent %q", w.tree.sent)
	}
	for _, p := range w.tree.listed {
		if p == "thr_lane_e" || p == "thr_lane_f" {
			t.Fatalf("looked below %s", p)
		}
	}
	l := *res.LaneStop
	// The check's output has counts only; the lanes are kept in state.
	if st, _ := state.Load(w.dir); len(l.StoppedLanes) != 0 || len(st.LaneStop.StoppedLanes) != 3 {
		t.Fatalf("%+v", l)
	}
	want := state.LaneStop{For: t0, At: t0.Add(time.Minute), Outcome: "done", Parent: "stopped", Stopped: 3, AlreadyIdle: 2, OtherLoop: 1,
		Failed: map[string]int{"unverified": 1, "unknown_status": 1}, RetriesCancelled: 1, Reported: true}
	if fmt.Sprintf("%+v", l) != fmt.Sprintf("%+v", want) {
		t.Fatalf("%+v\nwant %+v", l, want)
	}
	// One stop report, for the thread and its lanes together, by count.
	reports := w.stopReports()
	if len(reports) != 1 {
		t.Fatalf("%d stop reports: %s", len(reports), w.reportTypes())
	}
	e := reports[0]
	lanes, _ := e.Data["lanes"].(map[string]any)
	if e.Data["parent"] != "stopped" || lanes["stopped"] != 3 || lanes["alreadyIdle"] != 2 || lanes["otherLoop"] != 1 || lanes["notChecked"] != 0 {
		t.Fatalf("%+v", e.Data)
	}
	if !strings.Contains(e.Text, "stopped primary; lanes: 3 stopped, 2 already idle, 1 left to another loop, 2 failed, 0 not checked") || strings.Contains(e.Text, "thr_lane") {
		t.Fatalf("%q", e.Text)
	}
	// What is kept names no lane.
	if kept := everythingKept(t, w.world); strings.Contains(kept, "thr_lane") {
		t.Fatal("a lane ID was kept")
	}
	// The request is done: later checks stop nothing more and report nothing.
	for i := 2; i < 5; i++ {
		w.tick(t, t0.Add(time.Duration(i)*time.Minute))
	}
	if len(w.tree.laneStops) != 3 || len(w.stopReports()) != 1 || w.tree.stopped != 1 {
		t.Fatalf("stops %v, reports %d", w.tree.laneStops, len(w.stopReports()))
	}
	// Asking again is a new request: the lanes are looked at again, and those
	// still running are stopped.
	w.tree.kids[laneRoot][0].Status = "active"
	w.askStop(t, true, t0.Add(10*time.Minute))
	w.tick(t, t0.Add(11*time.Minute))
	if len(w.tree.laneStops) != 4 || len(w.stopReports()) != 2 {
		t.Fatalf("stops %v, reports %d", w.tree.laneStops, len(w.stopReports()))
	}
}

// A plain stop is unchanged: no lane is looked at, and its report is the
// ordinary one.
func TestPlainStopLeavesTheLanesAlone(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.askStop(t, false, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if res.Action != "stop" || len(w.tree.listed)+len(w.tree.laneStops)+len(w.tree.cancels) != 0 || res.LaneStop != nil {
		t.Fatalf("%+v listed %v", res, w.tree.listed)
	}
	if r := w.stopReports(); len(r) != 1 || r[0].Text != "example-loop: stopped primary" || r[0].Data != nil {
		t.Fatalf("%+v", r)
	}
}

// Stop wins: the lanes are stopped whether the loop is paused, held, done or
// switched off, and whether or not the supervised thread is running. A dry
// run stops nothing.
func TestStopWithLanesWinsOverHolds(t *testing.T) {
	for name, set := range map[string]func(w *laneStopWorld){
		"paused":       func(w *laneStopWorld) { control.Update(w.dir, func(f *control.File) { f.Paused = true }) },
		"held":         func(w *laneStopWorld) { control.Update(w.dir, func(f *control.File) { f.AwaitingUser = true }) },
		"completed":    func(w *laneStopWorld) { control.Update(w.dir, func(f *control.File) { f.Completed = true }) },
		"disabled":     func(w *laneStopWorld) { control.Update(w.dir, func(f *control.File) { f.Enabled = false }) },
		"thread idle":  func(w *laneStopWorld) { w.tree.obs.State = decide.Idle },
		"thread error": func(w *laneStopWorld) { w.tree.obs.State, w.tree.obs.ErrorClass = decide.Error, decide.ErrNetwork },
	} {
		w := newLaneStopWorld(t)
		plantLanes(t, w)
		set(w)
		w.askStop(t, true, t0)
		res := w.tick(t, t0.Add(time.Minute))
		if got := sorted(w.tree.laneStops); got != "thr_lane_a,thr_lane_b1,thr_lane_c" || len(w.tree.sent) != 0 {
			t.Errorf("%s: stopped %s, sent %q", name, got, w.tree.sent)
		}
		if r := w.stopReports(); len(r) != 1 || res.LaneStop == nil || res.LaneStop.Outcome != "done" {
			t.Errorf("%s: %d reports, %+v", name, len(r), res.LaneStop)
		}
		if want := map[bool]string{true: "stopped", false: "not_running"}[w.tree.obs.State == decide.Active]; res.LaneStop.Parent != want {
			t.Errorf("%s: parent %q", name, res.LaneStop.Parent)
		}
	}
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.dryRun = true
	w.askStop(t, true, t0)
	if res := w.tick(t, t0.Add(time.Minute)); len(w.tree.laneStops)+len(w.tree.listed) != 0 || res.LaneStop == nil || res.LaneStop.Outcome != "dry_run" {
		t.Fatalf("dry run: %+v %v", res.LaneStop, w.tree.laneStops)
	}
}

// A check killed in the middle of the stop. Its records say which lane stops
// it made or was about to make; the next check looks at each of those lanes
// again: one that is no longer running was stopped and is not stopped again,
// one still running is stopped now. A report it recorded is not sent again.
func TestLaneStopRecoversAfterACheckKilledMidStop(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.obs.State = decide.Idle // nothing to stop on the thread itself
	w.askStop(t, true, t0)
	key, err := state.LaneKey(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	request := t0.UTC().Format(time.RFC3339Nano)
	// The killed check recorded stops of a (made: it is idle now) and of b1
	// (not made: still running), and its report, and saved nothing else.
	w.tree.kids[laneRoot][0].Status = "idle"
	for _, id := range []string{"thr_lane_a", "thr_lane_b1"} {
		if _, err := state.Reserve(w.dir, 0, 0, state.SendIntent{At: t0, Action: laneStopAction, Target: state.LaneHash(key, id), Key: request}); err != nil {
			t.Fatal(err)
		}
	}
	res := w.tick(t, t0.Add(time.Minute))
	if got := sorted(w.tree.laneStops); got != "thr_lane_b1,thr_lane_c" {
		t.Fatalf("stopped %s: a was stopped twice or b1 was lost", got)
	}
	if l := res.LaneStop; l.Stopped != 3 || l.AlreadyIdle != 2 {
		t.Fatalf("%+v", l)
	}
	if len(w.stopReports()) != 1 {
		t.Fatal(w.reportTypes())
	}
	// Recorded report: the same request killed after recording its report.
	w2 := newLaneStopWorld(t)
	plantLanes(t, w2)
	w2.askStop(t, true, t0)
	if _, err := state.Reserve(w2.dir, 0, 0, state.SendIntent{At: t0, Action: "report:" + laneStopReport, Target: state.Primary, Key: request}); err != nil {
		t.Fatal(err)
	}
	w2.tick(t, t0.Add(time.Minute))
	if len(w2.stopReports()) != 0 || len(w2.tree.laneStops) != 3 {
		t.Fatalf("reports %s, stops %v", w2.reportTypes(), w2.tree.laneStops)
	}
	// Records of an earlier request say nothing about this one.
	w3 := newLaneStopWorld(t)
	plantLanes(t, w3)
	w3.askStop(t, true, t0)
	key3, _ := state.LaneKey(w3.dir)
	state.Reserve(w3.dir, 0, 0, state.SendIntent{At: t0, Action: laneStopAction, Target: state.LaneHash(key3, "thr_lane_b"), Key: t0.Add(-time.Hour).Format(time.RFC3339Nano)})
	if res := w3.tick(t, t0.Add(time.Minute)); res.LaneStop.Stopped != 3 || res.LaneStop.AlreadyIdle != 2 {
		t.Fatalf("%+v", res.LaneStop)
	}
}

// Past the bounds nothing is stopped and what was not looked at is counted:
// at most 50 threads, three levels deep.
func TestLaneStopIsBoundedAndCountsWhatItDidNotCheck(t *testing.T) {
	// Deep: a chain of five, all running. Three are stopped; the fourth is
	// listed and counted; the fifth, below it, is never listed.
	w := newLaneStopWorld(t)
	parent := laneRoot
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("thr_deep_%d", i)
		w.tree.add(parent, id, "active", 0)
		parent = id
	}
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if got := strings.Join(w.tree.laneStops, ","); got != "thr_deep_1,thr_deep_2,thr_deep_3" {
		t.Fatalf("stopped %s", got)
	}
	if l := res.LaneStop; l.Stopped != 3 || l.NotChecked != 1 || !l.NotCheckedAtLeast || l.Outcome != "done" {
		t.Fatalf("%+v", l)
	}
	if strings.Contains(strings.Join(w.tree.listed, ","), "thr_deep_4") {
		t.Fatal("listed below the depth bound")
	}
	if r := w.stopReports(); len(r) != 1 || !strings.Contains(r[0].Text, "at least 1 not checked (past 3 levels or 50 threads)") {
		t.Fatalf("%+v", r)
	}
	// Wide: sixty running lanes. Fifty are stopped, ten counted.
	w = newLaneStopWorld(t)
	for i := 0; i < 60; i++ {
		w.tree.add(laneRoot, fmt.Sprintf("thr_wide_%02d", i), "active", 0)
	}
	w.askStop(t, true, t0)
	res = w.tick(t, t0.Add(time.Minute))
	if l := res.LaneStop; len(w.tree.laneStops) != 50 || l.Stopped != 50 || l.NotChecked != 10 || !l.NotCheckedAtLeast {
		t.Fatalf("%d stopped, %+v", len(w.tree.laneStops), l)
	}
}

// Another loop's lane is left to that loop's own stop, and so is everything
// below it. A lane whose session another loop's check holds right now is
// another loop's too.
func TestAnotherLoopsLaneIsSkipped(t *testing.T) {
	w := newLaneStopWorld(t)
	w.tree.add(laneRoot, "thr_lane_e", "active", 0)
	w.tree.add("thr_lane_e", "thr_lane_e1", "active", 0)
	w.tree.add(laneRoot, "thr_lane_h", "active", 0)
	otherLoopSupervises(t, w.world, "thr_lane_e")
	claim, err := session.Take(w.sessions, session.Identity{Kind: "bb", ID: "thr_lane_h"}, session.Holder{Loop: "other-loop", PID: 1, Since: t0, Until: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Release()
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if len(w.tree.laneStops) != 0 || res.LaneStop.OtherLoop != 2 || res.LaneStop.Stopped != 0 {
		t.Fatalf("stopped %v, %+v", w.tree.laneStops, res.LaneStop)
	}
	for _, p := range w.tree.listed {
		if p != laneRoot {
			t.Fatalf("looked below another loop's lane %s", p)
		}
	}
}

// An adapter without lanes: the stop goes ahead and says lanes do not apply.
func TestLaneStopIsNotApplicableWithoutLanes(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	p := w.primary(decide.Observation{State: decide.Active})
	at := t0
	control.Update(w.dir, func(f *control.File) { f.Stop, f.StopLanesAt = true, &at })
	res := runTick(t, w, t0.Add(time.Minute))
	if res.Action != "stop" || p.stopped != 1 || res.LaneStop == nil || res.LaneStop.Outcome != "not_applicable" {
		t.Fatalf("%+v %+v", res, res.LaneStop)
	}
	var stops []report.Event
	for _, e := range w.reports {
		if e.Type == "stop" {
			stops = append(stops, e)
		}
	}
	if len(stops) != 1 || !strings.Contains(stops[0].Text, "lanes: not applicable") {
		t.Fatalf("%+v", stops)
	}
}

// A lane stop that fails is counted by its class and reported; the others go
// ahead. A provider that cannot cancel a queued retry says so.
func TestLaneStopFailuresAreCountedByClass(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.noCancel = true
	w.tree.stopErr["thr_lane_a"] = &adapter.LaneStopError{Class: "thread_host_offline", Err: fmt.Errorf("x")}
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	l := res.LaneStop
	if l.Stopped != 2 || l.Failed["thread_host_offline"] != 1 || l.QueuedNotCancelled != 1 || l.RetriesCancelled != 0 {
		t.Fatalf("%+v", l)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "queued retries are not cancelled") {
		t.Fatalf("%v", res.Warnings)
	}
	if r := w.stopReports(); len(r) != 1 || !strings.Contains(r[0].Text, "thread_host_offline 1") || !strings.Contains(r[0].Text, "1 queued retry(s) were not cancelled") {
		t.Fatalf("%+v", r)
	}
}

// With too little of the check's time left the walk stops and the next check
// goes on with the same request. The report waits for the end.
func TestLaneStopGoesOnInTheNextCheckWhenOutOfTime(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.obs.State = decide.Idle
	wall := t0
	w.clock = func() time.Time { return wall }
	w.tree.onCall = func() { wall = wall.Add(70 * time.Second) }
	w.askStop(t, true, t0)
	res := w.tick(t, t0.Add(time.Minute))
	if res.LaneStop.Outcome != "incomplete" || len(w.stopReports()) != 0 {
		t.Fatalf("%+v reports %d", res.LaneStop, len(w.stopReports()))
	}
	first := len(w.tree.laneStops)
	wall, w.tree.onCall = t0.Add(time.Hour), nil
	res = w.tick(t, t0.Add(2*time.Minute))
	if res.LaneStop.Outcome != "done" || len(w.stopReports()) != 1 || sorted(w.tree.laneStops) != "thr_lane_a,thr_lane_b1,thr_lane_c" || first == 3 {
		t.Fatalf("%+v stops %v (first check %d)", res.LaneStop, w.tree.laneStops, first)
	}
}

// A check run by stop --now does nothing but the stop: once a resume has
// lifted it, nothing at all.
func TestStopOnlyCheckDoesNothingWithoutAStop(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.tree.obs.State = decide.Idle
	opts := w.opts(t0)
	opts.StopOnly = true
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return w.tree, nil }
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Action != "none" || res.Reason != "stop no longer requested; nothing done" || len(w.tree.sent)+len(w.tree.laneStops)+len(w.tree.listed) != 0 {
		t.Fatalf("%+v %v sent %v", res, err, w.tree.sent)
	}
}

// A plain stop already carried out, then a stop with lanes: the session is
// not stopped again, and the report says it was stopped earlier.
func TestLanesAskedForAfterAPlainStop(t *testing.T) {
	w := newLaneStopWorld(t)
	plantLanes(t, w)
	w.askStop(t, false, t0)
	w.tick(t, t0.Add(time.Minute))
	w.askStop(t, true, t0.Add(2*time.Minute))
	res := w.tick(t, t0.Add(3*time.Minute))
	if w.tree.stopped != 1 || res.LaneStop.Stopped != 3 || res.LaneStop.Parent != "stopped_earlier" {
		t.Fatalf("session stops %d, %+v", w.tree.stopped, res.LaneStop)
	}
	r := w.stopReports()
	if len(r) != 2 || !strings.Contains(r[1].Text, "primary was stopped by an earlier check for this stop; lanes: 3 stopped") {
		t.Fatalf("%+v", r)
	}
}

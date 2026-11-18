package supervise

import (
	"context"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/prompt"
)

type assignmentScanAdapter struct {
	fakeAdapter
	fresh  *decide.DirectChildren
	scans  int
	onScan func()
}

func (a *assignmentScanAdapter) AssignmentChildren(context.Context) *decide.DirectChildren {
	a.scans++
	if a.onScan != nil {
		a.onScan()
	}
	return a.fresh
}

func TestAssignmentRevalidationChangesAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*decide.DirectChildren)
		budget bool
	}{
		{"same", func(*decide.DirectChildren) {}, true},
		{"added", func(s *decide.DirectChildren) { s.States["thr_example_new"] = decide.ChildState{Execution: "idle"} }, true},
		{"removed", func(s *decide.DirectChildren) { delete(s.States, "thr_example_docs") }, true},
		{"state", func(s *decide.DirectChildren) { s.States["thr_example_docs"] = decide.ChildState{Execution: "active"} }, true},
		{"interaction", func(s *decide.DirectChildren) {
			s.States["thr_example_docs"] = decide.ChildState{Execution: "idle", PendingInteraction: true}
		}, true},
		{"partial", func(s *decide.DirectChildren) { s.Complete = false }, true},
		{"no-budget", func(*decide.DirectChildren) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			initial := &decide.DirectChildren{Complete: true, States: map[string]decide.ChildState{"thr_example_docs": {Execution: "idle"}}}
			fresh := &decide.DirectChildren{Complete: true, States: map[string]decide.ChildState{"thr_example_docs": {Execution: "idle"}}}
			tc.change(fresh)
			r := &control.AssignmentRecord{Revisions: []control.AssignmentRevision{{Inventory: control.AssignmentInventory{Lanes: []control.AssignmentLane{{Thread: "thr_example_docs"}}}, Rows: []control.AssignmentRow{{Spec: control.AssignmentSpec{ID: "docs", Lane: "thr_example_docs"}}}, Wake: &control.AssignmentWake{Selections: []control.AssignmentWakeSelection{{Assignment: "docs"}}}}}}
			check := tick{ctx: context.Background(), cfg: w.cfg, loop: w.cfg.Loop, opts: Options{Clock: func() time.Time { return t0 }}, res: &Result{}, deadline: t0.Add(reserveSend + reserveReports + 10*time.Second), reserve: reserveSend + reserveReports, assignment: &assignmentDelivery{record: r, count: 1}}
			if !tc.budget {
				check.deadline = t0.Add(check.reserve + time.Second)
			}
			check.decisionInput.Obs.DirectChildren = initial
			a := &assignmentScanAdapter{fresh: fresh}
			_, allowed := check.prepareAssignment(a, decide.Decision{IdleWake: true, AssignmentWake: true}, prompt.Message{Text: "task"})
			if allowed != (tc.name == "same") {
				t.Fatal(allowed, check.res)
			}
			if !tc.budget && a.scans != 0 {
				t.Fatal("borrowed mandatory reserve")
			}
			if tc.name != "same" && check.assignment.disposition != "" {
				t.Fatal("withheld exception consumed grant")
			}
		})
	}
}

func TestAssignmentOrdinaryWakeBudgetDoesNotBlockOrClaimFreshness(t *testing.T) {
	w := newWorld(t, true, false)
	check := tick{ctx: context.Background(), cfg: w.cfg, loop: w.cfg.Loop, opts: Options{Clock: func() time.Time { return t0 }}, res: &Result{}, deadline: t0, reserve: reserveSend + reserveReports, assignment: &assignmentDelivery{count: 2}}
	a := &assignmentScanAdapter{}
	m, allowed := check.prepareAssignment(a, decide.Decision{IdleWake: true}, prompt.Message{Text: "original task"})
	if !allowed || m.Text != "original task" || m.NoticeOnly || a.scans != 0 || check.assignment.disposition != "superseded_by_ordinary_wake_attempt" {
		t.Fatal(m, allowed, a.scans, check.assignment)
	}
}

func TestAssignmentRevalidationRespectsNewPause(t *testing.T) {
	w := newWorld(t, true, false)
	check := tick{ctx: context.Background(), cfg: w.cfg, loop: w.cfg.Loop, opts: Options{Clock: func() time.Time { return t0 }}, res: &Result{}, deadline: t0.Add(reserveSend + reserveReports + 10*time.Second), reserve: reserveSend + reserveReports, assignment: &assignmentDelivery{count: 1}}
	a := &assignmentScanAdapter{onScan: func() {
		if _, err := control.Update(w.dir, func(c *control.File) { c.Paused = true }); err != nil {
			t.Fatal(err)
		}
	}}
	if _, allowed := check.prepareAssignment(a, decide.Decision{IdleWake: true, AssignmentWake: true}, prompt.Message{}); allowed {
		t.Fatal("pause during scan bypassed")
	}
	if check.assignment.disposition != "" {
		t.Fatal("new pause consumed grant")
	}
}

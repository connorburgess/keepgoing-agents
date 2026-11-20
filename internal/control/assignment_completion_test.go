package control

import (
	"testing"
	"time"
)

func TestAssignmentLifecycleCompletionLocalEvidence(t *testing.T) {
	b := AssignmentBinding{Loop: "example", Goal: "goal_example", GoalRevision: 1}
	complete := AssignmentRow{Spec: AssignmentSpec{ID: "docs", Lane: "thr_example_docs"}, HandoffRevision: 1, Acknowledged: 1, Handoff: &AssignmentHandoff{Outcome: "complete"}}
	for _, tc := range []struct {
		name   string
		change func(*AssignmentRevision)
		want   bool
	}{
		{"complete present", func(x *AssignmentRevision) {}, true},
		{"complete historical absent", func(x *AssignmentRevision) { x.Inventory.Lanes = []AssignmentLane{} }, true},
		{"unknown absent", func(x *AssignmentRevision) { x.Inventory.Lanes = []AssignmentLane{}; x.Rows[0].Handoff = nil }, false},
		{"unfinished absent", func(x *AssignmentRevision) {
			x.Inventory.Lanes = []AssignmentLane{}
			x.Rows[0].Handoff.Outcome = "unfinished"
		}, false},
		{"superseded", func(x *AssignmentRevision) { x.Rows[0].Handoff.Outcome = "superseded" }, false},
		{"waiting person", func(x *AssignmentRevision) {
			x.Rows[0].Handoff.Outcome = "waiting"
			x.Rows[0].Handoff.Blocker = "person"
		}, false},
		{"unacknowledged", func(x *AssignmentRevision) { x.Rows[0].Acknowledged = 0 }, false},
		{"partial", func(x *AssignmentRevision) { x.Inventory.Coverage = "partial" }, false},
		{"wrong goal", func(x *AssignmentRevision) { x.Binding.GoalRevision++ }, false},
		{"active", func(x *AssignmentRevision) { x.Inventory.Lanes[0].Execution = "active" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := complete
			h := *complete.Handoff
			row.Handoff = &h
			x := AssignmentRevision{Revision: 1, Binding: b, Rows: []AssignmentRow{row}, Inventory: AssignmentInventory{ObservedAt: time.Now(), Coverage: "observed_direct_children", Lanes: []AssignmentLane{{Thread: row.Spec.Lane, Execution: "idle"}}}}
			tc.change(&x)
			r := &AssignmentRecord{ID: "example", Revisions: []AssignmentRevision{x}}
			if got := CheckAssignmentCompletion(r, b, "example", 1) == nil; got != tc.want {
				t.Fatalf("accepted=%t want=%t", got, tc.want)
			}
			if CheckAssignmentCompletion(r, b, "stale", 1) == nil || CheckAssignmentCompletion(r, b, "example", 2) == nil {
				t.Fatal("stale expected identity accepted")
			}
		})
	}
}

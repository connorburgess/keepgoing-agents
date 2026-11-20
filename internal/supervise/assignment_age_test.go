package supervise

import (
	"bytes"
	"context"
	"fmt"
	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAssignmentObservationTimeCountsTowardGrantAge(t *testing.T) {
	w := newWorld(t, true, false)
	dir, _ := filepath.EvalSymlinks(w.dir)
	path := filepath.Join(dir, "example.yaml")
	body := fmt.Sprintf("version: 1\nloop:\n  name: example-loop\n  state_dir: %s\n  adapter: bb\n  bb: {thread: thr_primary}\n  prompt: example\n  assignment_lifecycle: true\n  assignment_coordination: {enabled: true, max_evidence_age: 1m}\n", dir)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	w.cfg = cfg
	owner := "thr_example_owner"
	if err := control.Save(dir, control.File{Enabled: true, Ownership: &control.Ownership{ThreadID: owner, Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	_, err = control.WriteGoal(dir, "example-loop", owner, control.GoalSpec{Title: "Example", ObjectiveRefs: []string{"objective"}, AcceptanceRefs: []string{"acceptance"}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := control.Load(dir)
	b, err := control.AssignmentBindingFor(dir, path, "example-loop", "thr_primary", c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r, err := control.WriteAssignments(dir, owner, b, "", 0, func(x *control.AssignmentRevision) error {
		x.Inventory = control.AssignmentInventory{ObservedAt: now, Coverage: "observed_direct_children", Lanes: []control.AssignmentLane{{Thread: "thr_example_docs", Execution: "idle"}, {Thread: "thr_example_ci", Execution: "active"}}}
		x.Rows = []control.AssignmentRow{}
		for _, name := range []string{"docs", "ci"} {
			h := &control.AssignmentHandoff{Assignment: name, AssignmentRevision: 1, DeclaredBy: "thr_example_" + name, DeclaredAt: now, Outcome: "unfinished", Availability: "available", Blocker: "none", EvidenceRefs: []string{"evidence"}, RemainingRefs: []string{"remaining"}, NextOwner: "coordinator"}
			x.Rows = append(x.Rows, control.AssignmentRow{Spec: control.AssignmentSpec{ID: name, Lane: h.DeclaredBy, ScopeRefs: []string{"scope"}, AuthorizationRefs: []string{"auth"}}, Revision: 1, Handoff: h, HandoffRevision: 1, Acknowledged: 1})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = control.WriteAssignments(dir, owner, b, r.ID, 1, func(x *control.AssignmentRevision) error {
		return control.DeclareAssignmentWake(x, b, []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}, time.Now().UTC(), time.Minute)
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC()
	start := clock
	children := &decide.DirectChildren{Complete: true, States: map[string]decide.ChildState{"thr_example_docs": {Execution: "idle"}, "thr_example_ci": {Execution: "active"}}}
	a := &assignmentScanAdapter{fakeAdapter: fakeAdapter{obs: decide.Observation{State: decide.Idle, ActiveChildren: 1, DirectChildren: children}, convKnown: true, convKey: "thr_primary"}, fresh: children}
	a.onObserve = func() { clock = clock.Add(70 * time.Second) }
	opts := w.opts(start)
	opts.CallerThread = owner
	opts.Clock = func() time.Time { return clock }
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return a, nil }
	before, _ := os.ReadFile(filepath.Join(dir, control.AssignmentName))
	result, err := Tick(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result=%+v scans=%d elapsed=%v", result, a.scans, clock.Sub(start))
	after, _ := os.ReadFile(filepath.Join(dir, control.AssignmentName))
	freshControl, _ := control.Load(dir)
	if !bytes.Equal(before, after) || freshControl.AwaitingUser {
		t.Fatal("expiry changed declarations or raised a hold")
	}
	if len(a.sent) > 0 || a.scans != 0 || result.Action != "none" {
		t.Fatalf("sent expired grant after70s observation at max_age1m: %+v", result)
	}
}

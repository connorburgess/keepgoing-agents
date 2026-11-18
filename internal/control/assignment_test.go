package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/looptender/looptender/internal/lock"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assignmentFixture(t *testing.T) (string, AssignmentBinding, *AssignmentRecord) {
	t.Helper()
	dir := t.TempDir()
	canonical, _ := filepath.EvalSymlinks(dir)
	ctl := File{Ownership: &Ownership{ThreadID: "thr_example_owner", Generation: 1}, Enabled: true, Paused: true, Stop: true, AwaitingUser: true}
	if err := Save(dir, ctl); err != nil {
		t.Fatal(err)
	}
	g, err := WriteGoal(dir, "example-loop", "thr_example_owner", testGoalSpec(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	b := AssignmentBinding{Loop: "example-loop", Runtime: canonical, Config: filepath.Join(canonical, "example.yaml"), Coordinator: "thr_example_owner", Owner: "thr_example_owner", Generation: 1, Goal: g.ID, GoalRevision: 1}
	r, err := WriteAssignments(dir, "thr_example_owner", b, "", 0, func(x *AssignmentRevision) error {
		x.Inventory = AssignmentInventory{ObservedAt: time.Now().UTC(), Coverage: "observed_direct_children", Lanes: []AssignmentLane{{Thread: "thr_example_docs", Execution: "idle"}}}
		x.Rows = []AssignmentRow{{Spec: AssignmentSpec{ID: "docs", Lane: "thr_example_docs", ScopeRefs: []string{"PRIVATE-SCOPE"}, AuthorizationRefs: []string{"PRIVATE-AUTHORIZATION"}}, Revision: 1}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, b, r
}
func assignmentWaiting() AssignmentHandoff {
	return AssignmentHandoff{Assignment: "docs", AssignmentRevision: 1, DeclaredBy: "thr_example_docs", DeclaredAt: time.Now().UTC(), Outcome: "waiting", Availability: "available", Blocker: "person", EvidenceRefs: []string{"PRIVATE-EVIDENCE"}, RemainingRefs: []string{"PRIVATE-REMAINING"}, DependencyRefs: []string{"PRIVATE-APPROVAL"}, NextOwner: "person"}
}
func TestAssignmentLifecycleEvidenceAndGuard(t *testing.T) {
	dir, b, r := assignmentFixture(t)
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	if s := AssignmentSummaryOf(r, b); s.Counts.Unknown != 1 || s.Counts.Unfinished != 1 {
		t.Fatal(s)
	}
	if CheckAssignmentAwaitUser(dir, b) == nil {
		t.Fatal("missing handoff authorized await-user")
	}
	h := assignmentWaiting()
	r, err := WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error { x.Rows[0].Handoff = &h; x.Rows[0].HandoffRevision = 1; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if CheckAssignmentAwaitUser(dir, b) == nil {
		t.Fatal("unacknowledged handoff authorized await-user")
	}
	if s := AssignmentSummaryOf(r, b); s.Counts.Unknown != 1 || s.Counts.Unacknowledged != 1 {
		t.Fatal(s)
	}
	r, err = WriteAssignments(dir, b.Owner, b, r.ID, 2, func(x *AssignmentRevision) error { x.Rows[0].Acknowledged = 1; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckAssignmentAwaitUser(dir, b); err != nil {
		t.Fatal(err)
	}
	// Availability and idle execution do not change the person-blocking declaration.
	if s := AssignmentSummaryOf(r, b); s.Counts.PersonBlocked != 1 || s.Counts.Unknown != 0 {
		t.Fatal(s)
	}
	r, err = WriteAssignments(dir, b.Owner, b, r.ID, 3, func(x *AssignmentRevision) error {
		x.Rows[0].Handoff.Blocker = "dependency"
		x.Rows[0].HandoffRevision++
		x.Rows[0].Acknowledged = 2
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if CheckAssignmentAwaitUser(dir, b) == nil {
		t.Fatal("dependency waiting treated as person blocked")
	}
	r, err = WriteAssignments(dir, b.Owner, b, r.ID, 4, func(x *AssignmentRevision) error {
		x.Rows[0].Handoff.Outcome = "complete"
		x.Rows[0].Handoff.Blocker = "none"
		x.Rows[0].Handoff.RemainingRefs = nil
		x.Rows[0].HandoffRevision++
		x.Rows[0].Acknowledged = 3
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := AssignmentSummaryOf(r, b); s.Counts.Unfinished != 0 || s.Counts.Unknown != 0 {
		t.Fatal(s)
	}
	if CheckAssignmentAwaitUser(dir, b) == nil {
		t.Fatal("completed assignments manufactured global hold/completion")
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("metadata changed gates")
	}
	for _, mutate := range []func(*AssignmentBinding){func(b *AssignmentBinding) { b.Generation++ }, func(b *AssignmentBinding) { b.GoalRevision++ }, func(b *AssignmentBinding) { b.Config += "-other" }} {
		stale := b
		mutate(&stale)
		if s := AssignmentSummaryOf(r, stale); s.Availability != "stale_binding" || s.Counts != nil {
			t.Fatal(s)
		}
	}
}
func TestAssignmentLifecycleRefusalAndRetainedHistory(t *testing.T) {
	dir, b, r := assignmentFixture(t)
	path := filepath.Join(dir, AssignmentName)
	original, _ := os.ReadFile(path)
	for _, trial := range []struct {
		caller, id string
		rev        uint64
	}{{"thr_example_other", r.ID, 1}, {b.Owner, "assignment_00000000000000000000000000000000", 1}, {b.Owner, r.ID, 0}} {
		if _, err := WriteAssignments(dir, trial.caller, b, trial.id, trial.rev, func(x *AssignmentRevision) error { return nil }); err == nil {
			t.Fatal("stale/wrong-owner update succeeded")
		}
	}
	if _, err := WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error { x.Rows = nil; return nil }); err == nil {
		t.Fatal("unfinished assignment dropped")
	}
	// Even when the latest inventory loses the child, its obligation is retained.
	r, err := WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error { x.Inventory.Lanes = []AssignmentLane{}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if s := AssignmentSummaryOf(r, b); s.Counts.Absent != 1 || s.Counts.Unknown != 1 {
		t.Fatal(s)
	}
	good, _ := os.ReadFile(path)
	for name, mutate := range map[string]func(*AssignmentRecord){
		"binding-without-reset": func(x *AssignmentRecord) { x.Revisions[1].Binding.Generation++ },
		"history-loss":          func(x *AssignmentRecord) { x.Revisions[1].Rows = []AssignmentRow{} },
		"future-schema":         func(x *AssignmentRecord) { x.Version = 2 },
		"null-rows":             func(x *AssignmentRecord) { x.Revisions[1].Rows = nil },
		"copied-runtime":        func(x *AssignmentRecord) { x.Revisions[0].Binding.Runtime = filepath.Dir(b.Runtime) },
	} {
		t.Run(name, func(t *testing.T) {
			var x AssignmentRecord
			_ = json.Unmarshal(good, &x)
			mutate(&x)
			raw, _ := json.Marshal(x)
			_ = os.WriteFile(path, raw, 0600)
			if _, err := LoadAssignments(dir); err == nil {
				t.Fatal("damaged record accepted")
			}
			if _, err := WriteAssignments(dir, b.Owner, b, r.ID, 2, func(x *AssignmentRevision) error { return nil }); err == nil {
				t.Fatal("damaged record replaced")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, raw) {
				t.Fatal("damaged bytes repaired")
			}
		})
	}
	_ = os.WriteFile(path, good, 0600)
	// Independent runtime binding even with identical loop/thread/goal JSON.
	copyDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(copyDir, AssignmentName), good, 0600)
	if _, err := LoadAssignments(copyDir); err == nil {
		t.Fatal("copied runtime accepted")
	}
	// Interrupted publication leaves the previous snapshot authoritative.
	_ = os.WriteFile(filepath.Join(dir, "assignments.json.interrupted"), []byte("broken"), 0600)
	if _, err := LoadAssignments(dir); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, original, 0600)
	for n := 1; n < assignmentHistoryLimit; n++ {
		r, err = WriteAssignments(dir, b.Owner, b, r.ID, uint64(n), func(x *AssignmentRevision) error { return nil })
		if err != nil {
			t.Fatal(n, err)
		}
	}
	full, _ := os.ReadFile(path)
	if _, err := WriteAssignments(dir, b.Owner, b, r.ID, assignmentHistoryLimit, func(x *AssignmentRevision) error { return nil }); err == nil {
		t.Fatal("full history silently evicted evidence")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(full, after) {
		t.Fatal("capacity failure changed bytes")
	}
}
func TestAssignmentLifecycleSymlinkAndNonregular(t *testing.T) {
	dir, b, r := assignmentFixture(t)
	path := filepath.Join(dir, AssignmentName)
	raw, _ := os.ReadFile(path)
	_ = os.Remove(path)
	target := filepath.Join(t.TempDir(), "record.json")
	_ = os.WriteFile(target, raw, 0600)
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	if _, err := LoadAssignments(dir); err == nil {
		t.Fatal("symlink read")
	}
	if _, err := WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error { return nil }); err == nil {
		t.Fatal("symlink write")
	}
	_ = os.Remove(path)
	_ = os.Mkdir(path, 0700)
	if _, err := LoadAssignments(dir); err == nil {
		t.Fatal("directory read")
	}
}

func TestAssignmentLifecycleRetirementAndContention(t *testing.T) {
	dir, b, r := assignmentFixture(t)
	path := filepath.Join(dir, AssignmentName)
	before, _ := os.ReadFile(path)
	held, err := lock.AcquireNamed(dir, lock.Control)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error { return nil }); !errors.Is(err, lock.ErrHeld) {
		t.Fatal("control contention not preserved", err)
	}
	held.Release()
	ctl, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctl.SupersededBy = "thr_example_successor"
	if err = Save(dir, ctl); err != nil {
		t.Fatal(err)
	}
	if _, err = WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error { return nil }); err == nil {
		t.Fatal("retired source published assignment changes")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed publication changed evidence")
	}
}

func TestAssignmentLifecycleChangedBindingCannotRetainAcknowledgement(t *testing.T) {
	dir, b, r := assignmentFixture(t)
	h := assignmentWaiting()
	r, err := WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error { x.Rows[0].Handoff = &h; x.Rows[0].HandoffRevision = 1; return nil })
	if err != nil {
		t.Fatal(err)
	}
	r, err = WriteAssignments(dir, b.Owner, b, r.ID, 2, func(x *AssignmentRevision) error { x.Rows[0].Acknowledged = 1; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckAssignmentAwaitUser(dir, b); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r.Revisions[2])
	var next AssignmentRevision
	_ = json.Unmarshal(raw, &next)
	next.Revision = 4
	next.RecordedAt = time.Now().UTC()
	next.Binding.Generation++
	r.Revisions = append(r.Revisions, next)
	damaged, _ := json.Marshal(r)
	path := filepath.Join(dir, AssignmentName)
	_ = os.WriteFile(path, damaged, 0600)
	if _, err = LoadAssignments(dir); err == nil {
		t.Fatal("old acknowledgement satisfied changed binding")
	}
	if _, err = WriteAssignments(dir, b.Owner, b, r.ID, 4, func(x *AssignmentRevision) error { return nil }); err == nil {
		t.Fatal("damaged binding history replaced")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, damaged) {
		t.Fatal("damaged bytes repaired")
	}
}

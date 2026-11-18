//go:build unix

package main

import (
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAssignmentSchedulingOrdinaryWakeCannotLeakGrantThroughOtherHandoff(t *testing.T) {
	f, cfg, mutate := assignmentSchedulingFixture(t, true)
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  progress: {min_wake_interval: 1m}\n  name:", 1), 0600)
	mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}})
	r, _ := control.LoadAssignments(cfg.Loop.StateDir)
	grant := r.Revisions[len(r.Revisions)-1].Wake.ID
	mutate("handoff", control.AssignmentHandoff{Assignment: "ci", AssignmentRevision: 1, DeclaredBy: "thr_example_ci", DeclaredAt: time.Now().UTC(), Outcome: "unfinished", Availability: "busy", Blocker: "none", EvidenceRefs: []string{"new-ci-evidence"}, RemainingRefs: []string{"new-ci-work"}, NextOwner: "coordinator"})
	write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	if got := f.tick(); got["action"] != "wake" {
		t.Fatal("ordinary task wake blocked", got)
	}
	if strings.Contains(f.read("thr_primary.told"), "Assignment coordination:") {
		t.Fatal("unknown readiness claimed current evidence")
	}
	r, e := control.LoadAssignments(cfg.Loop.StateDir)
	if e != nil || r.Revisions[len(r.Revisions)-1].Wake.AttemptedAt == nil || r.Revisions[len(r.Revisions)-1].Wake.Disposition != "superseded_by_ordinary_wake_attempt" {
		t.Fatal("ordinary attempt did not retire temporarily unusable grant", r, e)
	}
	mutate("acknowledge", map[string]any{"assignment": "ci", "handoffRevision": 2})
	write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"active"}}`, 0600)
	st, e := state.Load(cfg.Loop.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	// Expire only ordinary send cadence in disposable diagnostic state; the
	// assignment/grant record and all authorization declarations stay intact.
	st.LastWakeAt = time.Now().Add(-2 * time.Minute)
	if e = state.Save(cfg.Loop.StateDir, st); e != nil {
		t.Fatal(e)
	}
	got := f.tick()
	if got["action"] == "wake" {
		t.Fatalf("grant %s reactivated after unrelated handoff acknowledgement, without new ready: %v; tells=%d", grant, got, strings.Count(f.read("calls"), "thread tell"))
	}
}

func TestAssignmentSchedulingOtherHandoffRequiresNewReadiness(t *testing.T) {
	f, _, mutate := assignmentSchedulingFixture(t, true)
	ready := map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}}
	mutate("ready", ready)
	mutate("handoff", control.AssignmentHandoff{Assignment: "ci", AssignmentRevision: 1, DeclaredBy: "thr_example_ci", DeclaredAt: time.Now().UTC(), Outcome: "unfinished", Availability: "busy", Blocker: "none", EvidenceRefs: []string{"updated-ci-evidence"}, RemainingRefs: []string{"updated-ci-work"}, NextOwner: "coordinator"})
	mutate("acknowledge", map[string]any{"assignment": "ci", "handoffRevision": 2})
	if got := f.tick(); got["action"] != "none" {
		t.Fatal("non-selected evidence reactivated old readiness", got)
	}
	if strings.Contains(f.read("calls"), "thread tell") {
		t.Fatal("old grant sent")
	}
	mutate("ready", ready)
	if got := f.tick(); got["action"] != "wake" {
		t.Fatal("new explicit declaration did not restore eligibility", got)
	}
}

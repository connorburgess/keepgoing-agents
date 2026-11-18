//go:build unix

package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
)

func TestAssignmentCompletionMilestoneAndVanishedWork(t *testing.T) {
	for _, name := range []string{"milestone", "vanished"} {
		t.Run(name, func(t *testing.T) { testAssignmentCompletionObjective(t, name == "vanished") })
	}
}
func testAssignmentCompletionObjective(t *testing.T, vanished bool) {
	f, cfg, plan := completionFixture(t, true)
	// Both assignments are explicit members of the current goal review. The
	// finished docs milestone cannot stand in for the whole authorized objective.
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_docs"},{"id":"thr_example_remaining"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_example_remaining.show"), `{"thread":{"id":"thr_example_remaining","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	write(t, plan, `{"assignments":[{"id":"docs","lane":"thr_example_docs","scopeRefs":["example:docs"],"authorizationRefs":["example:authority"]},{"id":"remaining","lane":"thr_example_remaining","scopeRefs":["example:remaining-objective-work"],"authorizationRefs":["example:objective-authority"]}]}`, 0600)
	mutate := func(cmd, path string) {
		t.Helper()
		r, err := control.LoadAssignments(cfg.Loop.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		if c, _, e := f.run("assignment", cmd, "--file", path, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions))); c != 0 {
			t.Fatal(e)
		}
	}
	mutate("review", plan)
	completionHandoff(t, f, cfg, "complete", true)
	handoff := func(outcome string, revision uint64) {
		t.Helper()
		h := control.AssignmentHandoff{Assignment: "remaining", AssignmentRevision: 1, DeclaredBy: "thr_example_remaining", DeclaredAt: time.Now().UTC(), Outcome: outcome, Availability: "available", Blocker: "none", EvidenceRefs: []string{"example:explicit-status"}, RemainingRefs: []string{}, DependencyRefs: []string{}}
		if outcome == "unfinished" {
			h.RemainingRefs = []string{"example:remaining-objective-work"}
			h.NextOwner = "coordinator"
		}
		b, _ := json.Marshal(h)
		p := filepath.Join(t.TempDir(), "handoff.json")
		write(t, p, string(b), 0600)
		mutate("handoff", p)
		b, _ = json.Marshal(map[string]any{"assignment": "remaining", "handoffRevision": revision})
		write(t, p, string(b), 0600)
		mutate("acknowledge", p)
	}
	handoff("unfinished", 1)
	// A stopped/died/disappeared worker is no evidence that its work finished.
	// Review the changed direct-child set explicitly, retaining the obligation.
	if vanished {
		write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_docs"}]`, 0600)
		mutate("review", plan)
	}
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	x := r.Revisions[len(r.Revisions)-1]
	if len(x.Rows) != 2 || x.Rows[1].Spec.ID != "remaining" || x.Rows[1].Handoff.Outcome != "unfinished" || x.Rows[1].Acknowledged != 1 {
		t.Fatal("worker disappearance erased declared work", x.Rows)
	}
	completionRefused(t, f, cfg, completionInput(t, cfg))
	// Completion becomes eligible only after a new explicit complete declaration
	// and acknowledgement, followed by whole-objective attestation. No worker
	// is restarted, and no unrelated roadmap prose is read to invent scope.
	handoff("complete", 2)
	if c, _, e := f.run("complete", "--review-file", completionInput(t, cfg)); c != 0 {
		t.Fatal(e)
	}
	if !mustAssignmentControl(t, cfg).Completed {
		t.Fatal("valid whole objective was not completed")
	}
}

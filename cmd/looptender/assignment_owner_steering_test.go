//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
)

// New owner steering is explicit evidence, not text inferred from a thread.
// Reviewing changed authorization must retire an older person-park declaration
// without dropping its obligation or clearing any actual runtime hold.
func TestAssignmentNewOwnerDirectionReconcilesParkedLane(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_coordinator")
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  name:", 1), 0600)
	save := func(path string, value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(t, path, string(b), 0600)
	}
	runOK := func(args ...string) {
		t.Helper()
		if code, out, errs := f.run(args...); code != 0 || strings.Contains(out+errs, "PRIVATE-") {
			t.Fatal(code, out, errs)
		}
	}
	runOK("enable")
	goal := filepath.Join(t.TempDir(), "goal.json")
	save(goal, map[string]any{"title": "Fictional documentation", "objectiveRefs": []string{"PRIVATE-OBJECTIVE"}, "acceptanceRefs": []string{"PRIVATE-ACCEPTANCE"}})
	runOK("goal", "create", "--file", goal)
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"id":"thr_primary","status":"idle"}}`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_parked"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_example_parked.show"), `{"thread":{"id":"thr_example_parked","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	plan := filepath.Join(t.TempDir(), "plan.json")
	spec := control.AssignmentSpec{ID: "docs", Lane: "thr_example_parked", ScopeRefs: []string{"PRIVATE-SCOPE"}, AuthorizationRefs: []string{"PRIVATE-OLD-PARK"}}
	save(plan, map[string]any{"assignments": []control.AssignmentSpec{spec}})
	runOK("assignment", "review", "--file", plan, "--json")
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	get := func() *control.AssignmentRecord {
		t.Helper()
		r, err := control.LoadAssignments(cfg.Loop.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	mutate := func(cmd, path string) {
		t.Helper()
		r := get()
		runOK("assignment", cmd, "--file", path, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions)), "--json")
	}
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	parked := control.AssignmentHandoff{Assignment: "docs", AssignmentRevision: 1, DeclaredBy: spec.Lane, DeclaredAt: time.Now().UTC(), Outcome: "waiting", Availability: "available", Blocker: "person", EvidenceRefs: []string{"PRIVATE-PARK-EVIDENCE"}, RemainingRefs: []string{"PRIVATE-WORK"}, DependencyRefs: []string{"PRIVATE-OLD-GATE"}, NextOwner: "person"}
	save(handoff, parked)
	mutate("handoff", handoff)
	ack := filepath.Join(t.TempDir(), "ack.json")
	save(ack, map[string]any{"assignment": "docs", "handoffRevision": 1})
	mutate("acknowledge", ack)
	runOK("await-user")
	runOK("pause")
	runOK("stop")
	before, err := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
	if err != nil {
		t.Fatal(err)
	}
	// Owner explicitly activates the same unfinished task under a newer direction.
	spec.AuthorizationRefs = []string{"PRIVATE-NEW-OWNER-DIRECTION"}
	save(plan, map[string]any{"assignments": []control.AssignmentSpec{spec}})
	mutate("review", plan)
	r := get()
	row := r.Revisions[len(r.Revisions)-1].Rows[0]
	if row.Revision != 2 || row.Handoff != nil || row.Acknowledged != 0 || row.Spec.ID != "docs" {
		t.Fatalf("old park adopted or obligation dropped: %+v", row)
	}
	if s := assignmentStatus(cfg, mustAssignmentControl(t, cfg)); s.Counts == nil || s.Counts.Unknown != 1 || s.Counts.Unfinished != 1 || s.Counts.PersonBlocked != 0 {
		t.Fatal(s)
	}
	// A replayed old declaration cannot re-establish the superseded person gate.
	r = get()
	if code, _, _ := f.run("assignment", "handoff", "--file", handoff, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions))); code == 0 {
		t.Fatal("old assignment revision replayed")
	}
	parked.AssignmentRevision = 2
	parked.Outcome = "unfinished"
	parked.Blocker = "none"
	parked.DependencyRefs = nil
	parked.NextOwner = "coordinator"
	save(handoff, parked)
	mutate("handoff", handoff)
	mutate("acknowledge", ack)
	if s := assignmentStatus(cfg, mustAssignmentControl(t, cfg)); s.Counts.Unknown != 0 || s.Counts.PersonBlocked != 0 || s.Counts.Unfinished != 1 {
		t.Fatal(s)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("assignment reconciliation cleared or changed runtime holds")
	}
	if f.read("probes") != "" {
		t.Fatal("assignment reconciliation invoked provider")
	}
	for _, action := range []string{"thread tell", "thread retry", "thread create", "thread spawn", "thread archive", "thread reparent"} {
		if strings.Contains(f.read("calls"), action) {
			t.Fatalf("assignment reconciliation invoked %s", action)
		}
	}
	if len(get().Revisions[0].Rows) != 1 || get().Revisions[2].Rows[0].Handoff.Blocker != "person" {
		t.Fatal("old park history lost")
	}
}

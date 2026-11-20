package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assignmentWakeFixture(t *testing.T) (string, AssignmentBinding, *AssignmentRecord) {
	t.Helper()
	dir, b, r := assignmentFixture(t)
	c, _ := Load(dir)
	c.Stop, c.Paused, c.AwaitingUser = false, false, false
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	h := assignmentWaiting()
	h.Outcome = "unfinished"
	h.Blocker = "none"
	h.DependencyRefs = nil
	r, err := WriteAssignments(dir, b.Owner, b, r.ID, 1, func(x *AssignmentRevision) error {
		x.Rows[0].Handoff = &h
		x.Rows[0].HandoffRevision = 1
		x.Rows[0].Acknowledged = 1
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err = WriteAssignments(dir, b.Owner, b, r.ID, 2, func(x *AssignmentRevision) error {
		return DeclareAssignmentWake(x, b, []AssignmentWakeSelection{{"docs", 1, 1}}, time.Now().UTC(), time.Hour)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, b, r
}
func TestAssignmentWakeDurableAttemptAndReconciliation(t *testing.T) {
	dir, b, r := assignmentWakeFixture(t)
	w := r.Revisions[2].Wake
	if err := ConsumeAssignmentWake(dir, b.Owner, b, r.ID, 3, w.ID, "assignment_notice_attempt", time.Now().UTC(), time.Hour); err != nil {
		t.Fatal(err)
	}
	fresh, err := LoadAssignments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n, s := AssignmentWakeLocal(fresh, b, time.Now().UTC(), time.Hour); n != 0 || s != "attempted_unreconciled" {
		t.Fatal(n, s)
	}
	if err := ConsumeAssignmentWake(dir, b.Owner, b, r.ID, 4, w.ID, "assignment_notice_attempt", time.Now().UTC(), time.Hour); err == nil {
		t.Fatal("replayed")
	}
	if _, err := WriteAssignments(dir, b.Owner, b, r.ID, 4, func(x *AssignmentRevision) error {
		return DeclareAssignmentWake(x, b, []AssignmentWakeSelection{{"docs", 1, 1}}, time.Now().UTC(), time.Hour)
	}); err == nil {
		t.Fatal("unreconciled attempt rearmed")
	}
	if _, err := WriteAssignments(dir, b.Owner, b, r.ID, 4, func(x *AssignmentRevision) error { now := time.Now().UTC(); x.Wake.ReconciledAt = &now; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAssignments(dir, b.Owner, b, r.ID, 5, func(x *AssignmentRevision) error {
		return DeclareAssignmentWake(x, b, []AssignmentWakeSelection{{"docs", 1, 1}}, time.Now().UTC(), time.Hour)
	}); err != nil {
		t.Fatal(err)
	}
}
func TestAssignmentWakeMalformedHistoryRetainsBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(*AssignmentRecord)
	}{
		{"drop-grant", func(r *AssignmentRecord) { r.Revisions[3].Wake = nil }},
		{"clear-attempt", func(r *AssignmentRecord) { r.Revisions[4].Wake.AttemptedAt = nil; r.Revisions[4].Wake.Disposition = "" }},
		{"replace-unreconciled", func(r *AssignmentRecord) { r.Revisions[4].Wake.ID = "assignment_00000000000000000000000000000000" }},
		{"changed-selection", func(r *AssignmentRecord) { r.Revisions[4].Wake.Selections[0].AssignmentRevision++ }},
		{"missing-digest", func(r *AssignmentRecord) { r.Revisions[2].Wake.EvidenceDigest = "" }},
		{"changed-digest", func(r *AssignmentRecord) {
			r.Revisions[4].Wake.EvidenceDigest = "0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"lost-acknowledgement", func(r *AssignmentRecord) { r.Revisions[4].Rows[0].Acknowledged = 0 }},
		{"future-attempt", func(r *AssignmentRecord) { at := time.Now().Add(time.Hour); r.Revisions[4].Wake.AttemptedAt = &at }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, b, r := assignmentWakeFixture(t)
			if err := ConsumeAssignmentWake(dir, b.Owner, b, r.ID, 3, r.Revisions[2].Wake.ID, "assignment_notice_attempt", time.Now().UTC(), time.Hour); err != nil {
				t.Fatal(err)
			}
			if _, err := WriteAssignments(dir, b.Owner, b, r.ID, 4, func(*AssignmentRevision) error { return nil }); err != nil {
				t.Fatal(err)
			}
			r, err := LoadAssignments(dir)
			if err != nil {
				t.Fatal(err)
			}
			tc.damage(r)
			raw, _ := json.Marshal(r)
			path := filepath.Join(dir, AssignmentName)
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = LoadAssignments(dir); err == nil {
				t.Fatal("damaged grant history accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(raw, after) {
				t.Fatal("damaged bytes rewritten")
			}
		})
	}
}

func TestAssignmentWakeConsumptionControlFences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*File)
	}{
		{"pause", func(c *File) { c.Paused = true }}, {"stop", func(c *File) { c.Stop = true }}, {"awaiting", func(c *File) { c.AwaitingUser = true }}, {"complete", func(c *File) { c.Completed = true }}, {"disabled", func(c *File) { c.Enabled = false }}, {"retired", func(c *File) { c.SupersededBy = "thr_example_successor" }}, {"owner", func(c *File) { c.Ownership.Generation++ }}, {"target", func(c *File) { c.BBTarget = "thr_example_successor" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, b, r := assignmentWakeFixture(t)
			before, _ := os.ReadFile(filepath.Join(dir, AssignmentName))
			c, _ := Load(dir)
			tc.change(&c)
			if err := Save(dir, c); err != nil {
				t.Fatal(err)
			}
			if err := ConsumeAssignmentWake(dir, b.Owner, b, r.ID, 3, r.Revisions[2].Wake.ID, "assignment_notice_attempt", time.Now().UTC(), time.Hour); err == nil {
				t.Fatal("control fence bypassed")
			}
			after, _ := os.ReadFile(filepath.Join(dir, AssignmentName))
			if !bytes.Equal(before, after) {
				t.Fatal("refused attempt changed declarations")
			}
		})
	}
}
func TestAssignmentWakeMissingStaleAndUnknownEvidence(t *testing.T) {
	_, b, r := assignmentWakeFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*AssignmentRecord, *AssignmentBinding)
		later  bool
	}{
		{"missing", func(r *AssignmentRecord, b *AssignmentBinding) { r.Revisions[2].Wake = nil }, false},
		{"handoff", func(r *AssignmentRecord, b *AssignmentBinding) { r.Revisions[2].Rows[0].Handoff = nil }, false},
		{"unacknowledged", func(r *AssignmentRecord, b *AssignmentBinding) { r.Revisions[2].Rows[0].Acknowledged = 0 }, false},
		{"revision", func(r *AssignmentRecord, b *AssignmentBinding) { r.Revisions[2].Rows[0].HandoffRevision++ }, false},
		{"partial", func(r *AssignmentRecord, b *AssignmentBinding) { r.Revisions[2].Inventory.Coverage = "partial" }, false},
		{"absent", func(r *AssignmentRecord, b *AssignmentBinding) { r.Revisions[2].Inventory.Lanes = nil }, false},
		{"binding", func(r *AssignmentRecord, b *AssignmentBinding) { b.Generation++ }, false},
		{"age", func(*AssignmentRecord, *AssignmentBinding) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(r)
			var copy AssignmentRecord
			_ = json.Unmarshal(raw, &copy)
			binding := b
			tc.change(&copy, &binding)
			now := time.Now().UTC()
			if tc.later {
				now = now.Add(2 * time.Hour)
			}
			if n, s := AssignmentWakeLocal(&copy, binding, now, time.Hour); n != 0 || s == "declared_ready" {
				t.Fatal(n, s)
			}
			summary := AssignmentWakeSummaryOf(&copy, binding, now, time.Hour)
			if summary.DeclaredReady != nil || summary.NextAction == "" {
				t.Fatal(summary)
			}
		})
	}
}

func TestAssignmentWakeFullHistoryWithholdsWithoutDiscarding(t *testing.T) {
	dir, b, r := assignmentWakeFixture(t)
	for len(r.Revisions) < assignmentHistoryLimit {
		var err error
		r, err = WriteAssignments(dir, b.Owner, b, r.ID, uint64(len(r.Revisions)), func(*AssignmentRevision) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(dir, AssignmentName))
	if n, s := AssignmentWakeLocal(r, b, time.Now().UTC(), time.Hour); n != 0 || s != "capacity_exhausted" {
		t.Fatal(n, s)
	}
	if err := ConsumeAssignmentWake(dir, b.Owner, b, r.ID, uint64(len(r.Revisions)), r.Revisions[len(r.Revisions)-1].Wake.ID, "assignment_notice_attempt", time.Now().UTC(), time.Hour); err == nil {
		t.Fatal("full history accepted runtime write")
	}
	after, _ := os.ReadFile(filepath.Join(dir, AssignmentName))
	if !bytes.Equal(before, after) {
		t.Fatal("full history discarded evidence")
	}
}

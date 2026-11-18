package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"time"
)

// AssignmentWake is one coordinator-declared batch, never permission to act on
// children. Historical selection/binding survives changes as stale evidence.
type AssignmentWake struct {
	ID             string                    `json:"id"`
	Binding        AssignmentBinding         `json:"binding"`
	InventoryAt    time.Time                 `json:"inventoryAt"`
	DeclaredAt     time.Time                 `json:"declaredAt"`
	Selections     []AssignmentWakeSelection `json:"selections"`
	EvidenceDigest string                    `json:"evidenceDigest"`
	AttemptedAt    *time.Time                `json:"attemptedAt,omitempty"`
	Disposition    string                    `json:"disposition,omitempty"`
	ReconciledAt   *time.Time                `json:"reconciledAt,omitempty"`
}
type AssignmentWakeSelection struct {
	Assignment         string `json:"assignment"`
	AssignmentRevision uint64 `json:"assignmentRevision"`
	HandoffRevision    uint64 `json:"handoffRevision"`
}

// AssignmentWakeSummary contains no grant IDs, lane IDs or private references.
type AssignmentWakeSummary struct {
	State         string     `json:"state"`
	NextAction    string     `json:"nextAction"`
	DeclaredAt    *time.Time `json:"declaredAt,omitempty"`
	AttemptedAt   *time.Time `json:"attemptedAt,omitempty"`
	ReconciledAt  *time.Time `json:"reconciledAt,omitempty"`
	DeclaredReady *int       `json:"declaredReady,omitempty"`
}

func AssignmentWakeSummaryOf(r *AssignmentRecord, b AssignmentBinding, now time.Time, age time.Duration) *AssignmentWakeSummary {
	n, state := AssignmentWakeLocal(r, b, now, age)
	s := &AssignmentWakeSummary{State: state, NextAction: "looptender assignment show --private; reconcile current lanes with assignment review, handoff and acknowledge"}
	if state == "declared_ready" {
		s.DeclaredReady = &n
	}
	if r != nil && len(r.Revisions) > 0 {
		if w := r.Revisions[len(r.Revisions)-1].Wake; w != nil {
			s.DeclaredAt = &w.DeclaredAt
			s.AttemptedAt = w.AttemptedAt
			s.ReconciledAt = w.ReconciledAt
		}
	}
	switch state {
	case "ready_required", "reviewed_attempt":
		s.NextAction = "looptender assignment ready --file ready.json --id <record-id> --revision <current-revision>"
	case "attempted_unreconciled":
		s.NextAction = "looptender assignment show --private; then assignment reconcile-wake --file reconciliation.json --id <record-id> --revision <current-revision>; delivery is not established"
	case "capacity_exhausted":
		s.NextAction = "looptender assignment show --private; preserve the full record and explicitly disable assignment_coordination until supported record rotation; never roll back an attempt"
	case "declared_ready":
		s.NextAction = "wait for an eligible ordinary check; no lane is dispatched and no delivery is established"
	}
	if r != nil && len(r.Revisions) >= assignmentHistoryLimit {
		s.NextAction = "looptender assignment show --private; preserve the full record and explicitly disable assignment_coordination until supported record rotation; never roll back an attempt"
	}
	return s
}

func validAssignmentWake(w *AssignmentWake, at time.Time) bool {
	if w == nil {
		return true
	}
	digest, err := hex.DecodeString(w.EvidenceDigest)
	if err != nil || len(digest) != sha256.Size {
		return false
	}
	if !assignmentRecordID.MatchString(w.ID) || !validAssignmentBinding(w.Binding) || w.InventoryAt.IsZero() || w.DeclaredAt.Before(w.InventoryAt) || w.DeclaredAt.After(at) || len(w.Selections) == 0 || len(w.Selections) > 200 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range w.Selections {
		if !assignmentID.MatchString(s.Assignment) || seen[s.Assignment] || s.AssignmentRevision == 0 || s.HandoffRevision == 0 {
			return false
		}
		seen[s.Assignment] = true
	}
	if w.AttemptedAt == nil {
		return w.Disposition == "" && w.ReconciledAt == nil
	}
	if w.AttemptedAt.Before(w.DeclaredAt) || w.AttemptedAt.After(at) || (w.Disposition != "assignment_notice_attempt" && w.Disposition != "superseded_by_ordinary_wake_attempt") {
		return false
	}
	return w.ReconciledAt == nil || (!w.ReconciledAt.Before(*w.AttemptedAt) && !w.ReconciledAt.After(at))
}
func assignmentWakeTransition(a, b *AssignmentWake) bool {
	if a == nil {
		return true
	}
	if b == nil {
		return false
	} // obligations/attempts are never silently discarded
	if a.ID != b.ID {
		return a.AttemptedAt == nil || a.ReconciledAt != nil
	}
	x, y := *a, *b
	x.AttemptedAt, x.ReconciledAt, x.Disposition = nil, nil, ""
	y.AttemptedAt, y.ReconciledAt, y.Disposition = nil, nil, ""
	if !reflect.DeepEqual(x, y) {
		return false
	}
	if a.AttemptedAt != nil && (!reflect.DeepEqual(a.AttemptedAt, b.AttemptedAt) || a.Disposition != b.Disposition) {
		return false
	}
	return a.ReconciledAt == nil || reflect.DeepEqual(a.ReconciledAt, b.ReconciledAt)
}

// AssignmentBindingFor uses local typed evidence only. Source bytes and tick
// locking remain the command/supervisor caller's responsibility.
func AssignmentBindingFor(dir, path, loop, coordinator string, c File) (AssignmentBinding, error) {
	b := AssignmentBinding{Loop: loop, Coordinator: coordinator}
	if c.PendingTransfer != nil || (c.BBTarget != "" && c.BBTarget != coordinator) || c.Ownership == nil || CheckOwner(c, "", false) != nil {
		return b, ErrAssignment
	}
	var err error
	b.Config, err = filepath.EvalSymlinks(path)
	if err != nil {
		return b, ErrAssignment
	}
	b.Config, err = filepath.Abs(b.Config)
	if err != nil {
		return b, ErrAssignment
	}
	b.Runtime, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return b, ErrAssignment
	}
	b.Runtime, err = filepath.Abs(b.Runtime)
	if err != nil {
		return b, ErrAssignment
	}
	g, err := LoadGoal(dir)
	if err != nil || g == nil || g.OwningLoop != loop {
		return b, ErrAssignment
	}
	b.Owner, b.Generation, b.Goal, b.GoalRevision = c.Ownership.ThreadID, c.Ownership.Generation, g.ID, g.Summary().Revision
	if !validAssignmentBinding(b) {
		return b, ErrAssignment
	}
	return b, nil
}

// AssignmentWakeLocal checks historical declarations, not current execution.
// Ages only withhold scheduling; they never clear a hold or discard evidence.
func AssignmentWakeLocal(r *AssignmentRecord, b AssignmentBinding, now time.Time, age time.Duration) (int, string) {
	if r == nil || len(r.Revisions) == 0 {
		return 0, "review_required"
	}
	x := r.Revisions[len(r.Revisions)-1]
	if x.Binding != b {
		return 0, "stale_binding"
	}
	w := x.Wake
	if w == nil {
		return 0, "ready_required"
	}
	if w.AttemptedAt != nil {
		if w.ReconciledAt == nil {
			return 0, "attempted_unreconciled"
		}
		return 0, "reviewed_attempt"
	}
	if len(r.Revisions) >= assignmentHistoryLimit {
		return 0, "capacity_exhausted"
	}
	if w.EvidenceDigest != assignmentWakeEvidence(x) || w.Binding != b || !w.InventoryAt.Equal(x.Inventory.ObservedAt) || x.Inventory.Coverage != "observed_direct_children" || now.Before(w.DeclaredAt) || now.Sub(w.DeclaredAt) > age || now.Sub(x.Inventory.ObservedAt) > age {
		return 0, "stale_evidence"
	}
	rows := map[string]AssignmentRow{}
	present := map[string]bool{}
	for _, l := range x.Inventory.Lanes {
		present[l.Thread] = true
	}
	for _, row := range x.Rows {
		if !present[row.Spec.Lane] || !assignmentAcknowledged(row) || now.Before(row.Handoff.DeclaredAt) || now.Sub(row.Handoff.DeclaredAt) > age {
			return 0, "reconciliation_required"
		}
		rows[row.Spec.ID] = row
	}
	for _, s := range w.Selections {
		row, ok := rows[s.Assignment]
		if !ok || row.Revision != s.AssignmentRevision || row.HandoffRevision != s.HandoffRevision || row.Handoff.Outcome != "unfinished" || row.Handoff.Availability != "available" || row.Handoff.Blocker != "none" {
			return 0, "stale_evidence"
		}
	}
	return len(w.Selections), "declared_ready"
}

// DeclareAssignmentWake is called by an explicit authorized coordinator action.
func DeclareAssignmentWake(x *AssignmentRevision, b AssignmentBinding, selections []AssignmentWakeSelection, now time.Time, age time.Duration) error {
	if x.Wake != nil && x.Wake.AttemptedAt != nil && x.Wake.ReconciledAt == nil {
		return errors.New("assignment wake attempted; run assignment reconcile-wake before declaring readiness again")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ErrAssignment
	}
	x.Wake = &AssignmentWake{ID: "assignment_" + hex.EncodeToString(random[:]), Binding: b, InventoryAt: x.Inventory.ObservedAt, DeclaredAt: now, Selections: selections, EvidenceDigest: assignmentWakeEvidence(*x)}
	copy := *x
	copy.RecordedAt = now
	r := &AssignmentRecord{Revisions: []AssignmentRevision{copy}}
	if !validAssignmentWake(x.Wake, now) {
		return ErrAssignment
	}
	if _, state := AssignmentWakeLocal(r, b, now, age); state != "declared_ready" {
		return errors.New("assignment evidence is not current, acknowledged, available unfinished work; review and import current handoffs before assignment ready")
	}
	return nil
}

// ConsumeAssignmentWake requires tick.lock. Its optimistic write precedes Send.
func ConsumeAssignmentWake(dir, caller string, b AssignmentBinding, id string, revision uint64, grant, disposition string, now time.Time, age time.Duration) error {
	_, err := WriteAssignments(dir, caller, b, id, revision, func(x *AssignmentRevision) error {
		c, err := Load(dir)
		if err != nil {
			return ErrAssignment
		}
		g := c.Gate()
		if !g.Enabled || g.Paused || g.Stop || g.AwaitingUser || g.Completed {
			return ErrAssignment
		}
		if x.Wake == nil || x.Wake.ID != grant || x.Wake.Binding != b || x.Wake.AttemptedAt != nil {
			return ErrAssignment
		}
		if _, status := AssignmentWakeLocal(&AssignmentRecord{Revisions: []AssignmentRevision{*x}}, b, now, age); disposition != "superseded_by_ordinary_wake_attempt" && status != "declared_ready" {
			return ErrAssignment
		}
		x.Wake.AttemptedAt = &now
		x.Wake.Disposition = disposition
		return nil
	})
	return err
}

func assignmentWakeDeclarationValid(history []AssignmentRevision, n int, x AssignmentRevision) bool {
	if x.Wake == nil {
		return true
	}
	if n > 0 && history[n-1].Wake != nil && history[n-1].Wake.ID == x.Wake.ID {
		return true
	}
	if x.Wake.AttemptedAt != nil {
		return false
	}
	_, state := AssignmentWakeLocal(&AssignmentRecord{Revisions: []AssignmentRevision{x}}, x.Binding, x.RecordedAt, 24*time.Hour)
	return state == "declared_ready"
}

// Hash explicit records only to bind freshness, never to interpret reference text
// or authenticate a declaration. The digest never enters ordinary projection.
func assignmentWakeEvidence(x AssignmentRevision) string {
	raw, _ := json.Marshal(struct {
		Rows      []AssignmentRow
		Inventory AssignmentInventory
	}{x.Rows, x.Inventory})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// AssignmentWakeRetirable identifies a strictly loaded, still-bound grant for
// descriptive retirement by an ordinary wake; it does not establish readiness.
func AssignmentWakeRetirable(r *AssignmentRecord, b AssignmentBinding) bool {
	if r == nil || len(r.Revisions) == 0 || len(r.Revisions) >= assignmentHistoryLimit {
		return false
	}
	x := r.Revisions[len(r.Revisions)-1]
	return x.Binding == b && x.Wake != nil && x.Wake.Binding == b && x.Wake.AttemptedAt == nil
}

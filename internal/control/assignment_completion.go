package control

import "errors"

// CheckAssignmentCompletion is a local evidence precondition, never proof that
// the whole objective is complete. r must come from strict LoadAssignments;
// caller separately supplies the whole-objective attestation.
// Previously completed acknowledged rows may be absent after explicit review;
// unfinished and unknown obligations must never disappear by inference.
func CheckAssignmentCompletion(r *AssignmentRecord, b AssignmentBinding, id string, revision uint64) error {
	if r == nil || r.ID != id || len(r.Revisions) == 0 {
		return ErrAssignment
	}
	x := r.Revisions[len(r.Revisions)-1]
	if x.Revision != revision || x.Binding != b || x.Inventory.Coverage != "observed_direct_children" {
		return ErrAssignment
	}
	for _, row := range x.Rows {
		if !assignmentComplete(row) {
			return errors.New("assignment obligations are unfinished or unacknowledged; reconcile before whole-objective completion")
		}
	}
	for _, lane := range x.Inventory.Lanes {
		if lane.Execution != "idle" {
			return errors.New("reviewed lane execution is not idle; reconcile before completion")
		}
	}
	return nil
}

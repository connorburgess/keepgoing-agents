package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/lock"
)

const AssignmentName = "assignments.json"
const AssignmentLimit = 1 << 20
const AssignmentInputLimit = 100 << 10
const assignmentHistoryLimit = 32

var assignmentRecordID = regexp.MustCompile(`^assignment_[0-9a-f]{32}$`)

var ErrAssignment = errors.New("assignment lifecycle unavailable or invalid; inspect the private record; no evidence was replaced")

type AssignmentBinding struct {
	Loop         string `json:"loop"`
	Config       string `json:"config"`
	Runtime      string `json:"runtime"`
	Coordinator  string `json:"coordinator"`
	Owner        string `json:"owner"`
	Generation   uint64 `json:"generation"`
	Goal         string `json:"goal"`
	GoalRevision uint64 `json:"goalRevision"`
}
type AssignmentSpec struct {
	ID                string   `json:"id"`
	Lane              string   `json:"lane"`
	ScopeRefs         []string `json:"scopeRefs"`
	AuthorizationRefs []string `json:"authorizationRefs"`
}
type AssignmentLane struct {
	Thread    string `json:"thread"`
	Execution string `json:"execution"`
	Archived  bool   `json:"archived"`
}
type AssignmentInventory struct {
	ObservedAt time.Time        `json:"observedAt"`
	Coverage   string           `json:"coverage"` // observed_direct_children or partial; never descendants/atomic snapshot
	Lanes      []AssignmentLane `json:"lanes"`
}
type AssignmentHandoff struct {
	Assignment         string    `json:"assignment"`
	AssignmentRevision uint64    `json:"assignmentRevision"`
	DeclaredBy         string    `json:"declaredBy"`
	DeclaredAt         time.Time `json:"declaredAt"`
	Outcome            string    `json:"outcome"`
	Availability       string    `json:"availability"`
	Blocker            string    `json:"blocker"` // none, person, dependency; explicit declaration
	EvidenceRefs       []string  `json:"evidenceRefs"`
	RemainingRefs      []string  `json:"remainingRefs"`
	DependencyRefs     []string  `json:"dependencyRefs"`
	NextOwner          string    `json:"nextOwner"`
	SuccessorRef       string    `json:"successorRef,omitempty"`
}
type AssignmentRow struct {
	Spec            AssignmentSpec     `json:"spec"`
	Revision        uint64             `json:"revision"`
	Handoff         *AssignmentHandoff `json:"handoff,omitempty"`
	HandoffRevision uint64             `json:"handoffRevision"`
	Acknowledged    uint64             `json:"acknowledged"`
}
type AssignmentRevision struct {
	Revision   uint64              `json:"revision"`
	RecordedAt time.Time           `json:"recordedAt"`
	Binding    AssignmentBinding   `json:"binding"`
	Inventory  AssignmentInventory `json:"inventory"`
	Rows       []AssignmentRow     `json:"rows"`
	Wake       *AssignmentWake     `json:"wake,omitempty"`
}
type AssignmentRecord struct {
	Version   int                  `json:"version"`
	ID        string               `json:"id"`
	Revisions []AssignmentRevision `json:"revisions"`
}

// AssignmentSummary contains no private identities, references, or declarations.
// Counts are pointers so unavailable evidence cannot be mistaken for zero.
type AssignmentCounts struct {
	Assignments       int `json:"assignments"`
	Unknown           int `json:"unknown"`
	Unacknowledged    int `json:"unacknowledged"`
	Unfinished        int `json:"unfinished"`
	PersonBlocked     int `json:"personBlocked"`
	DependencyWaiting int `json:"dependencyWaiting"`
	Absent            int `json:"absent"`
}
type AssignmentSummary struct {
	Availability string                 `json:"availability"`
	Revision     uint64                 `json:"revision,omitempty"`
	Coverage     string                 `json:"coverage,omitempty"`
	ObservedAt   *time.Time             `json:"observedAt,omitempty"`
	RecordedAt   *time.Time             `json:"recordedAt,omitempty"`
	Counts       *AssignmentCounts      `json:"counts,omitempty"`
	Coordination *AssignmentWakeSummary `json:"coordination,omitempty"`
}

func assignmentRefs(v []string, required bool) bool {
	if len(v) > 32 || (required && len(v) == 0) {
		return false
	}
	for _, s := range v {
		if strings.TrimSpace(s) == "" || len(s) > 2048 || strings.ContainsAny(s, "\x00\r\n") {
			return false
		}
	}
	return true
}
func validAssignmentSpec(s AssignmentSpec) bool {
	return assignmentID.MatchString(s.ID) && ownerID.MatchString(s.Lane) && assignmentRefs(s.ScopeRefs, true) && assignmentRefs(s.AuthorizationRefs, true)
}
func validAssignmentBinding(b AssignmentBinding) bool {
	return strings.TrimSpace(b.Loop) != "" && len(b.Loop) <= 256 && filepath.IsAbs(b.Config) && filepath.IsAbs(b.Runtime) && ownerID.MatchString(b.Coordinator) && ownerID.MatchString(b.Owner) && b.Generation > 0 && goalID.MatchString(b.Goal) && b.GoalRevision > 0
}
func validAssignmentInventory(i AssignmentInventory) bool {
	if i.ObservedAt.IsZero() || (i.Coverage != "observed_direct_children" && i.Coverage != "partial") || i.Lanes == nil || len(i.Lanes) > 200 {
		return false
	}
	seen := map[string]bool{}
	for _, l := range i.Lanes {
		if !ownerID.MatchString(l.Thread) || seen[l.Thread] {
			return false
		}
		seen[l.Thread] = true
		switch l.Execution {
		case "pending", "idle", "starting", "active", "stopping", "error", "unknown":
		default:
			return false
		}
		if l.Execution == "unknown" && i.Coverage != "partial" {
			return false
		}
	}
	return true
}
func ValidAssignmentHandoff(h AssignmentHandoff) bool {
	if !assignmentID.MatchString(h.Assignment) || h.AssignmentRevision == 0 || !ownerID.MatchString(h.DeclaredBy) || h.DeclaredAt.IsZero() || !assignmentRefs(h.EvidenceRefs, true) || !assignmentRefs(h.RemainingRefs, false) || !assignmentRefs(h.DependencyRefs, false) {
		return false
	}
	switch h.Availability {
	case "available", "busy", "waiting":
	default:
		return false
	}
	switch h.Blocker {
	case "none", "person", "dependency":
	default:
		return false
	}
	if len(h.NextOwner) > 256 || len(h.SuccessorRef) > 2048 || strings.ContainsAny(h.NextOwner+h.SuccessorRef, "\r\n\x00") {
		return false
	}
	switch h.Outcome {
	case "complete":
		return h.Blocker == "none" && len(h.RemainingRefs) == 0 && h.SuccessorRef == ""
	case "unfinished":
		return h.Blocker == "none" && len(h.RemainingRefs) > 0 && strings.TrimSpace(h.NextOwner) != "" && h.SuccessorRef == ""
	case "waiting":
		return h.Blocker != "none" && len(h.RemainingRefs) > 0 && len(h.DependencyRefs) > 0 && strings.TrimSpace(h.NextOwner) != "" && h.SuccessorRef == ""
	case "superseded":
		return h.Blocker == "none" && len(h.RemainingRefs) > 0 && strings.TrimSpace(h.NextOwner) != "" && strings.TrimSpace(h.SuccessorRef) != ""
	}
	return false
}
func ParseAssignmentHandoff(b []byte) (AssignmentHandoff, error) {
	var h AssignmentHandoff
	if len(b) > AssignmentInputLimit || decodeGoalJSON(b, &h) != nil || !ValidAssignmentHandoff(h) {
		return h, ErrAssignment
	}
	return h, nil
}
func assignmentAcknowledged(r AssignmentRow) bool {
	return r.Handoff != nil && r.Acknowledged == r.HandoffRevision && r.Acknowledged > 0
}
func assignmentComplete(r AssignmentRow) bool {
	return assignmentAcknowledged(r) && r.Handoff.Outcome == "complete"
}
func validAssignmentRevision(r AssignmentRevision) bool {
	if !validAssignmentWake(r.Wake, r.RecordedAt) || r.Revision == 0 || r.RecordedAt.IsZero() || !validAssignmentBinding(r.Binding) || !validAssignmentInventory(r.Inventory) || r.Rows == nil || len(r.Rows) > 200 || r.Inventory.ObservedAt.After(r.RecordedAt) {
		return false
	}
	ids, lanes := map[string]bool{}, map[string]bool{}
	for _, row := range r.Rows {
		if !validAssignmentSpec(row.Spec) || row.Revision == 0 || ids[row.Spec.ID] || lanes[row.Spec.Lane] {
			return false
		}
		ids[row.Spec.ID], lanes[row.Spec.Lane] = true, true
		if row.Handoff == nil {
			if row.HandoffRevision != 0 || row.Acknowledged != 0 {
				return false
			}
		} else {
			h := row.Handoff
			if !ValidAssignmentHandoff(*h) || h.Assignment != row.Spec.ID || h.AssignmentRevision != row.Revision || h.DeclaredBy != row.Spec.Lane || h.DeclaredAt.After(r.RecordedAt) || row.HandoffRevision == 0 || (row.Acknowledged != 0 && row.Acknowledged != row.HandoffRevision) {
				return false
			}
		}
	}
	// Partial observations still account for every lane that was observed.
	for _, l := range r.Inventory.Lanes {
		if !lanes[l.Thread] {
			return false
		}
	}
	return true
}
func assignmentTransition(a, b AssignmentRevision) bool {
	if !assignmentWakeTransition(a.Wake, b.Wake) || b.Revision != a.Revision+1 || b.RecordedAt.Before(a.RecordedAt) {
		return false
	}
	rows := map[string]AssignmentRow{}
	for _, r := range b.Rows {
		rows[r.Spec.ID] = r
	}
	for _, old := range a.Rows {
		next, ok := rows[old.Spec.ID]
		if ok && a.Binding != b.Binding && next.Revision != old.Revision+1 {
			return false
		}
		if !ok {
			if !assignmentComplete(old) {
				return false
			}
			continue
		}
		if next.Revision < old.Revision || next.Revision > old.Revision+1 {
			return false
		}
		if next.Revision == old.Revision {
			if !reflect.DeepEqual(old.Spec, next.Spec) {
				return false
			}
			if next.HandoffRevision < old.HandoffRevision || next.HandoffRevision > old.HandoffRevision+1 {
				return false
			}
			if next.HandoffRevision == old.HandoffRevision && !reflect.DeepEqual(old.Handoff, next.Handoff) {
				return false
			}
		} else if next.Handoff != nil || next.HandoffRevision != 0 || next.Acknowledged != 0 {
			return false
		}
	}
	return true
}
func LoadAssignments(dir string) (*AssignmentRecord, error) {
	path := filepath.Join(dir, AssignmentName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	b, err := ReadMigrationFile(path, AssignmentLimit)
	if err != nil {
		return nil, ErrAssignment
	}
	var r AssignmentRecord
	if decodeGoalJSON(b, &r) != nil || (r.Version != 1 && r.Version != 2) || !assignmentRecordID.MatchString(r.ID) || len(r.Revisions) == 0 || len(r.Revisions) > assignmentHistoryLimit {
		return nil, ErrAssignment
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, ErrAssignment
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, ErrAssignment
	}
	hasWake := false
	seenGrants := map[string]bool{}
	priorGrant := ""
	for n, rev := range r.Revisions {
		hasWake = hasWake || rev.Wake != nil
		if rev.Wake != nil && rev.Wake.ID != priorGrant {
			if seenGrants[rev.Wake.ID] {
				return nil, ErrAssignment
			}
			seenGrants[rev.Wake.ID] = true
			priorGrant = rev.Wake.ID
		}
		if (r.Version == 1 && rev.Wake != nil) || rev.Revision != uint64(n+1) || !validAssignmentRevision(rev) || rev.Binding.Runtime != canonical || !assignmentWakeDeclarationValid(r.Revisions, n, rev) || (n > 0 && !assignmentTransition(r.Revisions[n-1], rev)) {
			return nil, ErrAssignment
		}
	}
	if r.Version == 2 && !hasWake {
		return nil, ErrAssignment
	}
	return &r, nil
}
func AssignmentSummaryOf(r *AssignmentRecord, b AssignmentBinding) AssignmentSummary {
	if r == nil {
		return AssignmentSummary{Availability: "not_recorded"}
	}
	if len(r.Revisions) == 0 {
		return AssignmentSummary{Availability: "unavailable"}
	}
	x := r.Revisions[len(r.Revisions)-1]
	if x.Binding != b {
		return AssignmentSummary{Availability: "stale_binding"}
	}
	s := AssignmentSummary{Availability: "available", Revision: x.Revision, Coverage: x.Inventory.Coverage, ObservedAt: &x.Inventory.ObservedAt, RecordedAt: &x.RecordedAt, Counts: &AssignmentCounts{Assignments: len(x.Rows)}}
	present := map[string]bool{}
	for _, l := range x.Inventory.Lanes {
		present[l.Thread] = true
	}
	for _, row := range x.Rows {
		if !present[row.Spec.Lane] {
			s.Counts.Absent++
		}
		if !assignmentComplete(row) {
			s.Counts.Unfinished++
		}
		if row.Handoff == nil {
			s.Counts.Unknown++
		} else if !assignmentAcknowledged(row) {
			s.Counts.Unknown++
			s.Counts.Unacknowledged++
		} else if row.Handoff.Outcome == "waiting" {
			if row.Handoff.Blocker == "person" {
				s.Counts.PersonBlocked++
			} else {
				s.Counts.DependencyWaiting++
			}
		}
	}
	return s
}

// CheckAssignmentAwaitUser never clears a hold or declares the whole goal done.
// Caller holds tick.lock and control.lock; no external query is made here.
func CheckAssignmentAwaitUser(dir string, b AssignmentBinding) error {
	r, err := LoadAssignments(dir)
	if err != nil {
		return ErrAssignment
	}
	s := AssignmentSummaryOf(r, b)
	if s.Availability != "available" || s.Coverage != "observed_direct_children" || s.Counts.Unknown > 0 || s.Counts.Unfinished == 0 || s.Counts.Unfinished != s.Counts.PersonBlocked {
		return errors.New("assignment review does not establish that all remaining work needs a person; reconcile assignments or use pause/stop")
	}
	return nil
}

// WriteAssignments requires tick.lock and checked config source bytes. This is
// a coordinator declaration, not authenticated lane publication or dispatch.
func WriteAssignments(dir, caller string, b AssignmentBinding, expectedID string, expected uint64, change func(*AssignmentRevision) error) (*AssignmentRecord, error) {
	l, err := lock.AcquireNamed(dir, lock.Control)
	if err != nil {
		return nil, err
	}
	defer l.Release()
	canonical, canonicalErr := filepath.EvalSymlinks(dir)
	if canonicalErr != nil {
		return nil, ErrAssignment
	}
	canonical, canonicalErr = filepath.Abs(canonical)
	if canonicalErr != nil || b.Runtime != canonical {
		return nil, ErrAssignment
	}
	if CheckControlPath(dir) != nil {
		return nil, ErrAssignment
	}
	ctl, err := Load(dir)
	if err != nil {
		return nil, ErrAssignment
	}
	if err = CheckTransferReady(ctl); err != nil {
		return nil, err
	}
	if err = CheckOwner(ctl, caller, false); err != nil {
		return nil, err
	}
	if ctl.BBTarget != "" && ctl.BBTarget != b.Coordinator {
		return nil, ErrAssignment
	}
	if ctl.Ownership == nil || b.Owner != ctl.Ownership.ThreadID || b.Generation != ctl.Ownership.Generation {
		return nil, ErrAssignment
	}
	goal, err := LoadGoal(dir)
	if err != nil || goal == nil || goal.ID != b.Goal || goal.Summary().Revision != b.GoalRevision || goal.OwningLoop != b.Loop {
		return nil, ErrAssignment
	}
	r, err := LoadAssignments(dir)
	if err != nil {
		return nil, err
	}
	next := AssignmentRevision{Binding: b, Rows: []AssignmentRow{}}
	if r == nil {
		if expected != 0 || expectedID != "" {
			return nil, ErrAssignment
		}
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return nil, ErrAssignment
		}
		r = &AssignmentRecord{Version: 1, ID: "assignment_" + hex.EncodeToString(id[:]), Revisions: []AssignmentRevision{}}
	} else {
		if r.ID != expectedID || uint64(len(r.Revisions)) != expected {
			return nil, ErrAssignment
		}
		// Deep copy: callbacks cannot mutate retained history.
		raw, _ := json.Marshal(r.Revisions[len(r.Revisions)-1])
		_ = json.Unmarshal(raw, &next)
		next.Binding = b
	}
	if len(r.Revisions) >= assignmentHistoryLimit {
		return nil, errors.New("assignment history is full; no obligations were discarded")
	}
	if err = change(&next); err != nil {
		return nil, err
	}
	next.Revision = uint64(len(r.Revisions) + 1)
	next.RecordedAt = time.Now().UTC()
	if !validAssignmentRevision(next) || !assignmentWakeDeclarationValid(r.Revisions, len(r.Revisions), next) || (len(r.Revisions) > 0 && !assignmentTransition(r.Revisions[len(r.Revisions)-1], next)) {
		return nil, ErrAssignment
	}
	if next.Wake != nil {
		r.Version = 2
	}
	r.Revisions = append(r.Revisions, next)
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil || len(raw)+1 > AssignmentLimit {
		return nil, ErrAssignment
	}
	if err = writeAtomic(filepath.Join(dir, AssignmentName), append(raw, '\n'), 0600); err != nil {
		return nil, ErrAssignment
	}
	return r, nil
}

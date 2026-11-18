package supervise

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/prompt"
	"github.com/looptender/looptender/internal/state"
)

type assignmentDelivery struct {
	record      *control.AssignmentRecord
	binding     control.AssignmentBinding
	count       int
	disposition string
	expiresAt   time.Time
}

// No opt-in means no assignment read or added acquisition in supervision.
func (t *tick) assignmentObservation(a adapter.Adapter, obs decide.Observation) int {
	p := t.loop.AssignmentCoordination
	if p == nil || !p.Enabled || t.res.Target != state.Primary || t.loop.BB == nil {
		return 0
	}
	conversation, known := conversationKey(a)
	if !known || conversation != prompt.ConversationHash(t.loop.BB.Thread) {
		return 0
	}
	b, err := control.AssignmentBindingFor(t.loop.StateDir, t.cfg.Path(), t.loop.Name, t.loop.BB.Thread, t.ctl)
	if err != nil {
		return 0
	}
	r, err := control.LoadAssignments(t.loop.StateDir)
	if err != nil {
		return 0
	}
	n, s := control.AssignmentWakeLocal(r, b, t.assignmentNow(), p.MaxEvidenceAge.Duration)
	if r == nil || len(r.Revisions) == 0 {
		return 0
	}
	// A valid current-binding grant can be retired by an ordinary wake without
	// treating its stale/missing readiness evidence as authority for that wake.
	if !control.AssignmentWakeRetirable(r, b) {
		return 0
	}
	t.assignment = &assignmentDelivery{record: r, binding: b}
	if s != "declared_ready" {
		return 0
	}
	t.assignment.count = n
	latest := r.Revisions[len(r.Revisions)-1]
	t.assignment.expiresAt = latest.Wake.DeclaredAt.Add(p.MaxEvidenceAge.Duration)
	dates := []time.Time{latest.Inventory.ObservedAt}
	for _, row := range latest.Rows {
		dates = append(dates, row.Handoff.DeclaredAt)
	}
	for _, at := range dates {
		expiry := at.Add(p.MaxEvidenceAge.Duration)
		if expiry.Before(t.assignment.expiresAt) {
			t.assignment.expiresAt = expiry
		}
	}

	if !assignmentChildrenMatch(r, obs.DirectChildren) {
		return 0
	}
	return n
}
func assignmentChildrenMatch(r *control.AssignmentRecord, s *decide.DirectChildren) bool {
	if r == nil || s == nil || !s.Complete {
		return false
	}
	x := r.Revisions[len(r.Revisions)-1]
	if len(x.Inventory.Lanes) != len(s.States) {
		return false
	}
	for _, l := range x.Inventory.Lanes {
		if _, ok := s.States[l.Thread]; !ok || l.Archived {
			return false
		}
	}
	rows := map[string]control.AssignmentRow{}
	for _, row := range x.Rows {
		rows[row.Spec.ID] = row
	}
	for _, selection := range x.Wake.Selections {
		c, ok := s.States[rows[selection.Assignment].Spec.Lane]
		if !ok || c.Execution != "idle" || c.PendingInteraction {
			return false
		}
	}
	for _, c := range s.States {
		if c.PendingInteraction {
			return false
		}
	}
	return true
}

// prepareAssignment only performs the approved bounded optional scan. It never
// consumes a grant or borrows the mandatory send/report/cleanup reserve.
func (t *tick) prepareAssignment(a adapter.Adapter, d decide.Decision, m prompt.Message) (prompt.Message, bool) {
	if t.assignment == nil || !d.IdleWake {
		return m, true
	}
	verified := false
	reason := "assignment revalidation budget unavailable"
	if scan, ok := a.(interface {
		AssignmentChildren(context.Context) *decide.DirectChildren
	}); ok && t.assignment.count > 0 && t.left() >= 2*time.Second {
		ctx, cancel := context.WithTimeout(t.ctx, min(20*time.Second, t.left()-time.Second))
		fresh := scan.AssignmentChildren(ctx)
		verified = ctx.Err() == nil && fresh != nil && fresh.Complete && reflect.DeepEqual(fresh, t.decisionInput.Obs.DirectChildren) && assignmentChildrenMatch(t.assignment.record, fresh)
		cancel()
		reason = "assignment child evidence changed or unavailable"
		if verified && (t.assignment.expiresAt.IsZero() || t.assignmentNow().After(t.assignment.expiresAt)) {
			verified = false
			reason = "assignment readiness expired during revalidation"
		}
	}
	if t.ctx.Err() != nil || !t.routeChangeAllowed() || t.cfg.CheckSources() != nil {
		t.res.Action, t.res.Reason = string(decide.None), "assignment controls or sources changed; no wake sent"
		return m, false
	}
	if !verified && d.AssignmentWake {
		t.res.Action, t.res.Reason = string(decide.None), reason
		return m, false
	}
	t.assignment.disposition = "superseded_by_ordinary_wake_attempt"
	if verified {
		note := fmt.Sprintf("\nAssignment coordination: %d explicitly declared independent assignments have verified idle direct lanes at this check. Review the private assignment record, continue already-authorized independent work, and preserve healthy busy workers. Reconcile the wake attempt with assignment reconcile-wake. This notice is not new permission or a completion declaration.\n", t.assignment.count)
		if d.AssignmentWake {
			m = prompt.Message{Text: note, NoticeOnly: true}
			t.assignment.disposition = "assignment_notice_attempt"
		} else {
			m.Text += note
		}
	}
	// An ordinary wake proceeds without a current-assignment claim if optional
	// evidence failed. A still-valid local grant is retired conservatively below.
	return m, true
}

func (t *tick) consumeAssignment() error {
	x := t.assignment
	if x == nil || x.disposition == "" {
		return nil
	}
	if t.cfg.CheckSources() != nil || !t.routeChangeAllowed() {
		return control.ErrAssignment
	}
	c, err := control.Load(t.loop.StateDir)
	if err != nil {
		return err
	}
	b, err := control.AssignmentBindingFor(t.loop.StateDir, t.cfg.Path(), t.loop.Name, t.loop.BB.Thread, c)
	if err != nil || b != x.binding {
		return control.ErrAssignment
	}
	r := x.record
	return control.ConsumeAssignmentWake(t.loop.StateDir, t.opts.CallerThread, b, r.ID, uint64(len(r.Revisions)), r.Revisions[len(r.Revisions)-1].Wake.ID, x.disposition, t.assignmentNow(), t.loop.AssignmentCoordination.MaxEvidenceAge.Duration)
}

// All acquisition time counts toward freshness, including the initial Observe
// before assignment eligibility. Clock is monotonic elapsed, Now is check time.
func (t *tick) assignmentNow() time.Time {
	return t.now.Add(max(0, t.opts.Clock().Sub(t.startClock)))
}

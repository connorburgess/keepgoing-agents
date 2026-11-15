package decide

import (
	"testing"
	"time"
)

// Child work suppresses wakes, nudges and switch-backs of a parent that is not
// in error, and never overrides a control. An errored parent is recovered
// whatever its children do; TestErroredParentIsRecoveredWhateverItsChildrenDo
// has the full table.
func TestChildrenDoNotOverrideControlsOrPermitWakes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		want   Action
		reason string
	}{
		{"idle", func(*Input) {}, None, "child work running"},
		{"stalled", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = time.Hour; i.Nudges, i.MaxNudges = 3, 3 }, None, "child work running"},
		{"unknown_state", func(i *Input) { i.Obs.State = Unknown }, None, "child work running"},
		{"pending", func(i *Input) { i.Obs.State = Pending }, None, "child work running"},
		{"errored_parent_is_reprompted", func(i *Input) { i.Obs.State = Error }, Reprompt, "error: unknown"},
		{"errored_parent_hands_off", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true}
		}, Handoff, "error: rate_limit"},
		{"switch_back", func(i *Input) { i.SwitchBackReady = true }, None, "child work running"},
		{"unknown_children", func(i *Input) { i.Obs.ActiveChildren = 0; i.Obs.ChildrenUnverified = true }, None, "child status unverified"},
		{"stop_active", func(i *Input) { i.Control.Stop = true; i.Obs.State = Active }, Stop, "stop requested"},
		{"stop_unverified", func(i *Input) {
			i.Control.Stop = true
			i.Obs.State = Active
			i.Obs.ActiveChildren = 0
			i.Obs.ChildrenUnverified = true
		}, Stop, "stop requested"},
		{"stop_idle_does_not_stop_child", func(i *Input) { i.Control.Stop = true }, None, "stop requested; session not running"},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, None, "disabled"},
		{"paused", func(i *Input) { i.Control.Paused = true }, None, "paused"},
		{"completed", func(i *Input) { i.Control.Completed = true }, None, "completed"},
		{"awaiting_user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
		{"parent_interaction", func(i *Input) { i.Obs.AwaitingInput = true }, None, "waiting on a person (pending interaction)"},
		{"gone", func(i *Input) { i.Obs.Gone = true }, AwaitUser, "target gone (archived, deleted, or not found)"},
		{"session lost", func(i *Input) { i.Obs.SessionLost = true }, AwaitUser, SessionLostReason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs.ActiveChildren = 1
			tc.mut(&in)
			if d := Decide(in); d.Action != tc.want || d.Reason != tc.reason {
				t.Fatalf("%+v", d)
			}
		})
	}
}

func TestChildInputEscalationPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Input)
		want Action
	}{
		{"idle", func(*Input) {}, AwaitUser},
		{"active_child", func(i *Input) { i.Obs.ActiveChildren = 1 }, AwaitUser},
		{"unverified_sibling", func(i *Input) { i.Obs.ChildrenUnverified = true }, AwaitUser},
		{"parent_input", func(i *Input) { i.Obs.AwaitingInput = true }, AwaitUser},
		{"stalled", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = time.Hour }, AwaitUser},
		{"handoff", func(i *Input) {
			i.Obs.State = Error
			i.Obs.ErrorClass = ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true}
		}, AwaitUser},
		{"switchback", func(i *Input) { i.SwitchBackReady = true }, AwaitUser},
		{"outside_window", func(i *Input) { i.InWindow = false }, AwaitUser},
		{"stop_active", func(i *Input) { i.Control.Stop = true; i.Obs.State = Active }, Stop},
		{"stop_idle", func(i *Input) { i.Control.Stop = true }, None},
		{"pause", func(i *Input) { i.Control.Paused = true }, None},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, None},
		{"completed", func(i *Input) { i.Control.Completed = true }, None},
		{"await_user", func(i *Input) { i.Control.AwaitingUser = true }, None},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs.AwaitingChildren = []string{"thr_child"}
			tc.mut(&in)
			d := Decide(in)
			if d.Action != tc.want {
				t.Fatalf("%+v", d)
			}
			if tc.want == AwaitUser && d.Reason != "children waiting on a person (pending interaction): thr_child" {
				t.Fatalf("missing child: %+v", d)
			}
		})
	}
}

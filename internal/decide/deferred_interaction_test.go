package decide

import (
	"testing"
	"time"
)

func TestDeferredInteractionHoldsWithoutOverridingControls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		want   Action
		reason string
	}{
		{"idle", func(*Input) {}, AwaitUser, DeferredInteractionReason},
		{"unknown", func(i *Input) { i.Obs.State = Unknown }, AwaitUser, DeferredInteractionReason},
		{"pending", func(i *Input) { i.Obs.State = Pending }, AwaitUser, DeferredInteractionReason},
		{"stalled", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = time.Hour }, AwaitUser, DeferredInteractionReason},
		{"reprompt", func(i *Input) { i.Obs.State = Error }, AwaitUser, DeferredInteractionReason},
		{"handoff", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true}
		}, AwaitUser, DeferredInteractionReason},
		{"switch_back", func(i *Input) { i.SwitchBackReady = true }, AwaitUser, DeferredInteractionReason},
		{"outside_window", func(i *Input) { i.InWindow = false }, AwaitUser, DeferredInteractionReason},
		{"still_pending_interaction", func(i *Input) { i.Obs.AwaitingInput = true }, AwaitUser, DeferredInteractionReason},
		{"active_child", func(i *Input) { i.Obs.ActiveChildren = 1 }, AwaitUser, DeferredInteractionReason},
		{"unverified_children", func(i *Input) { i.Obs.ChildrenUnverified = true }, AwaitUser, DeferredInteractionReason},
		{"provider_retrying", func(i *Input) { i.Obs.Retrying = true }, AwaitUser, DeferredInteractionReason},
		{"retry_unverified", func(i *Input) { i.Obs.RetryUnverified = true }, AwaitUser, DeferredInteractionReason},
		{"queued", func(i *Input) { i.Obs.Queued = 1 }, AwaitUser, DeferredInteractionReason},
		{"child_input_first", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_child"} }, AwaitUser, "children waiting on a person (pending interaction): thr_child"},
		{"gone_first", func(i *Input) { i.Obs.Gone = true }, AwaitUser, "target gone (archived, deleted, or not found)"},
		{"stop_active", func(i *Input) { i.Control.Stop = true; i.Obs.State = Active }, Stop, "stop requested"},
		{"stop_pending", func(i *Input) { i.Control.Stop = true; i.Obs.State = Pending }, Stop, "stop requested"},
		{"stop_idle", func(i *Input) { i.Control.Stop = true }, None, "stop requested; session not running"},
		{"stop_while_paused", func(i *Input) { i.Control.Stop, i.Control.Paused = true, true; i.Obs.State = Active }, Stop, "stop requested"},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, None, "disabled"},
		{"paused", func(i *Input) { i.Control.Paused = true }, None, "paused"},
		{"already_awaiting_user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
		{"completed", func(i *Input) { i.Control.Completed = true }, None, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs.DeferredInteraction = true
			tc.mut(&in)
			d := Decide(in)
			if d.Action != tc.want || d.Reason != tc.reason {
				t.Fatalf("%+v", d)
			}
			// The typed marker is set by this rule only, never by a rule that outranks it.
			if d.DeferredInteraction != (tc.reason == DeferredInteractionReason) {
				t.Fatalf("typed cause: %+v", d)
			}
			// No rule may turn this evidence into provider work.
			switch Decide(in).Action {
			case Wake, Nudge, Reprompt, Handoff, SwitchBack:
				t.Fatal("deferred interaction produced a send")
			}
		})
	}
	// The field is the only trigger: the same inputs without it keep their
	// existing outcomes, including a still-pending interaction's plain wait.
	in := base()
	if d := Decide(in); d.Action != Wake || d.DeferredInteraction {
		t.Fatalf("%+v", d)
	}
	in.Obs.AwaitingInput = true
	if d := Decide(in); d.Action != None || d.Reason != "waiting on a person (pending interaction)" {
		t.Fatalf("%+v", d)
	}
}

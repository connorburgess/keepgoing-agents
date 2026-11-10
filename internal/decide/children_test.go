package decide

import (
	"testing"
	"time"
)

func TestChildrenDoNotOverrideControlsOrPermitRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		want   Action
		reason string
	}{
		{"idle", func(*Input) {}, None, "child work running"},
		{"stalled", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = time.Hour; i.Nudges, i.MaxNudges = 3, 3 }, None, "child work running"},
		{"reprompt", func(i *Input) { i.Obs.State = Error }, None, "child work running"},
		{"handoff", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true}
		}, None, "child work running"},
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

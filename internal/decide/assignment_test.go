package decide

import (
	"testing"
	"time"
)

func TestAssignmentWakeOnlyBypassesBusyChildGate(t *testing.T) {
	base := Input{Control: Control{Enabled: true}, Obs: Observation{State: Idle, ActiveChildren: 1}, AssignmentReady: 2, InWindow: true, SinceWake: time.Hour, MinWakeInterval: time.Minute}
	for _, tc := range []struct {
		name       string
		change     func(*Input)
		want       Action
		assignment bool
	}{
		{"independent", func(*Input) {}, Wake, true},
		{"no-readiness", func(i *Input) { i.AssignmentReady = 0 }, None, false},
		{"ordinary", func(i *Input) { i.Obs.ActiveChildren = 0 }, Wake, false},
		{"paused", func(i *Input) { i.Control.Paused = true }, None, false},
		{"stopped", func(i *Input) { i.Control.Stop = true }, None, false},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, None, false},
		{"awaiting-person", func(i *Input) { i.Control.AwaitingUser = true }, None, false},
		{"completed", func(i *Input) { i.Control.Completed = true }, None, false},
		{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser, false},
		{"send-limit", func(i *Input) { i.SendLimitReached = true }, AwaitUser, false},
		{"no-progress", func(i *Input) { i.NoProgressReached = true }, AwaitUser, false},
		{"window", func(i *Input) { i.InWindow = false }, None, false},
		{"network", func(i *Input) { i.NetworkDown = true }, None, false},
		{"spacing", func(i *Input) { i.SinceWake = 0 }, None, false},
		{"partial", func(i *Input) { i.Obs.ChildrenUnverified = true }, None, false},
		{"queued", func(i *Input) { i.Obs.Queued = 1 }, None, false},
		{"background", func(i *Input) { i.Obs.Background = 1 }, None, false},
		{"retry", func(i *Input) { i.Obs.Retrying = true }, None, false},
		{"retry-unknown", func(i *Input) { i.Obs.RetryUnverified = true }, None, false},
		{"person", func(i *Input) { i.Obs.AwaitingInput = true }, None, false},
		{"child-person", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_example_child"} }, AwaitUser, false},
		{"active-parent", func(i *Input) { i.Obs.State = Active }, None, false},
		{"unknown-parent", func(i *Input) { i.Obs.State = Unknown }, None, false},
		{"pending-parent", func(i *Input) { i.Obs.State = Pending }, None, false},
		{"stopping-parent", func(i *Input) { i.Obs.State = Stopping }, None, false},
		{"no-switch-back", func(i *Input) { i.SwitchBackReady = true }, Wake, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			d := Decide(in)
			if d.Action != tc.want || d.AssignmentWake != tc.assignment {
				t.Fatal(d)
			}
		})
	}
}

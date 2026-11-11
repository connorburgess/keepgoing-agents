package decide

import (
	"testing"
	"time"
)

func TestRetryPrecedence(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			mut    func(*Input)
			action Action
			reason string
		}{
			{"active", func(*Input) {}, None, ""},
			{"idle", func(i *Input) { i.Obs.State = Idle }, None, ""},
			{"error", func(i *Input) {
				i.Obs.State = Error
				i.Obs.ErrorClass = ErrRateLimit
				i.Failover = Failover{Eligible: true, Available: true}
			}, None, ""},
			{"stop", func(i *Input) { i.Control.Stop = true }, Stop, "stop requested"},
			{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
			{"disabled", func(i *Input) { i.Control.Enabled = false }, None, "disabled"},
			{"completed", func(i *Input) { i.Control.Completed = true }, None, "completed"},
			{"await_user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
			{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser, ""},
			{"send_cap", func(i *Input) { i.SendLimitReached = true }, AwaitUser, ""},
			{"git_limit", func(i *Input) { i.NoProgressReached = true }, AwaitUser, ""},
			{"child_input", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_child"} }, AwaitUser, ""},
		} {
			t.Run(tc.name+map[bool]string{false: "_retry", true: "_unknown"}[uncertain], func(t *testing.T) {
				in := base()
				in.Obs.State = Active
				in.Obs.Retrying = !uncertain
				in.Obs.RetryUnverified = uncertain
				in.UnchangedFor = 24 * time.Hour
				in.Nudges = 3
				in.MaxNudges = 3
				in.SwitchBackReady = true
				tc.mut(&in)
				d := Decide(in)
				if d.Action != tc.action || tc.reason != "" && d.Reason != tc.reason {
					t.Fatalf("%+v", d)
				}
			})
		}
	}
}

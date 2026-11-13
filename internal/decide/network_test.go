package decide

import (
	"testing"
	"time"
)

// A failed connectivity check stops everything an errored session would
// otherwise spend on recovery, and nothing else. It never raises a hold, and
// it does not outrank the controls, the loop-wide limits or a queued message.
func TestNetworkDownWaitsInsteadOfSpendingRecovery(t *testing.T) {
	m := time.Minute
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		action Action
		reason string
	}{
		{"unknown error", func(*Input) {}, None, NetworkDownReason},
		{"network error with a fallback", func(i *Input) {
			i.Obs.ErrorClass, i.Failover = ErrNetwork, Failover{Eligible: true, Available: true}
		}, None, NetworkDownReason},
		{"rate limit with a fallback", func(i *Input) {
			i.Obs.ErrorClass, i.Failover = ErrRateLimit, Failover{Eligible: true, Available: true}
		}, None, NetworkDownReason},
		{"auth error is not held while offline", func(i *Input) { i.Obs.ErrorClass = ErrAuth }, None, NetworkDownReason},
		{"re-prompt cap reached is not held while offline", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 3, 60*m }, None, NetworkDownReason},
		{"backing off", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 1, m }, None, NetworkDownReason},
		{"queued message", func(i *Input) { i.Obs.Queued = 1 }, None, "messages already queued after error"},
		{"provider retrying", func(i *Input) { i.Obs.Retrying = true }, None, "provider is retrying the turn"},
		{"stop", func(i *Input) { i.Control.Stop = true }, None, "stop requested; session not running"},
		{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
		{"awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
		{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser, ""},
		{"send limit", func(i *Input) { i.SendLimitReached = true }, AwaitUser, ""},
		{"child waiting on a person", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_example_a"} }, AwaitUser, ""},
		{"pending interaction", func(i *Input) { i.Obs.AwaitingInput = true }, None, "waiting on a person (pending interaction)"},
		{"active child", func(i *Input) { i.Obs.ActiveChildren = 1 }, None, "child work running"},
		{"outside window", func(i *Input) { i.InWindow = false }, None, "outside window"},
		{"idle session is still woken", func(i *Input) { i.Obs.State = Idle }, Wake, "idle"},
		{"active session is unaffected", func(i *Input) { i.Obs.State = Active }, None, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs = Observation{State: Error, ErrorClass: ErrUnknown}
			in.NetworkDown = true
			tc.mut(&in)
			d := Decide(in)
			if d.Action != tc.action || tc.reason != "" && d.Reason != tc.reason {
				t.Fatalf("%+v", d)
			}
			if d.Action == Reprompt || d.Action == Handoff {
				t.Fatalf("recovery spent into a dead network: %+v", d)
			}
		})
	}
	// With the network up the same inputs recover as before.
	in := base()
	in.Obs = Observation{State: Error, ErrorClass: ErrUnknown}
	if d := Decide(in); d.Action != Reprompt {
		t.Fatalf("%+v", d)
	}
}

package decide

import (
	"testing"
	"time"
)

// A provider that refused or restricted the request has decided something
// about the work or the account. That is not a limit: it holds for a person
// and never moves to another route, however the loop is configured, and it is
// never prompted again.
func TestRestrictedHoldsAndNeverMoves(t *testing.T) {
	m := time.Minute
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		action Action
		reason string
	}{
		{"no fallbacks", func(*Input) {}, AwaitUser, RestrictedReason},
		{"confirmed fallback available", func(i *Input) {
			i.Failover = Failover{Eligible: true, Available: true}
		}, AwaitUser, RestrictedReason},
		{"fallback available and a reset reported", func(i *Input) {
			i.Failover = Failover{Eligible: true, Available: true, ResetIn: 5 * m, WaitIfResetWithin: 10 * m}
		}, AwaitUser, RestrictedReason},
		{"re-prompts left", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 1, 60*m }, AwaitUser, RestrictedReason},
		{"switch-back ready", func(i *Input) { i.SwitchBackReady = true }, AwaitUser, RestrictedReason},
		{"stop", func(i *Input) { i.Control.Stop = true }, None, "stop requested; session not running"},
		{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
		{"already awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
		{"network down waits", func(i *Input) { i.NetworkDown = true }, None, NetworkDownReason},
		{"queued message", func(i *Input) { i.Obs.Queued = 1 }, None, "messages already queued after error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs.State, in.Obs.ErrorClass = Error, ErrRestricted
			tc.mut(&in)
			d := Decide(in)
			if d.Action != tc.action || d.Reason != tc.reason {
				t.Fatalf("got %s (%q), want %s (%q)", d.Action, d.Reason, tc.action, tc.reason)
			}
		})
	}
}

// Whatever else is true of the input, a restricted session never produces a
// handoff, a switch-back, a re-prompt, a wake or a nudge.
func TestRestrictedNeverSendsOrMovesUnderAnyFailoverInput(t *testing.T) {
	for _, eligible := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			for _, deferred := range []bool{false, true} {
				for _, reprompts := range []int{0, 1, 3} {
					in := base()
					in.Obs.State, in.Obs.ErrorClass = Error, ErrRestricted
					in.Failover = Failover{Eligible: eligible, Available: available}
					in.HandoffDeferred, in.Reprompts, in.SinceLastReprompt = deferred, reprompts, time.Hour
					in.SwitchBackReady = true
					if d := Decide(in); d.Action != AwaitUser {
						t.Fatalf("eligible=%v available=%v deferred=%v reprompts=%d: %s (%q)", eligible, available, deferred, reprompts, d.Action, d.Reason)
					}
				}
			}
		}
	}
	if got := RecoverBy(Decision{Action: Reprompt}, Observation{State: Error, ErrorClass: ErrRestricted}, true); got != RecoverByPrompt {
		t.Fatalf("a restricted session must never have its turn retried: %s", got)
	}
}

package decide

import (
	"testing"
	"time"
)

var declinedClasses = []struct {
	class  ErrorClass
	reason string
}{{ErrRestricted, RestrictedReason}, {ErrRefused, RefusedReason}}

// A provider that denied permission, or a model or provider that declined the
// turn, has decided something about the account or the work. That is not a
// limit: it holds for a person, with its own fixed reason, and never moves to
// another route however the loop is configured. keepgoing never prompts it
// again by itself.
func TestDeclinedHoldsAndNeverMoves(t *testing.T) {
	m := time.Minute
	for _, dc := range declinedClasses {
		for _, tc := range []struct {
			name   string
			mut    func(*Input)
			action Action
			reason string
		}{
			{"no fallbacks", func(*Input) {}, AwaitUser, dc.reason},
			{"confirmed fallback available", func(i *Input) {
				i.Failover = Failover{Eligible: true, Available: true}
			}, AwaitUser, dc.reason},
			{"fallback available and a reset reported", func(i *Input) {
				i.Failover = Failover{Eligible: true, Available: true, ResetIn: 5 * m, WaitIfResetWithin: 10 * m}
			}, AwaitUser, dc.reason},
			{"re-prompts left", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 1, 60*m }, AwaitUser, dc.reason},
			{"switch-back ready", func(i *Input) { i.SwitchBackReady = true }, AwaitUser, dc.reason},
			{"stop", func(i *Input) { i.Control.Stop = true }, None, "stop requested; session not running"},
			{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
			{"already awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
			{"network down waits", func(i *Input) { i.NetworkDown = true }, None, NetworkDownReason},
			{"queued message", func(i *Input) { i.Obs.Queued = 1 }, None, "messages already queued after error"},
		} {
			t.Run(string(dc.class)+"/"+tc.name, func(t *testing.T) {
				in := base()
				in.Obs.State, in.Obs.ErrorClass = Error, dc.class
				tc.mut(&in)
				d := Decide(in)
				if d.Action != tc.action || d.Reason != tc.reason {
					t.Fatalf("got %s (%q), want %s (%q)", d.Action, d.Reason, tc.action, tc.reason)
				}
			})
		}
	}
	if RestrictedReason == RefusedReason {
		t.Fatal("a permission problem and a declined turn must not read the same")
	}
}

// Whatever else is true of the input, a declined session never produces a
// handoff, a switch-back, a wake or a nudge, and is re-prompted only when a
// person has resumed the loop that was held for it.
func TestDeclinedNeverMovesAndIsRepromptedOnlyOnAPersonsResume(t *testing.T) {
	for _, dc := range declinedClasses {
		for _, eligible := range []bool{false, true} {
			for _, available := range []bool{false, true} {
				for _, deferred := range []bool{false, true} {
					for _, reprompts := range []int{0, 1, 3, 30} {
						for _, acknowledged := range []bool{false, true} {
							in := base()
							in.Obs.State, in.Obs.ErrorClass = Error, dc.class
							in.Failover = Failover{Eligible: eligible, Available: available}
							in.HandoffDeferred, in.Reprompts, in.SinceLastReprompt = deferred, reprompts, time.Second
							in.SwitchBackReady, in.DeclineAcknowledged = true, acknowledged
							d := Decide(in)
							want, reason := AwaitUser, dc.reason
							if acknowledged {
								// One try, outside the cap and backoff that bound
								// keepgoing's own attempts.
								want, reason = Reprompt, DeclineAcknowledgedReason
							}
							if d.Action != want || d.Reason != reason {
								t.Fatalf("%s eligible=%v available=%v deferred=%v reprompts=%d acknowledged=%v: %s (%q)", dc.class, eligible, available, deferred, reprompts, acknowledged, d.Action, d.Reason)
							}
						}
					}
				}
			}
		}
		if got := RecoverBy(Decision{Action: Reprompt}, Observation{State: Error, ErrorClass: dc.class}, true); got != RecoverByPrompt {
			t.Fatalf("%s: a declined turn must never be retried by reference: %s", dc.class, got)
		}
	}
}

// A resume does not outrank the controls, the loop-wide limits, a dead
// network or a queued message, and means nothing for any other class.
func TestDeclineAcknowledgementIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Input)
		want Action
	}{
		{"stop", func(i *Input) { i.Control.Stop = true }, None},
		{"pause", func(i *Input) { i.Control.Paused = true }, None},
		{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser},
		{"send limit", func(i *Input) { i.SendLimitReached = true }, AwaitUser},
		{"network down", func(i *Input) { i.NetworkDown = true }, None},
		{"queued message", func(i *Input) { i.Obs.Queued = 1 }, None},
		{"outside window", func(i *Input) { i.InWindow = false }, None},
		{"an auth error still holds", func(i *Input) { i.Obs.ErrorClass = ErrAuth }, AwaitUser},
		{"a spent cap on an unknown error still holds", func(i *Input) { i.Obs.ErrorClass, i.Reprompts = ErrUnknown, 3 }, AwaitUser},
		{"an idle session is woken as usual", func(i *Input) { i.Obs.State, i.Obs.ErrorClass = Idle, ErrNone }, Wake},
	} {
		in := base()
		in.Obs.State, in.Obs.ErrorClass, in.DeclineAcknowledged = Error, ErrRefused, true
		tc.mut(&in)
		if d := Decide(in); d.Action != tc.want || d.Reason == DeclineAcknowledgedReason {
			t.Errorf("%s: %s (%q), want %s", tc.name, d.Action, d.Reason, tc.want)
		}
	}
}

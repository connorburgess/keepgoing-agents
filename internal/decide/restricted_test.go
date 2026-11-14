package decide

import (
	"testing"
	"time"
)

// A restricted turn is the provider denying permission: it holds for a
// person at once and never moves, however the loop is configured.
func TestRestrictedHoldsAndNeverMoves(t *testing.T) {
	m := time.Minute
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		action Action
		reason string
	}{
		{"no fallbacks", func(*Input) {}, AwaitUser, RestrictedReason},
		{"confirmed fallback available", func(i *Input) { i.Failover = Failover{Eligible: true, Available: true} }, AwaitUser, RestrictedReason},
		{"fallback available and a reset reported", func(i *Input) {
			i.Failover = Failover{Eligible: true, Available: true, ResetIn: 5 * m, WaitIfResetWithin: 10 * m}
		}, AwaitUser, RestrictedReason},
		{"re-prompts left", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 1, 60*m }, AwaitUser, RestrictedReason},
		{"switch-back ready", func(i *Input) { i.SwitchBackReady = true }, AwaitUser, RestrictedReason},
		{"a person resumed", func(i *Input) { i.DeclineAcknowledged = true }, Reprompt, DeclineAcknowledgedReason},
		{"stop", func(i *Input) { i.Control.Stop = true }, None, "stop requested; session not running"},
		{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
		{"already awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
		{"network down waits", func(i *Input) { i.NetworkDown = true }, None, NetworkDownReason},
		{"queued message", func(i *Input) { i.Obs.Queued = 1 }, None, "messages already queued after error"},
	} {
		in := base()
		in.Obs.State, in.Obs.ErrorClass = Error, ErrRestricted
		tc.mut(&in)
		if d := Decide(in); d.Action != tc.action || d.Reason != tc.reason {
			t.Errorf("%s: got %s (%q), want %s (%q)", tc.name, d.Action, d.Reason, tc.action, tc.reason)
		}
	}
}

// A refused turn gets a plain continue in the same session, spaced by the
// streak, with no per-episode cap; never a handoff, a switch-back or a turn
// retry. At refusal.ask_after in a row it asks a person instead.
func TestRefusedContinuesWithSpacingAndNeverMoves(t *testing.T) {
	m := time.Minute
	for _, tc := range []struct {
		streak int
		since  time.Duration
		want   Action
	}{
		{1, 30 * time.Second, None}, {1, m, Reprompt},
		{2, 90 * time.Second, None}, {2, 2 * m, Reprompt},
		{3, 4 * m, None}, {3, 5 * m, Reprompt},
		{4, 9 * m, None}, {4, 10 * m, Reprompt},
		{40, 9 * m, None}, {40, 10 * m, Reprompt},
	} {
		in := base()
		in.Obs.State, in.Obs.ErrorClass = Error, ErrRefused
		in.RefusalStreak, in.SinceRefusal = tc.streak, tc.since
		d := Decide(in)
		if d.Action != tc.want {
			t.Errorf("streak %d after %s: %s (%q), want %s", tc.streak, tc.since, d.Action, d.Reason, tc.want)
		}
		want := RefusalWaitReason
		if tc.want == Reprompt {
			want = RefusedContinueReason
		}
		if d.Reason != want {
			t.Errorf("streak %d after %s: reason %q", tc.streak, tc.since, d.Reason)
		}
	}
	// Whatever else is true, it never moves, and the re-prompt cap does not stop it.
	for _, eligible := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			for _, reprompts := range []int{0, 3, 300} {
				in := base()
				in.Obs.State, in.Obs.ErrorClass = Error, ErrRefused
				in.Failover = Failover{Eligible: eligible, Available: available}
				in.Reprompts, in.SwitchBackReady, in.RefusalStreak, in.SinceRefusal = reprompts, true, 7, time.Hour
				if d := Decide(in); d.Action != Reprompt || d.Reason != RefusedContinueReason {
					t.Fatalf("eligible=%v available=%v reprompts=%d: %s (%q)", eligible, available, reprompts, d.Action, d.Reason)
				}
			}
		}
	}
	if got := RecoverBy(Decision{Action: Reprompt, Reason: RefusedContinueReason}, Observation{State: Error, ErrorClass: ErrRefused}, true); got != RecoverByPrompt {
		t.Fatalf("a refused turn is continued with the prompt, never retried by reference: %s", got)
	}
	// The threshold that asks a person; 0 never asks.
	for _, tc := range []struct {
		askAfter, streak int
		want             Action
	}{{10, 9, Reprompt}, {10, 10, AwaitUser}, {10, 25, AwaitUser}, {0, 500, Reprompt}, {1, 1, AwaitUser}} {
		in := base()
		in.Obs.State, in.Obs.ErrorClass = Error, ErrRefused
		in.RefusalAskAfter, in.RefusalStreak, in.SinceRefusal = tc.askAfter, tc.streak, time.Hour
		d := Decide(in)
		if d.Action != tc.want || (tc.want == AwaitUser && d.Reason != RefusalAskReason) {
			t.Errorf("ask_after %d at streak %d: %s (%q)", tc.askAfter, tc.streak, d.Action, d.Reason)
		}
	}
}

// Controls and loop-wide limits come before a continue.
func TestRefusedContinueYieldsToControlsAndLimits(t *testing.T) {
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
	} {
		in := base()
		in.Obs.State, in.Obs.ErrorClass = Error, ErrRefused
		in.RefusalStreak, in.SinceRefusal = 1, time.Hour
		tc.mut(&in)
		if d := Decide(in); d.Action != tc.want || d.Reason == RefusedContinueReason {
			t.Errorf("%s: %s (%q), want %s", tc.name, d.Action, d.Reason, tc.want)
		}
	}
	if RefusalSpacing(0) != time.Minute || RefusalSpacing(1) != time.Minute || RefusalSpacing(2) != 2*time.Minute || RefusalSpacing(3) != 5*time.Minute || RefusalSpacing(4) != 10*time.Minute || RefusalSpacing(1000) != 10*time.Minute {
		t.Fatal("spacing")
	}
}

// A person's resume is narrow: it outranks nothing else, and means nothing
// for any other class.
func TestDeclineAcknowledgementIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Input)
		want Action
	}{
		{"stop", func(i *Input) { i.Control.Stop = true }, None},
		{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser},
		{"network down", func(i *Input) { i.NetworkDown = true }, None},
		{"an auth error still holds", func(i *Input) { i.Obs.ErrorClass = ErrAuth }, AwaitUser},
		{"an idle session is woken as usual", func(i *Input) { i.Obs.State, i.Obs.ErrorClass = Idle, ErrNone }, Wake},
	} {
		in := base()
		in.Obs.State, in.Obs.ErrorClass, in.DeclineAcknowledged = Error, ErrRestricted, true
		tc.mut(&in)
		if d := Decide(in); d.Action != tc.want || d.Reason == DeclineAcknowledgedReason {
			t.Errorf("%s: %s (%q), want %s", tc.name, d.Action, d.Reason, tc.want)
		}
	}
}

// While a refusal on this route is unrecovered, no error moves the work: a
// limit with a reported reset is waited out in place, and any other error
// gets the in-place rules. With no refusal outstanding the same inputs hand
// off as before.
func TestNothingMovesWhileARefusalIsUnrecovered(t *testing.T) {
	m := time.Minute
	for _, tc := range []struct {
		name   string
		class  ErrorClass
		fo     Failover
		streak int
		action Action
		wait   LimitWait
	}{
		{"limit, fallback available", ErrRateLimit, Failover{Eligible: true, Available: true}, 1, Reprompt, ""},
		{"limit with a reset, fallback available", ErrRateLimit, Failover{Eligible: true, Available: true, ResetIn: 3 * time.Hour, WaitIfResetWithin: 30 * m}, 2, None, WaitRefusalUnrecovered},
		{"limit with a reset soon", ErrRateLimit, Failover{Eligible: true, Available: true, ResetIn: 5 * m, WaitIfResetWithin: 30 * m}, 1, None, WaitRefusalUnrecovered},
		{"limit with a reset, no fallback", ErrRateLimit, Failover{Eligible: true, ResetIn: 3 * time.Hour}, 1, None, WaitRefusalUnrecovered},
		{"overloaded, fallback available", ErrOverloaded, Failover{Eligible: true, Available: true}, 1, Reprompt, ""},
		{"auth, fallback available", ErrAuth, Failover{Eligible: true, Available: true}, 1, AwaitUser, ""},
		{"recovered: limit hands off again", ErrRateLimit, Failover{Eligible: true, Available: true}, 0, Handoff, ""},
		{"recovered: limit with a reset hands off again", ErrRateLimit, Failover{Eligible: true, Available: true, ResetIn: 3 * time.Hour, WaitIfResetWithin: 30 * m}, 0, Handoff, ""},
	} {
		in := base()
		in.Obs.State, in.Obs.ErrorClass = Error, tc.class
		in.Failover, in.RefusalStreak = tc.fo, tc.streak
		if d := Decide(in); d.Action != tc.action || d.LimitWait != tc.wait {
			t.Errorf("%s: got %s %q (%q)", tc.name, d.Action, d.LimitWait, d.Reason)
		}
	}
}

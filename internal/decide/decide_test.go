package decide

import (
	"strings"
	"testing"
	"time"
)

func base() Input {
	return Input{
		Obs:             Observation{State: Idle, ErrorClass: ErrNone},
		Control:         Control{Enabled: true},
		InWindow:        true,
		StallAfter:      30 * time.Minute,
		MinWakeInterval: 60 * time.Minute,
		SinceWake:       24 * time.Hour,
		MaxReprompts:    3,
		Backoff:         10 * time.Minute,
	}
}

func TestDecide(t *testing.T) {
	m := time.Minute
	cases := []struct {
		name string
		mut  func(*Input)
		want Action
	}{
		{"idle wakes", func(*Input) {}, Wake},
		{"stop wins while active", func(i *Input) { i.Control.Stop = true; i.Obs.State = Active }, Stop},
		{"stop while idle does nothing", func(i *Input) { i.Control.Stop = true }, None},
		{"stop wins over pause", func(i *Input) { i.Control.Stop = true; i.Control.Paused = true; i.Obs.State = Active }, Stop},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, None},
		{"paused", func(i *Input) { i.Control.Paused = true }, None},
		{"completed blocks idle wake", func(i *Input) { i.Control.Completed = true }, None},
		{"completed blocks switch-back", func(i *Input) { i.Control.Completed, i.SwitchBackReady = true, true }, None},
		{"completed blocks handoff", func(i *Input) {
			i.Control.Completed = true
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true}
		}, None},
		{"completed blocks reprompt", func(i *Input) { i.Control.Completed = true; i.Obs.State = Error }, None},
		{"completed blocks nudge", func(i *Input) { i.Control.Completed = true; i.Obs.State, i.UnchangedFor = Active, time.Hour }, None},
		{"stop wins over completion", func(i *Input) { i.Control.Stop, i.Control.Completed = true, true; i.Obs.State = Active }, Stop},
		{"awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, None},
		{"target gone needs a person", func(i *Input) { i.Obs.Gone = true }, AwaitUser},
		{"outside window", func(i *Input) { i.InWindow = false }, None},
		{"idle with background work", func(i *Input) { i.Obs.Background = 1 }, None},
		{"idle with queued messages", func(i *Input) { i.Obs.Queued = 2 }, None},
		{"idle and primary ready switches back", func(i *Input) { i.SwitchBackReady = true }, SwitchBack},
		{"active never switches back mid-turn", func(i *Input) { i.Obs.State, i.SwitchBackReady = Active, true }, None},
		{"queued work blocks switch-back", func(i *Input) { i.Obs.Queued, i.SwitchBackReady = 1, true }, None},
		{"paused blocks switch-back", func(i *Input) { i.Control.Paused, i.SwitchBackReady = true, true }, None},
		{"active and progressing", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = 5 * m }, None},
		{"active stalled", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = 31 * m }, Nudge},
		{"active stalled but woken recently", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = 31 * m; i.SinceWake = 20 * m }, None},
		{"stalled with nudges left", func(i *Input) { i.Obs.State, i.UnchangedFor, i.Nudges, i.MaxNudges = Active, 31*m, 2, 3 }, Nudge},
		{"stalled after the last nudge asks a person", func(i *Input) { i.Obs.State, i.UnchangedFor, i.Nudges, i.MaxNudges = Active, 31*m, 3, 3 }, AwaitUser},
		{"stopping", func(i *Input) { i.Obs.State = Stopping }, None},
		{"pending", func(i *Input) { i.Obs.State = Pending }, None},
		{"unknown status never acts", func(i *Input) { i.Obs.State = Unknown }, None},
		{"error unknown class re-prompts", func(i *Input) { i.Obs.State = Error; i.Obs.ErrorClass = ErrUnknown }, Reprompt},
		{"error empty class re-prompts", func(i *Input) { i.Obs.State = Error }, Reprompt},
		{"error rate limit re-prompts, no failover here", func(i *Input) { i.Obs.State = Error; i.Obs.ErrorClass = ErrRateLimit }, Reprompt},
		{"error auth goes to user", func(i *Input) { i.Obs.State = Error; i.Obs.ErrorClass = ErrAuth }, AwaitUser},
		{"error backing off", func(i *Input) { i.Obs.State = Error; i.Reprompts = 1; i.SinceLastReprompt = 2 * m }, None},
		{"error backoff elapsed", func(i *Input) { i.Obs.State = Error; i.Reprompts = 1; i.SinceLastReprompt = 11 * m }, Reprompt},
		{"error exhausted", func(i *Input) { i.Obs.State = Error; i.Reprompts = 3 }, AwaitUser},
		{"error with queued message waits", func(i *Input) { i.Obs.State = Error; i.Obs.Queued = 1 }, None},
		{"provider retrying", func(i *Input) { i.Obs.State = Error; i.Obs.Retrying = true }, None},
		{"idle but waiting on a person", func(i *Input) { i.Obs.AwaitingInput = true }, None},
		{"stalled but waiting on a person", func(i *Input) {
			i.Obs.State, i.Obs.AwaitingInput, i.UnchangedFor = Active, true, 2*time.Hour
		}, None},
		{"errored but waiting on a person", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass, i.Obs.AwaitingInput = Error, ErrRateLimit, true
			i.Failover = Failover{Eligible: true, Available: true}
		}, None},
		{"stop still wins over a pending interaction", func(i *Input) {
			i.Control.Stop, i.Obs.State, i.Obs.AwaitingInput = true, Active, true
		}, Stop},
		{"rate limit hands off when available", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true}
		}, Handoff},
		{"rate limit waits when reset is near", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true, ResetIn: 10 * m, WaitIfResetWithin: 30 * m}
		}, None},
		{"rate limit hands off when reset is far", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true, ResetIn: 3 * time.Hour, WaitIfResetWithin: 30 * m}
		}, Handoff},
		{"rate limit with no fallback waits for a known reset", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, ResetIn: 3 * time.Hour, WaitIfResetWithin: 30 * m}
		}, None},
		{"rate limit with no fallback and no reset re-prompts", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true}
		}, Reprompt},
		{"unknown error never hands off", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrUnknown
			i.Failover = Failover{Eligible: true, Available: true}
		}, Reprompt},
		{"ineligible class never hands off", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrNetwork
			i.Failover = Failover{Eligible: false, Available: true}
		}, Reprompt},
		{"auth hands off when configured", func(i *Input) {
			i.Obs.State, i.Obs.ErrorClass = Error, ErrAuth
			i.Failover = Failover{Eligible: true, Available: true}
		}, Handoff},
		{"handoff blocked by stop", func(i *Input) {
			i.Control.Stop = true
			i.Obs.State, i.Obs.ErrorClass = Error, ErrRateLimit
			i.Failover = Failover{Eligible: true, Available: true}
		}, None},
	}
	for _, tc := range cases {
		in := base()
		tc.mut(&in)
		got := Decide(in)
		if got.Action != tc.want {
			t.Errorf("%s: got %s (%s), want %s", tc.name, got.Action, got.Reason, tc.want)
		}
		if got.Reason == "" {
			t.Errorf("%s: empty reason", tc.name)
		}
	}
}

func TestRateLimitWaitWithoutFailover(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Input)
		action Action
		reason string
	}{
		{"known reset", func(*Input) {}, None, "failover disabled; waiting"},
		{"spent retries", func(i *Input) { i.Reprompts = i.MaxReprompts }, None, "failover disabled; waiting"},
		{"fallback cannot bypass policy", func(i *Input) { i.Failover.Available = true }, None, "failover disabled; waiting"},
		{"reset reached", func(i *Input) { i.Failover.ResetIn = 0 }, Reprompt, "error: rate_limit"},
		{"unknown error", func(i *Input) { i.Obs.ErrorClass = ErrUnknown }, Reprompt, "error: unknown"},
		{"auth error", func(i *Input) { i.Obs.ErrorClass = ErrAuth }, AwaitUser, "auth error"},
		{"stop", func(i *Input) { i.Control.Stop = true }, None, "stop requested"},
		{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
		{"explicit hold", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
		{"completion", func(i *Input) { i.Control.Completed = true }, None, "completed"},
		{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser, "loop deadline reached"},
		{"send cap", func(i *Input) { i.SendLimitReached = true }, AwaitUser, "loop send limit reached"},
		{"progress bound", func(i *Input) { i.NoProgressReached = true }, AwaitUser, "no Git progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs.State, in.Obs.ErrorClass = Error, ErrRateLimit
			in.Failover.ResetIn = 24 * time.Hour
			tc.mutate(&in)
			got := Decide(in)
			if got.Action != tc.action || !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("got %+v; want %s / %s", got, tc.action, tc.reason)
			}
		})
	}
}

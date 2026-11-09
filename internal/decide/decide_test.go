package decide

import (
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
		{"awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, None},
		{"target gone", func(i *Input) { i.Obs.Gone = true }, None},
		{"outside window", func(i *Input) { i.InWindow = false }, None},
		{"idle with background work", func(i *Input) { i.Obs.Background = 1 }, None},
		{"idle with queued messages", func(i *Input) { i.Obs.Queued = 2 }, None},
		{"active and progressing", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = 5 * m }, None},
		{"active stalled", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = 31 * m }, Nudge},
		{"active stalled but woken recently", func(i *Input) { i.Obs.State = Active; i.UnchangedFor = 31 * m; i.SinceWake = 20 * m }, None},
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

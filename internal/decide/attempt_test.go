package decide

import (
	"testing"
	"time"
)

func TestAttemptedCountsEverySendAction(t *testing.T) {
	earlier := time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)
	at := earlier.Add(time.Hour)
	before := Counters{Nudges: 1, Reprompts: 1, LastSendAt: earlier, LastRepromptAt: earlier}
	cases := []struct {
		action Action
		want   Counters
	}{
		{Wake, Counters{Nudges: 1, Reprompts: 1, LastSendAt: at, LastRepromptAt: earlier}},
		{Nudge, Counters{Nudges: 2, Reprompts: 1, LastSendAt: at, LastRepromptAt: earlier}},
		{Reprompt, Counters{Nudges: 1, Reprompts: 2, LastSendAt: at, LastRepromptAt: at}},
		{None, before},
		{AwaitUser, before},
		{Stop, before},
		// Handoff and switch-back keep their own accounting in the command layer.
		{Handoff, before},
		{SwitchBack, before},
	}
	for _, tc := range cases {
		if got := Attempted(tc.action, before, at); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.action, got, tc.want)
		}
	}
}

// Attempted has no outcome argument, so these sequences hold whether every send
// was delivered, failed, was unsupported or is uncertain. Each row replays
// one-minute checks of a session that never changes state.
func TestAttemptsReachTheirCapAndSpacing(t *testing.T) {
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		obs       Observation
		control   Control
		checks    int
		wantSends []int // minutes at which a send is attempted
		wantHold  int   // minute of the first await_user, or -1
		wantStop  bool  // every check must return stop
	}{
		{"error: two re-prompts a backoff apart, then a person", Observation{State: Error, ErrorClass: ErrUnknown}, Control{Enabled: true}, 30, []int{0, 10}, 11, false},
		{"stalled: two nudges a wake interval apart, then a person", Observation{State: Active}, Control{Enabled: true}, 200, []int{0, 60}, 120, false},
		{"idle: no per-episode cap, a wake on every check", Observation{State: Idle}, Control{Enabled: true}, 5, []int{0, 1, 2, 3, 4}, -1, false},
		{"paused error never sends or escalates", Observation{State: Error, ErrorClass: ErrUnknown}, Control{Enabled: true, Paused: true}, 30, nil, -1, false},
		{"stop wins over a stalled turn", Observation{State: Active}, Control{Enabled: true, Stop: true}, 200, nil, -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c Counters
			var sent []int
			hold := -1
			for minute := 0; minute < tc.checks && hold < 0; minute++ {
				now := start.Add(time.Duration(minute) * time.Minute)
				in := Input{
					Obs: tc.obs, Control: tc.control, InWindow: true,
					UnchangedFor: 24 * time.Hour, StallAfter: 30 * time.Minute, MinWakeInterval: time.Hour,
					SinceWake: 24 * time.Hour, Backoff: 10 * time.Minute,
					Nudges: c.Nudges, MaxNudges: 2, Reprompts: c.Reprompts, MaxReprompts: 2,
				}
				if !c.LastSendAt.IsZero() {
					in.SinceWake = now.Sub(c.LastSendAt)
				}
				if !c.LastRepromptAt.IsZero() {
					in.SinceLastReprompt = now.Sub(c.LastRepromptAt)
				}
				d := Decide(in)
				if (d.Action == Stop) != tc.wantStop {
					t.Fatalf("minute %d: %+v", minute, d)
				}
				if d.Action == AwaitUser {
					hold = minute
				}
				if sends(d.Action) {
					sent = append(sent, minute)
				}
				c = Attempted(d.Action, c, now)
			}
			if len(sent) != len(tc.wantSends) || hold != tc.wantHold {
				t.Fatalf("sends at %v hold at %d, want %v and %d", sent, hold, tc.wantSends, tc.wantHold)
			}
			for i := range sent {
				if sent[i] != tc.wantSends[i] {
					t.Fatalf("sends at %v, want %v", sent, tc.wantSends)
				}
			}
		})
	}
}

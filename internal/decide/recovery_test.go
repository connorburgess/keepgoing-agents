package decide

import (
	"testing"
	"time"
)

func TestRecoverBy(t *testing.T) {
	reprompt := Decision{Action: Reprompt, Reason: "error: network"}
	for name, tc := range map[string]struct {
		d        Decision
		obs      Observation
		canRetry bool
		want     Recovery
	}{
		"network error":              {reprompt, Observation{State: Error, ErrorClass: ErrNetwork}, true, RecoverByTurnRetry},
		"overloaded":                 {reprompt, Observation{State: Error, ErrorClass: ErrOverloaded}, true, RecoverByTurnRetry},
		"unknown error":              {reprompt, Observation{State: Error, ErrorClass: ErrUnknown}, true, RecoverByTurnRetry},
		"error with no class":        {reprompt, Observation{State: Error}, true, RecoverByTurnRetry},
		"rate limit":                 {reprompt, Observation{State: Error, ErrorClass: ErrRateLimit}, true, RecoverByPrompt},
		"auth":                       {reprompt, Observation{State: Error, ErrorClass: ErrAuth}, true, RecoverByPrompt},
		"an unrecognised class":      {reprompt, Observation{State: Error, ErrorClass: "something_new"}, true, RecoverByPrompt},
		"provider cannot retry":      {reprompt, Observation{State: Error, ErrorClass: ErrNetwork}, false, RecoverByPrompt},
		"not in error":               {reprompt, Observation{State: Idle, ErrorClass: ErrNetwork}, true, RecoverByPrompt},
		"active":                     {reprompt, Observation{State: Active, ErrorClass: ErrNetwork}, true, RecoverByPrompt},
		"a wake":                     {Decision{Action: Wake}, Observation{State: Error, ErrorClass: ErrNetwork}, true, RecoverByPrompt},
		"a nudge":                    {Decision{Action: Nudge}, Observation{State: Error, ErrorClass: ErrNetwork}, true, RecoverByPrompt},
		"a handoff":                  {Decision{Action: Handoff}, Observation{State: Error, ErrorClass: ErrNetwork}, true, RecoverByPrompt},
		"a hold":                     {Decision{Action: AwaitUser}, Observation{State: Error, ErrorClass: ErrNetwork}, true, RecoverByPrompt},
		"nothing":                    {Decision{Action: None}, Observation{State: Error, ErrorClass: ErrNetwork}, true, RecoverByPrompt},
		"queued messages, reprompt":  {reprompt, Observation{State: Error, ErrorClass: ErrNetwork, Queued: 2}, true, RecoverByTurnRetry},
		"gone target, never reaches": {Decision{Action: AwaitUser}, Observation{State: Error, Gone: true}, true, RecoverByPrompt},
	} {
		if got := RecoverBy(tc.d, tc.obs, tc.canRetry); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	// The engine re-prompts only an errored session, and holds an auth error,
	// so the two classes a retry cannot help never reach it as a turn retry.
	for _, class := range []ErrorClass{ErrNetwork, ErrOverloaded, ErrUnknown, ErrRateLimit, ErrAuth} {
		obs := Observation{State: Error, ErrorClass: class}
		d := Decide(Input{Obs: obs, Control: Control{Enabled: true}, InWindow: true, MaxReprompts: 2})
		got := RecoverBy(d, obs, true)
		want := RecoverByTurnRetry
		if class == ErrRateLimit || class == ErrAuth {
			want = RecoverByPrompt
		}
		if got != want {
			t.Errorf("%s: decision %s recovered by %s", class, d.Action, got)
		}
	}
}

// Only the three holds raised because a per-episode allowance ran out are
// marked, so an explicit resume starts a new episode for those and no other.
func TestOnlySpentEpisodeAllowancesMarkTheirHold(t *testing.T) {
	failed := func(class ErrorClass) Input {
		in := base()
		in.Obs = Observation{State: Error, ErrorClass: class}
		in.Reprompts, in.MaxReprompts = 3, 3
		return in
	}
	stalled := base()
	stalled.Obs = Observation{State: Active}
	stalled.ProgressTracked, stalled.StallAfter, stalled.UnchangedFor = true, 30*time.Minute, 5*time.Hour
	stalled.MinWakeInterval, stalled.SinceWake = time.Hour, 2*time.Hour
	stalled.Nudges, stalled.MaxNudges = 3, 3
	silent := base()
	silent.Obs = Observation{State: Active, RetryExpired: true, RetryKnown: true}
	silent.StallAfter, silent.MinWakeInterval, silent.MaxNudges, silent.RetrySilentFor = 30*time.Minute, time.Hour, 3, 4*time.Hour
	deadline, sends, gone, child := base(), base(), base(), base()
	deadline.DeadlineReached, sends.SendLimitReached = true, true
	gone.Obs = Observation{Gone: true}
	child.Obs = Observation{State: Idle, AwaitingChildren: []string{"thr_example_child"}}
	for name, tc := range map[string]struct {
		in    Input
		spent bool
	}{
		"re-prompt cap":     {failed(ErrUnknown), true},
		"nudge cap":         {stalled, true},
		"silent retry":      {silent, true},
		"auth error":        {failed(ErrAuth), false},
		"deadline":          {deadline, false},
		"send limit":        {sends, false},
		"target gone":       {gone, false},
		"child interaction": {child, false},
	} {
		if d := Decide(tc.in); d.Action != AwaitUser || d.EpisodeSpent != tc.spent {
			t.Fatalf("%s: %+v", name, d)
		}
	}
}

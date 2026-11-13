package decide

import "testing"

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

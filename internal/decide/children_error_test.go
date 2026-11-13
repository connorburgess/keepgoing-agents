package decide

import (
	"strings"
	"testing"
	"time"
)

// A supervised session that is itself in error cannot take its children's
// results, so child work must not hold back its recovery. Every error rule
// gives the same decision whatever the children are doing, and whether or not
// the child scan completed. The one exception is a child waiting on a person,
// which holds the loop as it always has.
func TestErroredParentIsRecoveredWhateverItsChildrenDo(t *testing.T) {
	classes := []ErrorClass{ErrRateLimit, ErrAuth, ErrNetwork, ErrUnknown}
	type want struct {
		action Action
		reason string
		wait   LimitWait
	}
	same := func(w want) map[ErrorClass]want {
		return map[ErrorClass]want{ErrRateLimit: w, ErrAuth: w, ErrNetwork: w, ErrUnknown: w}
	}
	reprompt := func(c ErrorClass) want { return want{Reprompt, "error: " + string(c), ""} }
	authHold := want{AwaitUser, "auth error", ""}
	// failoverOn says which classes the loop is configured to hand off on.
	eligible := func(i *Input, failoverOn ...ErrorClass) {
		for _, c := range failoverOn {
			if i.Obs.ErrorClass == c {
				i.Failover.Eligible = true
			}
		}
	}
	rules := []struct {
		name string
		mut  func(*Input)
		want map[ErrorClass]want
	}{
		{"confirmed fallback, failover on rate limits", func(i *Input) { eligible(i, ErrRateLimit); i.Failover.Available = true }, map[ErrorClass]want{
			ErrRateLimit: {Handoff, "error: rate_limit", ""}, ErrAuth: authHold, ErrNetwork: reprompt(ErrNetwork), ErrUnknown: reprompt(ErrUnknown)}},
		{"confirmed fallback, failover on every class", func(i *Input) { eligible(i, classes...); i.Failover.Available = true }, map[ErrorClass]want{
			ErrRateLimit: {Handoff, "error: rate_limit", ""}, ErrAuth: {Handoff, "error: auth", ""}, ErrNetwork: {Handoff, "error: network", ""},
			ErrUnknown: reprompt(ErrUnknown)}}, // an unknown error never hands off
		{"limit resets soon", func(i *Input) {
			eligible(i, ErrRateLimit)
			i.Failover.Available, i.Failover.ResetIn, i.Failover.WaitIfResetWithin = true, 5*time.Minute, 30*time.Minute
		}, map[ErrorClass]want{ErrRateLimit: {None, "limit resets in 5m0s; waiting", WaitResetSoon}, ErrAuth: authHold, ErrNetwork: reprompt(ErrNetwork), ErrUnknown: reprompt(ErrUnknown)}},
		{"no confirmed fallback, reset known", func(i *Input) {
			eligible(i, ErrRateLimit)
			i.Failover.ResetIn, i.Failover.WaitIfResetWithin = 3*time.Hour, 30*time.Minute
		}, map[ErrorClass]want{ErrRateLimit: {None, "limit resets in 3h0m0s; no confirmed fallback", WaitNoFallback}, ErrAuth: authHold, ErrNetwork: reprompt(ErrNetwork), ErrUnknown: reprompt(ErrUnknown)}},
		{"failover disabled, reset known", func(i *Input) { i.Failover.ResetIn = 3 * time.Hour }, map[ErrorClass]want{
			ErrRateLimit: {None, "limit resets in 3h0m0s; failover disabled; waiting", WaitFailoverDisabled}, ErrAuth: authHold, ErrNetwork: reprompt(ErrNetwork), ErrUnknown: reprompt(ErrUnknown)}},
		{"handoff due but not confirmed in this check", func(i *Input) {
			eligible(i, ErrRateLimit)
			i.Failover.Available, i.HandoffDeferred = true, true
		}, map[ErrorClass]want{ErrRateLimit: {None, HandoffDeferredReason, ""}, ErrAuth: authHold, ErrNetwork: reprompt(ErrNetwork), ErrUnknown: reprompt(ErrUnknown)}},
		{"re-prompts spent", func(i *Input) { i.Reprompts = i.MaxReprompts }, map[ErrorClass]want{
			ErrRateLimit: {AwaitUser, "error persisted after re-prompts", ""}, ErrAuth: authHold,
			ErrNetwork: {AwaitUser, "error persisted after re-prompts", ""}, ErrUnknown: {AwaitUser, "error persisted after re-prompts", ""}}},
		{"backing off between re-prompts", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 1, time.Minute }, map[ErrorClass]want{
			ErrRateLimit: {None, "backing off after error", ""}, ErrAuth: authHold, ErrNetwork: {None, "backing off after error", ""}, ErrUnknown: {None, "backing off after error", ""}}},
		// The network gate is one of the error rules: a session in error with
		// children reaches it and waits while the network is down.
		{"network down", func(i *Input) { i.NetworkDown = true }, same(want{None, NetworkDownReason, ""})},
		{"network down, confirmed fallback", func(i *Input) {
			eligible(i, classes...)
			i.NetworkDown, i.Failover.Available = true, true
		}, same(want{None, NetworkDownReason, ""})},
		// Gates that come before the error rules keep their place.
		{"outside the window", func(i *Input) { i.InWindow = false }, same(want{None, "outside window", ""})},
		{"provider retry state unverified", func(i *Input) { i.Obs.RetryUnverified = true }, same(want{None, "provider retry state unverified", ""})},
		{"provider is retrying the turn", func(i *Input) { i.Obs.Retrying = true }, same(want{None, "provider is retrying the turn", ""})},
		{"message already queued", func(i *Input) { i.Obs.Queued = 1 }, same(want{None, "messages already queued after error", ""})},
		{"paused", func(i *Input) { i.Control.Paused = true }, same(want{None, "paused", ""})},
		{"awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, same(want{None, "awaiting user", ""})},
		{"stop requested", func(i *Input) { i.Control.Stop = true }, same(want{None, "stop requested; session not running", ""})},
		{"parent waiting on a person", func(i *Input) { i.Obs.AwaitingInput = true }, same(want{None, "waiting on a person (pending interaction)", ""})},
	}
	children := []struct {
		name string
		mut  func(*Input)
	}{
		{"active", func(i *Input) {
			i.Obs.ActiveChildren, i.Obs.ActiveChildIDs = 2, []string{"thr_example_a", "thr_example_b"}
		}},
		{"errored beside an active one", func(i *Input) {
			i.Obs.ActiveChildren, i.Obs.ErroredChildren = 1, []string{"thr_example_c"}
			i.UnannouncedErroredChildren, i.MaxChildErrorNotices = 1, 3
		}},
		{"errored only", func(i *Input) {
			i.Obs.ErroredChildren = []string{"thr_example_c"}
			i.UnannouncedErroredChildren, i.MaxChildErrorNotices = 1, 3
		}},
		{"scan failed", func(i *Input) { i.Obs.ChildrenUnverified = true }},
		{"scan incomplete beside an active one", func(i *Input) { i.Obs.ActiveChildren, i.Obs.ChildrenUnverified = 1, true }},
	}
	for _, class := range classes {
		for _, rule := range rules {
			build := func() Input {
				in := base()
				in.Obs.State, in.Obs.ErrorClass = Error, class
				rule.mut(&in)
				return in
			}
			expected := rule.want[class]
			alone := Decide(build())
			if alone.Action != expected.action || alone.Reason != expected.reason || alone.LimitWait != expected.wait {
				t.Fatalf("%s, %s, no children: %+v, want %+v", class, rule.name, alone, expected)
			}
			for _, child := range children {
				in := build()
				child.mut(&in)
				got := Decide(in)
				if got != alone {
					t.Errorf("%s, %s, children %s: %+v; without children %+v", class, rule.name, child.name, got, alone)
				}
				if strings.Contains(got.Reason, "child") {
					t.Errorf("%s, %s, children %s: recovery held back by child work: %+v", class, rule.name, child.name, got)
				}
				// How a re-prompt is carried out does not depend on children either.
				if RecoverBy(got, in.Obs, true) != RecoverBy(alone, build().Obs, true) {
					t.Errorf("%s, %s, children %s: recovery method changed", class, rule.name, child.name)
				}
			}
			// A child waiting on a person holds the loop, as before, unless a
			// control already holds or stops it.
			for name, mut := range map[string]func(*Input){
				"awaiting input":                      func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_example_d"} },
				"awaiting input beside an active one": func(i *Input) { i.Obs.AwaitingChildren, i.Obs.ActiveChildren = []string{"thr_example_d"}, 1 },
				"awaiting input, scan incomplete":     func(i *Input) { i.Obs.AwaitingChildren, i.Obs.ChildrenUnverified = []string{"thr_example_d"}, true },
			} {
				in := build()
				mut(&in)
				got := Decide(in)
				controlled := in.Control.Paused || in.Control.AwaitingUser || in.Control.Stop
				if controlled && got != alone {
					t.Errorf("%s, %s, children %s: a control was overridden: %+v", class, rule.name, name, got)
				}
				if !controlled && (got.Action != AwaitUser || got.Reason != "children waiting on a person (pending interaction): thr_example_d") {
					t.Errorf("%s, %s, children %s: %+v", class, rule.name, name, got)
				}
			}
		}
	}
}

// For a parent that is not in error nothing changes: child work and an
// incomplete scan still suppress every wake, nudge and switch-back.
func TestChildWorkStillHoldsAParentThatIsNotInError(t *testing.T) {
	for _, state := range []State{Idle, Active, Pending, Stopping, Unknown} {
		for name, tc := range map[string]struct {
			mut    func(*Input)
			reason string
		}{
			"active":                    {func(i *Input) { i.Obs.ActiveChildren = 2 }, "child work running"},
			"scan failed":               {func(i *Input) { i.Obs.ChildrenUnverified = true }, "child status unverified"},
			"active, scan incomplete":   {func(i *Input) { i.Obs.ActiveChildren, i.Obs.ChildrenUnverified = 1, true }, "child work running"},
			"active, stalled long ago":  {func(i *Input) { i.Obs.ActiveChildren, i.UnchangedFor, i.Nudges, i.MaxNudges = 1, 9*time.Hour, 3, 3 }, "child work running"},
			"active, switch-back ready": {func(i *Input) { i.Obs.ActiveChildren, i.SwitchBackReady = 1, true }, "child work running"},
			// An error class left on a session that is not in the error state is not an error.
			"active, stale error class": {func(i *Input) {
				i.Obs.ActiveChildren, i.Obs.ErrorClass = 1, ErrRateLimit
				i.Failover = Failover{Eligible: true, Available: true}
			}, "child work running"},
		} {
			in := base()
			in.Obs.State = state
			tc.mut(&in)
			if d := Decide(in); d.Action != None || d.Reason != tc.reason {
				t.Errorf("%s parent, children %s: %+v", state, name, d)
			}
		}
	}
}

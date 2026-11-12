package decide

import (
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
	"testing"
	"time"
)

func TestRetryPrecedence(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			mut    func(*Input)
			action Action
			reason string
		}{
			{"active", func(*Input) {}, None, ""},
			{"idle", func(i *Input) { i.Obs.State = Idle }, None, ""},
			{"error", func(i *Input) {
				i.Obs.State = Error
				i.Obs.ErrorClass = ErrRateLimit
				i.Failover = Failover{Eligible: true, Available: true}
			}, None, ""},
			{"stop", func(i *Input) { i.Control.Stop = true }, Stop, "stop requested"},
			{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
			{"disabled", func(i *Input) { i.Control.Enabled = false }, None, "disabled"},
			{"completed", func(i *Input) { i.Control.Completed = true }, None, "completed"},
			{"await_user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
			{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser, ""},
			{"send_cap", func(i *Input) { i.SendLimitReached = true }, AwaitUser, ""},
			{"git_limit", func(i *Input) { i.NoProgressReached = true }, AwaitUser, ""},
			{"child_input", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_child"} }, AwaitUser, ""},
		} {
			t.Run(tc.name+map[bool]string{false: "_retry", true: "_unknown"}[uncertain], func(t *testing.T) {
				in := base()
				in.Obs.State = Active
				in.Obs.Retrying = !uncertain
				in.Obs.RetryUnverified = uncertain
				in.UnchangedFor = 24 * time.Hour
				in.Nudges = 3
				in.MaxNudges = 3
				in.SwitchBackReady = true
				tc.mut(&in)
				d := Decide(in)
				if d.Action != tc.action || tc.reason != "" && d.Reason != tc.reason {
					t.Fatalf("%+v", d)
				}
			})
		}
	}
}

func TestDiagnosticEvidenceCannotAuthorizeRecovery(t *testing.T) {
	in := base()
	in.Obs = Observation{State: Error, ErrorClass: ErrUnknown}
	in.Failover = Failover{Eligible: true, Available: true}
	want := Decide(in)
	in.Obs.Evidence = evidence.Detail{Source: "bb_provider_error", Code: "rate_limit", HTTPStatus: evidence.Number(429)}
	if got := Decide(in); got != want {
		t.Fatalf("diagnostic metadata changed decision: %+v != %+v", got, want)
	}
}

// An expired retry notice is reported with Retrying false. On an errored
// session the flag itself changes nothing: every decision equals the one made without it, so an errored
// session gets the ordinary bounded handling and controls keep precedence.
func TestExpiredRetrySignalUsesOrdinaryErrorHandling(t *testing.T) {
	m := time.Minute
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		action Action
		reason string
	}{
		{"first re-prompt", func(*Input) {}, Reprompt, "error: unknown"},
		{"backing off", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 1, 9*m }, None, "backing off after error"},
		{"re-prompt after backoff", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 2, 10*m }, Reprompt, "error: unknown"},
		{"cap reached holds for a person", func(i *Input) { i.Reprompts, i.SinceLastReprompt = 3, 60*m }, AwaitUser, "error persisted after re-prompts"},
		{"unknown never fails over", func(i *Input) { i.Failover = Failover{Eligible: true, Available: true} }, Reprompt, "error: unknown"},
		{"unclassified never fails over", func(i *Input) { i.Obs.ErrorClass, i.Failover = ErrNone, Failover{Eligible: true, Available: true} }, Reprompt, "error: unknown"},
		{"network may fail over when configured", func(i *Input) { i.Obs.ErrorClass, i.Failover = ErrNetwork, Failover{Eligible: true, Available: true} }, Handoff, "error: network"},
		{"auth holds", func(i *Input) { i.Obs.ErrorClass = ErrAuth }, AwaitUser, "auth error"},
		{"queued input waits", func(i *Input) { i.Obs.Queued = 1 }, None, "messages already queued after error"},
		{"stop", func(i *Input) { i.Control.Stop = true }, None, "stop requested; session not running"},
		{"pause", func(i *Input) { i.Control.Paused = true }, None, "paused"},
		{"disabled", func(i *Input) { i.Control.Enabled = false }, None, "disabled"},
		{"awaiting user", func(i *Input) { i.Control.AwaitingUser = true }, None, "awaiting user"},
		{"completed", func(i *Input) { i.Control.Completed = true }, None, "completed"},
		{"deadline", func(i *Input) { i.DeadlineReached = true }, AwaitUser, ""},
		{"send cap", func(i *Input) { i.SendLimitReached = true }, AwaitUser, ""},
		{"child input", func(i *Input) { i.Obs.AwaitingChildren = []string{"thr_child"} }, AwaitUser, ""},
		{"pending interaction", func(i *Input) { i.Obs.AwaitingInput = true }, None, "waiting on a person (pending interaction)"},
		{"active child", func(i *Input) { i.Obs.ActiveChildren = 1 }, None, "child work running"},
		{"outside window", func(i *Input) { i.InWindow = false }, None, "outside window"},
		{"scan failed", func(i *Input) { i.Obs.RetryUnverified = true }, None, "provider retry state unverified"},
		{"fresh notice still suppresses", func(i *Input) { i.Obs.Retrying = true }, None, "provider is retrying the turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs = Observation{State: Error, ErrorClass: ErrUnknown, RetryKnown: true}
			tc.mut(&in)
			want := Decide(in)
			in.Obs.RetryExpired = true
			got := Decide(in)
			if got.Action != want.Action || got.Reason != want.Reason || got.LimitWait != want.LimitWait {
				t.Fatalf("diagnostic flag changed the decision: %+v != %+v", got, want)
			}
			if got.Action != tc.action || tc.reason != "" && got.Reason != tc.reason {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// On an active session the expired flag is equally inert: the ordinary stall
// rules decide, and an expired retry never becomes a re-prompt or a handoff.
func TestExpiredRetrySignalOnActiveSessionUsesStallRules(t *testing.T) {
	h := time.Hour
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		action Action
	}{
		{"not stalled", func(i *Input) { i.UnchangedFor = 10 * time.Minute }, None},
		{"stalled", func(i *Input) { i.UnchangedFor = 2 * h }, Nudge},
		{"spaced", func(i *Input) { i.UnchangedFor, i.SinceWake = 2*h, 10*time.Minute }, None},
		{"nudges spent", func(i *Input) { i.UnchangedFor, i.Nudges = 2*h, 3 }, AwaitUser},
		{"fallback available", func(i *Input) { i.UnchangedFor, i.Failover = 2*h, Failover{Eligible: true, Available: true} }, Nudge},
		{"stop", func(i *Input) { i.Control.Stop = true }, Stop},
		{"pause", func(i *Input) { i.Control.Paused = true }, None},
		{"still emitting events", func(i *Input) { i.UnchangedFor, i.Obs.Retrying = 2*h, true }, None},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs = Observation{State: Active, ErrorClass: ErrNetwork, RetryKnown: true}
			in.MaxNudges = 3
			tc.mut(&in)
			want := Decide(in)
			in.Obs.RetryExpired = true
			if got := Decide(in); got != want || got.Action != tc.action || got.Action == Reprompt || got.Action == Handoff {
				t.Fatalf("%+v want %+v", got, want)
			}
		})
	}
}

// A retry notice on a rate-limited, errored session is honored only while a
// retry is actually queued or the structured reset is within
// wait_if_reset_within. A far reset with nothing queued goes to the ordinary
// limit rules, so a healthy fallback is used instead of waiting hours.
func TestRetryNoticeYieldsToFarRateLimitReset(t *testing.T) {
	h, m := time.Hour, time.Minute
	for _, tc := range []struct {
		name               string
		resetIn            time.Duration
		queued             int
		eligible, fallback bool
		action             Action
		reason             string
		wait               LimitWait
	}{
		{"far, queued, fallback", 5 * h, 1, true, true, None, "provider is retrying the turn", ""},
		{"far, queued, no fallback", 5 * h, 1, true, false, None, "provider is retrying the turn", ""},
		{"far, not queued, fallback", 5 * h, 0, true, true, Handoff, "error: rate_limit", ""},
		{"far, not queued, no fallback", 5 * h, 0, true, false, None, "limit resets in 5h0m0s; no confirmed fallback", WaitNoFallback},
		{"far, not queued, failover off", 5 * h, 0, false, true, None, "limit resets in 5h0m0s; failover disabled; waiting", WaitFailoverDisabled},
		{"near, not queued, fallback", 20 * m, 0, true, true, None, "provider is retrying the turn", ""},
		{"near, queued, fallback", 20 * m, 1, true, true, None, "provider is retrying the turn", ""},
		{"at the boundary", 30 * m, 0, true, true, None, "provider is retrying the turn", ""},
		{"just past the boundary", 30*m + time.Second, 0, true, true, Handoff, "error: rate_limit", ""},
		{"no structured reset, fallback", 0, 0, true, true, None, "provider is retrying the turn", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs = Observation{State: Error, ErrorClass: ErrRateLimit, Retrying: true, RetryKnown: true, Queued: tc.queued}
			in.Failover = Failover{Eligible: tc.eligible, Available: tc.fallback, ResetIn: tc.resetIn, WaitIfResetWithin: 30 * m}
			if d := Decide(in); d.Action != tc.action || d.Reason != tc.reason || d.LimitWait != tc.wait {
				t.Fatalf("%+v", d)
			}
			// Only an errored rate limit yields. Other classes and an active
			// session keep honoring the notice.
			in.Obs.ErrorClass = ErrNetwork
			if d := Decide(in); d.Reason != "provider is retrying the turn" {
				t.Fatalf("network: %+v", d)
			}
			in.Obs.ErrorClass, in.Obs.State = ErrRateLimit, Active
			if d := Decide(in); d.Reason != "provider is retrying the turn" {
				t.Fatalf("active: %+v", d)
			}
			in.Obs.State, in.Control.Paused = Error, true
			if d := Decide(in); d.Reason != "paused" {
				t.Fatalf("pause: %+v", d)
			}
		})
	}
}

// Without Git tracking an active session cannot be shown to have stalled and
// is never nudged. Once its retry notice has expired and it has stayed silent
// for one stall interval plus every nudge interval, a person is asked.
func TestSilentActiveSessionWithoutGitTrackingHolds(t *testing.T) {
	patience := SilentRetryPatience(30*time.Minute, 60*time.Minute, 3)
	if patience != 3*time.Hour+30*time.Minute {
		t.Fatal(patience)
	}
	for _, tc := range []struct {
		name   string
		mut    func(*Input)
		action Action
	}{
		{"within patience", func(i *Input) { i.RetrySilentFor = patience - time.Second }, None},
		{"patience spent", func(*Input) {}, AwaitUser},
		{"tracked loops use nudges instead", func(i *Input) { i.ProgressTracked = true }, None},
		{"tracked and stalled", func(i *Input) { i.ProgressTracked, i.UnchangedFor = true, 5*time.Hour }, Nudge},
		{"notice not expired", func(i *Input) { i.Obs.RetryExpired = false }, None},
		{"still retrying", func(i *Input) { i.Obs.RetryExpired, i.Obs.Retrying = false, true }, None},
		{"stop", func(i *Input) { i.Control.Stop = true }, Stop},
		{"pause", func(i *Input) { i.Control.Paused = true }, None},
		{"pending interaction", func(i *Input) { i.Obs.AwaitingInput = true }, None},
		{"active child", func(i *Input) { i.Obs.ActiveChildren = 1 }, None},
		{"outside window", func(i *Input) { i.InWindow = false }, None},
		{"errored session uses re-prompts", func(i *Input) { i.Obs.State = Error }, Reprompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Obs = Observation{State: Active, ErrorClass: ErrUnknown, RetryKnown: true, RetryExpired: true}
			in.MaxNudges, in.RetrySilentFor = 3, patience
			in.Failover = Failover{Eligible: true, Available: true}
			tc.mut(&in)
			d := Decide(in)
			if d.Action != tc.action || d.Action == Handoff {
				t.Fatalf("%+v", d)
			}
			if tc.name == "patience spent" && d.Reason != "provider retry went silent on an active session for 3h30m0s; no Git progress tracking to nudge by" {
				t.Fatal(d.Reason)
			}
		})
	}
}

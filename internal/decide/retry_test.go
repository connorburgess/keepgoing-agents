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

// An expired retry notice is reported with Retrying false. The flag itself is
// diagnostic: every decision equals the one made without it, so an errored
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

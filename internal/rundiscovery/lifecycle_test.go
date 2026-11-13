package rundiscovery

import (
	"errors"
	"testing"
	"time"
)

const (
	loop    = "supervision_11111111111111111111111111111111"
	run     = "run_22222222222222222222222222222222"
	nextRun = "run_33333333333333333333333333333333"
	attempt = "attempt_44444444444444444444444444444444"
)

var recorded = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func openState() State {
	return State{LoopID: loop, Current: Episode{ID: run, Origin: Explicit, RecordedAt: recorded}}
}
func completedState() State {
	s := openState()
	s.Completed = true
	s.Current.ClosedAt = recorded.Add(time.Minute)
	return s
}

func TestEpisodeTransitions(t *testing.T) {
	enrolled := State{LoopID: loop}
	open := openState()
	closed := completedState()
	at := recorded.Add(time.Minute)
	reopened := State{LoopID: loop, Current: Episode{ID: nextRun, Origin: Reopened, RecordedAt: at, Reopens: run}}
	tests := []struct {
		name   string
		before State
		event  Event
		want   State
	}{
		{"enroll only", State{}, Event{Kind: Enroll, NewID: loop}, enrolled},
		{"enroll again", enrolled, Event{Kind: Enroll, NewID: loop}, enrolled},
		{"first enable", enrolled, Event{Kind: Enable, NewID: run}, State{LoopID: loop, Current: Episode{ID: run, Origin: Explicit, RecordedAt: at}}},
		{"first start", enrolled, Event{Kind: Start, NewID: run}, State{LoopID: loop, Current: Episode{ID: run, Origin: Explicit, RecordedAt: at}}},
		{"repeated enable", open, Event{Kind: Enable, NewID: nextRun}, open},
		{"repeated start", open, Event{Kind: Start}, open},
		{"completed enable preserves hold", closed, Event{Kind: Enable, NewID: nextRun}, closed},
		{"completed start preserves hold", closed, Event{Kind: Start}, closed},
		{"adopt unknown history", enrolled, Event{Kind: Adopt, NewID: run, AdoptionEligible: true}, State{LoopID: loop, Current: Episode{ID: run, Origin: Adopted, RecordedAt: at}}},
		{"adopt again", open, Event{Kind: Adopt, AdoptionEligible: true}, open},
		{"complete", open, Event{Kind: Complete}, closed},
		{"complete again", closed, Event{Kind: Complete}, closed},
		{"complete without run", enrolled, Event{Kind: Complete}, State{LoopID: loop, Completed: true}},
		{"resume open", open, Event{Kind: Resume}, open},
		{"resume no episode", enrolled, Event{Kind: Resume}, enrolled},
		{"reopen completed", closed, Event{Kind: Resume, NewID: nextRun}, reopened},
		{"reopen without known predecessor", State{LoopID: loop, Completed: true}, Event{Kind: Resume, NewID: nextRun}, State{LoopID: loop, Current: Episode{ID: nextRun, Origin: Reopened, RecordedAt: at}}},
		{"duplicate resume", reopened, Event{Kind: Resume, NewID: run}, reopened},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.event.At = at
			got, err := Apply(tt.before, tt.event)
			if err != nil || got != tt.want || !got.Valid() {
				t.Fatalf("got %#v, %v; want %#v", got, err, tt.want)
			}
		})
	}
}

func TestNonBoundaryEventsNeverOpenCloseOrChangeRun(t *testing.T) {
	for _, kind := range []Kind{Pause, Stop, Disable, AwaitUser, Check, Retry, Restart, GoalRevision, Transfer, ExternalInvocation} {
		t.Run(string(kind), func(t *testing.T) {
			for _, before := range []State{{LoopID: loop}, {LoopID: loop, Completed: true}, openState(), completedState()} {
				event := Event{Kind: kind, At: recorded.Add(-time.Hour)}
				if kind == ExternalInvocation {
					event.Routine = nightly
				}
				got, err := Apply(before, event)
				if err != nil || got != before {
					t.Fatalf("%s changed %#v to %#v: %v", kind, before, got, err)
				}
			}
		})
	}
}

func TestDryRunsDoNotEnrollAdoptCompleteOrReopen(t *testing.T) {
	for _, tt := range []struct {
		before State
		event  Event
	}{
		{State{}, Event{Kind: Enroll, NewID: loop}},
		{State{LoopID: loop}, Event{Kind: Enable, NewID: run}},
		{State{LoopID: loop}, Event{Kind: Adopt, NewID: run, AdoptionEligible: true}},
		{openState(), Event{Kind: Complete}},
		{completedState(), Event{Kind: Resume, NewID: nextRun}},
	} {
		tt.event.At = recorded
		tt.event.DryRun = true
		got, err := Apply(tt.before, tt.event)
		if err != nil || got != tt.before {
			t.Fatalf("dry-run %s: %#v, %v", tt.event.Kind, got, err)
		}
	}
}

func TestRejectInvalidTransitionsWithoutChangingState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		before State
		event  Event
	}{
		{"unenrolled enable", State{}, Event{Kind: Enable, NewID: run}},
		{"registration is not loop identity", State{}, Event{Kind: Enroll, NewID: "loop_11111111111111111111111111111111"}},
		{"replace durable loop", openState(), Event{Kind: Enroll, NewID: "supervision_55555555555555555555555555555555"}},
		{"missing allocation", State{LoopID: loop}, Event{Kind: Enable}},
		{"missing reopen allocation", completedState(), Event{Kind: Resume}},
		{"reuse current ID", completedState(), Event{Kind: Resume, NewID: run}},
		{"held legacy cannot adopt", State{LoopID: loop}, Event{Kind: Adopt, NewID: run}},
		{"unexpected ID", openState(), Event{Kind: Restart, NewID: nextRun}},
		{"unexpected eligibility", openState(), Event{Kind: Check, AdoptionEligible: true}},
		{"unknown event", openState(), Event{Kind: "provider_says_done"}},
		{"malformed prior state", State{LoopID: loop, Current: Episode{ID: run}}, Event{Kind: Check}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.event.At = recorded
			got, err := Apply(tt.before, tt.event)
			if !errors.Is(err, ErrInvalid) || got != tt.before {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
	before := openState()
	got, err := Apply(before, Event{Kind: Complete})
	if !errors.Is(err, ErrInvalid) || got != before {
		t.Fatal("missing timestamp accepted")
	}
}

func TestStateValidation(t *testing.T) {
	for _, change := range []func(*State){
		func(s *State) { s.LoopID = "" },
		func(s *State) { s.Current.ID = "run_bad" },
		func(s *State) { s.Current.Origin = "inferred" },
		func(s *State) { s.Current.RecordedAt = time.Time{} },
		func(s *State) { s.Completed = true },
		func(s *State) { s.Current.ClosedAt = recorded },
		func(s *State) { s.Current.Reopens = nextRun },
		func(s *State) { s.Current.Origin = Reopened; s.Current.Reopens = run },
		func(s *State) { s.Current = Episode{Origin: Explicit} },
	} {
		s := openState()
		change(&s)
		if s.Valid() {
			t.Fatalf("invalid state accepted: %#v", s)
		}
	}
	if (State{Completed: true}).Valid() {
		t.Fatal("unenrolled completion accepted")
	}
}

func intent() Attempt {
	return Attempt{ID: attempt, RunID: run, Action: "wake", Mode: "auto", RecordedAt: recorded, Outcome: Intent}
}

func TestAttemptCompletionIsNotRunCompletion(t *testing.T) {
	s := openState()
	for _, outcome := range []Outcome{AdapterReturned, AdapterError} {
		a, err := BeginAttempt(s, intent())
		if err != nil {
			t.Fatal(err)
		}
		closed, err := Apply(s, Event{Kind: Complete, At: recorded.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		// An in-flight return may be recorded after run completion, without reopening.
		returned, err := FinishAttempt(a, outcome, recorded.Add(2*time.Minute))
		if err != nil || returned.Outcome != outcome || returned.RunID != s.Current.ID {
			t.Fatalf("return: %#v %v", returned, err)
		}
		again, err := FinishAttempt(returned, outcome, recorded.Add(3*time.Minute))
		if err != nil || again != returned {
			t.Fatal("duplicate return changed first evidence")
		}
		conflicting := AdapterError
		if outcome == AdapterError {
			conflicting = AdapterReturned
		}
		if got, err := FinishAttempt(returned, conflicting, recorded); err == nil || got != returned {
			t.Fatal("conflicting return changed evidence")
		}
		if !closed.Completed || s != openState() {
			t.Fatal("attempt changed lifecycle")
		}
		if _, err := BeginAttempt(closed, intent()); err == nil {
			t.Fatal("late intent attached to closed run")
		}
		reopened, err := Apply(closed, Event{Kind: Resume, At: recorded, NewID: nextRun})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := BeginAttempt(reopened, intent()); err == nil {
			t.Fatal("stale intent attached to reopened run")
		}
	}
}

func TestAttemptValidationAndGoalProvenance(t *testing.T) {
	for _, action := range []string{"wake", "reprompt", "nudge", "handoff", "switch_back"} {
		a := intent()
		a.Action = action
		if action == "nudge" {
			a.Mode = "queue"
		}
		a.Goal = GoalRef{ID: "goal_66666666666666666666666666666666", Revision: 2}
		if got, err := BeginAttempt(openState(), a); err != nil || got != a {
			t.Fatalf("%s: %#v %v", action, got, err)
		}
	}
	for _, change := range []func(*Attempt){
		func(a *Attempt) { a.ID = "bad" }, func(a *Attempt) { a.RunID = nextRun }, func(a *Attempt) { a.RecordedAt = time.Time{} },
		func(a *Attempt) { a.Action = "observe" }, func(a *Attempt) { a.Mode = "queue" }, func(a *Attempt) { a.Action = "nudge" },
		func(a *Attempt) { a.Goal.Revision = 1 }, func(a *Attempt) { a.Goal = GoalRef{ID: "goal_66666666666666666666666666666666", Revision: 129} },
		func(a *Attempt) { a.Outcome = "delivered" }, func(a *Attempt) { a.ReturnedAt = recorded },
		func(a *Attempt) { a.Outcome = AdapterReturned; a.ReturnedAt = recorded },
	} {
		a := intent()
		change(&a)
		if _, err := BeginAttempt(openState(), a); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid intent accepted: %#v", a)
		}
	}
	for _, s := range []State{{}, {LoopID: loop}, completedState()} {
		if _, err := BeginAttempt(s, intent()); err == nil {
			t.Fatalf("intent without open run: %#v", s)
		}
	}
	for _, outcome := range []Outcome{Intent, "delivered", ""} {
		if _, err := FinishAttempt(intent(), outcome, recorded); err == nil {
			t.Fatal("unsupported outcome")
		}
	}
	if _, err := FinishAttempt(intent(), AdapterReturned, time.Time{}); err == nil {
		t.Fatal("missing return timestamp")
	}
}

func TestClockIsNotIdentityOrOrderingAuthority(t *testing.T) {
	s := openState()
	closed, err := Apply(s, Event{Kind: Complete, At: recorded.Add(-time.Hour)})
	if err != nil || !closed.Valid() {
		t.Fatal("wall-clock rollback invalidated identity")
	}
	reopened, err := Apply(closed, Event{Kind: Resume, At: recorded.Add(-2 * time.Hour), NewID: nextRun})
	if err != nil || reopened.Current.Reopens != run {
		t.Fatal("wall-clock rollback broke reopen relationship")
	}
	a, err := FinishAttempt(intent(), AdapterReturned, recorded.Add(-time.Hour))
	if err != nil || !a.Valid() {
		t.Fatal("clock order was treated as return identity")
	}
}

func TestDryRunValidatesProspectiveTransition(t *testing.T) {
	for _, tt := range []struct {
		before State
		event  Event
	}{
		{State{}, Event{Kind: Enable, NewID: run}},
		{State{LoopID: loop}, Event{Kind: Enable}},
		{State{LoopID: loop}, Event{Kind: Adopt, NewID: run}},
		{completedState(), Event{Kind: Resume, NewID: run}},
	} {
		tt.event.At = recorded
		tt.event.DryRun = true
		got, err := Apply(tt.before, tt.event)
		if !errors.Is(err, ErrInvalid) || got != tt.before {
			t.Fatalf("dry run skipped prerequisites: %#v %v", got, err)
		}
	}
}

// A re-prompt may be a retry of the failed turn by reference, which has no
// prompt. No other action may claim that mode.
func TestTurnRetryIsAReprompAttemptOnly(t *testing.T) {
	a := intent()
	a.Action, a.Mode = "reprompt", "turn_retry"
	if !a.Valid() {
		t.Fatal("a turn-retry re-prompt was refused")
	}
	a.Mode = "auto"
	if !a.Valid() {
		t.Fatal("a prompted re-prompt was refused")
	}
	for _, action := range []string{"wake", "nudge", "handoff", "switch_back"} {
		a.Action, a.Mode = action, "turn_retry"
		if a.Valid() {
			t.Errorf("%s accepted the turn_retry mode", action)
		}
	}
	a.Action, a.Mode = "reprompt", "queue"
	if a.Valid() {
		t.Error("a queued re-prompt was accepted")
	}
}

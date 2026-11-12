package rundiscovery

import (
	"errors"
	"testing"
	"time"
)

const (
	registration = "loop_77777777777777777777777777777777"
	goal         = "goal_66666666666666666666666666666666"
)

var (
	nightly     = RoutineRef{Source: "example-scheduler", Routine: "routine-nightly", Invocation: "invocation-0001"}
	nextNightly = RoutineRef{Source: "example-scheduler", Routine: "routine-nightly", Invocation: "invocation-0002"}
	chatA       = SessionRef{Provider: "example-chat", ID: "thr_example_a"}
	chatB       = SessionRef{Provider: "example-headless", ID: "session-example-b"}
)

func sessionLink(runID string, ref SessionRef) Link {
	return Link{RunID: runID, Kind: SessionLink, Session: ref, RecordedAt: recorded}
}

func TestScheduledTriggersOpenARunOnlyThroughAnExplicitStart(t *testing.T) {
	at := recorded.Add(time.Minute)
	enrolled := State{LoopID: loop}
	open := openState()
	closed := completedState()
	opened := State{LoopID: loop, Current: Episode{ID: run, Origin: Explicit, RecordedAt: at, Routine: nightly}}
	reopened := State{LoopID: loop, Current: Episode{ID: nextRun, Origin: Reopened, RecordedAt: at, Reopens: run, Routine: nextNightly}}
	for _, tt := range []struct {
		name   string
		before State
		event  Event
		want   State
	}{
		{"trigger alone opens nothing", enrolled, Event{Kind: ExternalInvocation, Routine: nightly}, enrolled},
		{"explicit start records its invocation", enrolled, Event{Kind: Start, NewID: run, Routine: nightly}, opened},
		{"explicit enable records its invocation", enrolled, Event{Kind: Enable, NewID: run, Routine: nightly}, opened},
		{"later trigger keeps the run and its opening invocation", opened, Event{Kind: ExternalInvocation, Routine: nextNightly}, opened},
		{"later scheduled start keeps the run", opened, Event{Kind: Start, NewID: nextRun, Routine: nextNightly}, opened},
		{"trigger without invocation ID keeps the run", open, Event{Kind: ExternalInvocation, Routine: RoutineRef{Source: "example-scheduler", Routine: "routine-nightly"}}, open},
		{"trigger after completion does not reopen", closed, Event{Kind: ExternalInvocation, Routine: nextNightly}, closed},
		{"scheduled start after completion does not reopen", closed, Event{Kind: Start, NewID: nextRun, Routine: nextNightly}, closed},
		{"explicit resume after completion records its invocation", closed, Event{Kind: Resume, NewID: nextRun, Routine: nextNightly}, reopened},
		{"resume of an open run ignores the invocation", open, Event{Kind: Resume, Routine: nextNightly}, open},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.event.At = at
			got, err := Apply(tt.before, tt.event)
			if err != nil || got != tt.want || !got.Valid() {
				t.Fatalf("got %#v, %v; want %#v", got, err, tt.want)
			}
		})
	}
}

func TestRoutineReferencesAreValidated(t *testing.T) {
	for _, tt := range []struct {
		name   string
		before State
		event  Event
	}{
		{"trigger without structured identity", openState(), Event{Kind: ExternalInvocation}},
		{"trigger without source", openState(), Event{Kind: ExternalInvocation, Routine: RoutineRef{Routine: "routine-nightly"}}},
		{"malformed routine on start", State{LoopID: loop}, Event{Kind: Start, NewID: run, Routine: RoutineRef{Source: "example-scheduler", Routine: "has space"}}},
		{"overlong invocation", State{LoopID: loop}, Event{Kind: Start, NewID: run, Routine: RoutineRef{Source: "example-scheduler", Routine: "routine-nightly", Invocation: string(make([]byte, 129))}}},
		{"routine on check", openState(), Event{Kind: Check, Routine: nightly}},
		{"routine on adoption", State{LoopID: loop}, Event{Kind: Adopt, NewID: run, AdoptionEligible: true, Routine: nightly}},
		{"routine on completion", openState(), Event{Kind: Complete, Routine: nightly}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.event.At = recorded
			got, err := Apply(tt.before, tt.event)
			if !errors.Is(err, ErrInvalid) || got != tt.before {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
	adopted := State{LoopID: loop, Current: Episode{ID: run, Origin: Adopted, RecordedAt: recorded, Routine: nightly}}
	malformed := openState()
	malformed.Current.Routine = RoutineRef{Routine: "routine-nightly"}
	if adopted.Valid() || malformed.Valid() {
		t.Fatal("state with an invented or malformed opening invocation accepted")
	}
}

// A restart is modelled as the caller reloading the same recorded values and
// applying Restart. Durable storage of those values is not exercised here.
func TestRestartAndRetryKeepTheRunAndNeverMergeAttempts(t *testing.T) {
	s := openState()
	first, err := BeginAttempt(s, intent())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []Kind{Restart, Retry, Check} {
		after, err := Apply(s, Event{Kind: kind, At: recorded.Add(time.Hour)})
		if err != nil || after != s {
			t.Fatalf("%s changed the run reference: %#v %v", kind, after, err)
		}
		if _, err := BeginAttempt(after, intent(), first); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s reused an unreturned attempt ID", kind)
		}
		second := intent()
		second.ID = "attempt_88888888888888888888888888888888"
		got, err := BeginAttempt(after, second, first)
		if err != nil || got.RunID != first.RunID || got.ID == first.ID {
			t.Fatalf("%s: new attempt in the same run refused: %#v %v", kind, got, err)
		}
		if first.Outcome != Intent || !first.ReturnedAt.IsZero() {
			t.Fatalf("%s resolved the earlier intent", kind)
		}
	}
	// Repeated wakes of one run are separate attempts, never separate runs.
	wakes := []Attempt{first}
	for _, id := range []string{"attempt_88888888888888888888888888888888", "attempt_99999999999999999999999999999999"} {
		a := intent()
		a.ID = id
		got, err := BeginAttempt(s, a, wakes...)
		if err != nil || got.RunID != run {
			t.Fatalf("repeated wake refused: %v", err)
		}
		wakes = append(wakes, got)
	}
	returned, err := FinishAttempt(wakes[1], AdapterReturned, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BeginAttempt(s, wakes[1], returned); !errors.Is(err, ErrInvalid) {
		t.Fatal("returned attempt ID reused")
	}
}

func TestRunsAndSessionsAreManyToManyWithoutParentage(t *testing.T) {
	first := openState()
	closed := completedState()
	second, err := Apply(closed, Event{Kind: Resume, At: recorded, NewID: nextRun})
	if err != nil {
		t.Fatal(err)
	}
	var links []Link
	record := func(s State, l Link) {
		t.Helper()
		got, err := Relate(s, l)
		if err != nil || got != l {
			t.Fatalf("relate %#v: %#v %v", l, got, err)
		}
		links = append(links, got)
	}
	withAttempt := sessionLink(run, chatB)
	withAttempt.AttemptID = attempt
	record(first, sessionLink(run, chatA))
	record(first, withAttempt)
	record(first, sessionLink(run, chatA))  // recorded twice, listed once
	record(closed, sessionLink(run, chatB)) // late evidence for the completed current run
	record(second, sessionLink(nextRun, chatA))

	if got := Sessions(run, links); len(got) != 2 || got[0] != chatA || got[1] != chatB {
		t.Fatalf("one run should list both sessions once: %#v", got)
	}
	if got := Sessions(nextRun, links); len(got) != 1 || got[0] != chatA {
		t.Fatalf("a later run should reuse the earlier chat: %#v", got)
	}
	if got := Sessions("run_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", links); got != nil {
		t.Fatalf("unrelated run listed sessions: %#v", got)
	}
	// Recording a session changes no lifecycle state and adds no run-to-run
	// edge: the only predecessor reference is the explicit reopen.
	if first != openState() || second.Current.Reopens != run || second.Current.Origin != Reopened {
		t.Fatal("session links changed run identity")
	}
	// A stale or invalid record is skipped by the projection.
	links = append(links, Link{RunID: run, Kind: SessionLink, Session: SessionRef{Provider: "example-chat"}, RecordedAt: recorded})
	if got := Sessions(run, links); len(got) != 2 {
		t.Fatalf("invalid link listed: %#v", got)
	}
}

func TestGoalRevisionAndTransferKeepTheRunAndAddHistory(t *testing.T) {
	s := openState()
	var links []Link
	for _, step := range []struct {
		kind Kind
		link Link
	}{
		{GoalRevision, Link{RunID: run, Kind: GoalLink, Goal: GoalRef{ID: goal, Revision: 1}, RecordedAt: recorded}},
		{GoalRevision, Link{RunID: run, Kind: GoalLink, Goal: GoalRef{ID: goal, Revision: 2}, RecordedAt: recorded}},
		{Transfer, sessionLink(run, chatA)},
		{Transfer, sessionLink(run, chatB)},
	} {
		after, err := Apply(s, Event{Kind: step.kind, At: recorded})
		if err != nil || after != s {
			t.Fatalf("%s changed the run: %#v %v", step.kind, after, err)
		}
		got, err := Relate(after, step.link)
		if err != nil {
			t.Fatalf("%s link refused: %v", step.kind, err)
		}
		links = append(links, got)
	}
	// Source and destination both remain recorded; neither is marked current.
	if got := Sessions(run, links); len(got) != 2 || got[0] != chatA || got[1] != chatB {
		t.Fatalf("transfer history lost: %#v", got)
	}
	if links[0].Goal.Revision != 1 || links[1].Goal.Revision != 2 || links[0].RunID != links[1].RunID {
		t.Fatal("goal revisions were not recorded against one run")
	}
}

func TestInvocationLinksNeedAnOpenRun(t *testing.T) {
	l := Link{RunID: run, Kind: InvocationLink, Routine: nextNightly, RecordedAt: recorded}
	if got, err := Relate(openState(), l); err != nil || got != l {
		t.Fatalf("trigger during an open run refused: %v", err)
	}
	for _, s := range []State{{}, {LoopID: loop}, {LoopID: loop, Completed: true}, completedState()} {
		if _, err := Relate(s, l); !errors.Is(err, ErrInvalid) {
			t.Fatalf("trigger related to a run that is not open: %#v", s)
		}
	}
}

func TestRelateRejectsInvalidOrMisdirectedLinks(t *testing.T) {
	reopened, err := Apply(completedState(), Event{Kind: Resume, At: recorded, NewID: nextRun})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Relate(reopened, sessionLink(run, chatA)); !errors.Is(err, ErrInvalid) {
		t.Fatal("late receipt for a superseded run attached through the newer state")
	}
	if _, err := Relate(State{LoopID: loop}, sessionLink(run, chatA)); !errors.Is(err, ErrInvalid) {
		t.Fatal("link accepted without an episode")
	}
	for name, change := range map[string]func(*Link){
		"other run":             func(l *Link) { l.RunID = nextRun },
		"malformed run":         func(l *Link) { l.RunID = "run_bad" },
		"missing time":          func(l *Link) { l.RecordedAt = time.Time{} },
		"unknown kind":          func(l *Link) { l.Kind = "parent" },
		"missing session":       func(l *Link) { l.Session = SessionRef{} },
		"missing provider":      func(l *Link) { l.Session.Provider = "" },
		"session ID with space": func(l *Link) { l.Session.ID = "two words" },
		"session ID as URL":     func(l *Link) { l.Session.ID = "https://example.com/chat/1" },
		"overlong session ID":   func(l *Link) { l.Session.ID = string(make([]byte, 129)) },
		"malformed attempt":     func(l *Link) { l.AttemptID = "attempt_bad" },
		"two payloads":          func(l *Link) { l.Goal = GoalRef{ID: goal, Revision: 1} },
		"routine on session":    func(l *Link) { l.Routine = nightly },
		"goal without ID":       func(l *Link) { l.Kind = GoalLink; l.Session = SessionRef{} },
		"goal revision bound":   func(l *Link) { l.Kind = GoalLink; l.Session = SessionRef{}; l.Goal = GoalRef{ID: goal, Revision: 129} },
		"attempt on goal": func(l *Link) {
			*l = Link{RunID: run, Kind: GoalLink, Goal: GoalRef{ID: goal, Revision: 1}, AttemptID: attempt, RecordedAt: recorded}
		},
		"invocation no routine": func(l *Link) { l.Kind = InvocationLink; l.Session = SessionRef{} },
	} {
		l := sessionLink(run, chatA)
		change(&l)
		if got, err := Relate(openState(), l); !errors.Is(err, ErrInvalid) || got != (Link{}) {
			t.Fatalf("%s accepted: %#v", name, l)
		}
	}
}

func TestRegistrationIsAnExplicitReferenceNotLoopIdentity(t *testing.T) {
	got, err := Register(openState(), registration, recorded)
	want := Registration{LoopID: loop, RegistrationID: registration, RecordedAt: recorded}
	if err != nil || got != want {
		t.Fatalf("got %#v, %v", got, err)
	}
	// Re-registering yields a different removable reference; the durable
	// identity and run are untouched, and only an explicit call links them.
	again, err := Register(openState(), "loop_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", recorded)
	if err != nil || again.LoopID != got.LoopID || again.RegistrationID == got.RegistrationID {
		t.Fatalf("got %#v, %v", again, err)
	}
	for name, tt := range map[string]struct {
		s  State
		id string
		at time.Time
	}{
		"unenrolled":           {State{}, registration, recorded},
		"durable ID as ref":    {openState(), loop, recorded},
		"malformed":            {openState(), "loop_bad", recorded},
		"missing time":         {openState(), registration, time.Time{}},
		"malformed loop state": {State{LoopID: "loop_77777777777777777777777777777777"}, registration, recorded},
	} {
		if got, err := Register(tt.s, tt.id, tt.at); !errors.Is(err, ErrInvalid) || got != (Registration{}) {
			t.Fatalf("%s accepted: %#v", name, got)
		}
	}
}

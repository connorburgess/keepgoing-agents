package conformance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// fakeAgent is an in-memory agent and adapter in one. With no fault it keeps
// every contract. Each fault breaks exactly one, to show the suite catches it
// and says what to change.
type fakeAgent struct {
	mu        sync.Mutex
	fault     string
	stateDir  string
	situation Situation
	sent      []Sent
	foreign   bool // foreign work is alive
	agents    int  // agents running
	sending   bool
	memory    string // a session kept only in memory
}

// session is what the adapter saved in its state directory.
func (f *fakeAgent) session() string {
	b, _ := os.ReadFile(filepath.Join(f.stateDir, "session"))
	return string(b)
}

func (f *fakeAgent) Name() string { return "fake" }

func (f *fakeAgent) Observe(ctx context.Context) (decide.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fault == "torn read during send" && f.sending {
		return decide.Observation{State: "sending"}, nil
	}
	if f.fault == "resends on read" && len(f.sent) > 0 {
		f.sent = append(f.sent, f.sent[len(f.sent)-1])
	}
	switch f.situation {
	case Idle:
		return decide.Observation{State: decide.Idle}, nil
	case Active:
		return decide.Observation{State: decide.Active}, nil
	case Waiting:
		return decide.Observation{State: decide.Idle, AwaitingInput: f.fault != "misses waiting"}, nil
	case FailedCoded:
		if f.fault == "ignores codes" {
			return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
		}
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}, nil
	case FailedProse:
		if f.fault == "reads prose" {
			return decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}, nil
		}
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	case Garbage:
		if f.fault == "guesses" {
			return decide.Observation{State: decide.Idle}, nil
		}
		if f.fault == "invents a state" {
			return decide.Observation{State: "busy"}, nil
		}
	case ExitNonZero:
		if f.fault == "healthy on a failed exit" {
			return decide.Observation{State: decide.Idle}, nil
		}
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	case Crashed:
		if f.fault == "classifies a crash" {
			return decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}, nil
		}
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	case FailedUnmapped:
		if f.fault == "classifies an unmapped code" {
			return decide.Observation{State: decide.Error, ErrorClass: decide.ErrOverloaded}, nil
		}
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	case FailedStderr:
		if f.fault == "reads standard error" {
			return decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit}, nil
		}
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	case Hanging:
		if f.fault == "hangs" {
			f.mu.Unlock()
			<-ctx.Done()
			f.mu.Lock()
		}
		if f.fault == "idle while hung" {
			return decide.Observation{State: decide.Idle}, nil
		}
		return decide.Observation{State: decide.Active}, nil
	case Oversized:
		if f.fault == "unbounded" {
			return decide.Observation{State: decide.Idle}, nil
		}
	}
	return decide.Observation{State: decide.Unknown}, nil
}

func (f *fakeAgent) Send(_ context.Context, prompt string, _ adapter.Mode) error {
	f.mu.Lock()
	f.sending = true
	f.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // a send takes a moment
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sending = false
	if f.situation == Active && f.fault != "second agent" {
		return adapter.ErrUnsupported{Op: "sending to a working agent"}
	}
	// The prompt goes into a private file, and the session beside it.
	mode := os.FileMode(0o600)
	if f.fault == "readable prompt file" {
		mode = 0o644
	}
	promptFile := filepath.Join(f.stateDir, "prompt")
	os.Remove(promptFile)
	if err := os.WriteFile(promptFile, []byte(prompt), mode); err != nil && f.fault != "success without delivery" {
		return err
	} else if err != nil {
		return nil
	}
	saved := f.session()
	if f.fault == "session in memory" {
		saved = f.memory
	}
	s := Sent{Prompt: prompt, Argv: []string{"agent", "run"}, Session: saved}
	switch f.fault {
	case "prompt in argv":
		s.Argv = append(s.Argv, prompt)
	case "prompt in environment":
		s.InEnv = true
	case "rewrites prompt":
		s.Prompt = strings.ToUpper(prompt)
	case "new session each time":
		s.Session = ""
	case "sends twice":
		f.sent = append(f.sent, s)
	}
	f.sent = append(f.sent, s)
	switch f.fault {
	case "forgets session":
	case "session in memory":
		f.memory = "session-example"
	default:
		os.WriteFile(filepath.Join(f.stateDir, "session"), []byte("session-example"), 0o600)
	}
	if f.situation == Active {
		f.agents++ // a second agent beside the one that is working
		return nil
	}
	f.situation = Idle
	return nil
}

func (f *fakeAgent) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fault == "kills foreign work" {
		f.foreign = false
	}
	if f.fault != "stop does nothing" && f.situation == Active {
		f.situation, f.agents = Idle, 0
	}
	return nil
}

// fakeRig stages the fake agent directly.
type fakeRig struct{ f *fakeAgent }

func (r fakeRig) Supports(Situation) bool { return true }
func (r fakeRig) Stage(_ context.Context, _ adapter.Adapter, s Situation) error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	r.f.situation, r.f.agents = s, 0
	if s == Active {
		r.f.agents = 1
	}
	return nil
}
func (r fakeRig) Sent() ([]Sent, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return append([]Sent(nil), r.f.sent...), nil
}
func (r fakeRig) Running() (int, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return r.f.agents, nil
}
func (r fakeRig) Settle(context.Context, adapter.Adapter) error { return nil }
func (r fakeRig) CodedClass() decide.ErrorClass                 { return decide.ErrRateLimit }
func (r fakeRig) ReportedSession() string                       { return "session-example" }
func (r fakeRig) StartForeign() (func() bool, func(), error) {
	r.f.foreign = true
	return func() bool { return r.f.foreign }, func() { r.f.foreign = false }, nil
}

func fakeSubject(t *testing.T, fault string) Subject {
	f := &fakeAgent{fault: fault, situation: Idle, stateDir: t.TempDir()}
	return Subject{Name: "fake", New: func() (adapter.Adapter, error) { return f, nil }, Rig: fakeRig{f}, CallLimit: 300 * time.Millisecond, StateDir: f.stateDir,
		Declared: Declared{ExactResume: true, Stoppable: true, StructuredErrors: true, StructuredWaiting: true}}
}

func outcomes(results []Result) map[string]Result {
	out := map[string]Result{}
	for _, r := range results {
		out[r.ID] = r
	}
	return out
}

// A fake that keeps every contract passes every case that applies to it.
func TestAConformingAdapterPassesEveryCase(t *testing.T) {
	for id, r := range outcomes(Run(context.Background(), fakeSubject(t, ""), Options{})) {
		unwritable := id == "state-unwritable" && (runtime.GOOS == "windows" || os.Geteuid() == 0)
		if r.Outcome == Fail || (r.Outcome == Skipped && id != "writes-stay-inside" && !unwritable) {
			t.Errorf("%s: %s %s", id, r.Outcome, r.Detail)
		}
	}
}

// Each fault breaks one contract. The case for that contract fails, says
// what to change, and no unrelated case fails with it.
func TestEachFaultFailsItsOwnCase(t *testing.T) {
	for fault, want := range map[string]struct{ id, says string }{
		"invents a state":             {"never-guess", "is not one of idle, active, pending, stopping, error, unknown"},
		"reads standard error":        {"structured-errors-only", "Never read standard error"},
		"classifies an unmapped code": {"structured-errors-only", "Map only the codes you know"},
		"healthy on a failed exit":    {"failed-is-not-healthy", "Check the exit code before the output"},
		"classifies a crash":          {"failed-is-not-healthy", "a turn with no result"},
		"prompt in environment":       {"send-once", "environment"},
		"readable prompt file":        {"send-once", "mode 0600"},
		"second agent":                {"send-while-working", "another agent beside it"},
		"torn read during send":       {"observe-during-send", "not one valid state"},
		"success without delivery":    {"state-unwritable", "reported success"},
		"session in memory":           {"exact-resume", "not saved in the state directory"},
		"guesses":                     {"never-guess", "do not guess"},
		"misses waiting":              {"waiting-on-person", "awaiting input"},
		"ignores codes":               {"structured-errors-only", "Map the agent's error code"},
		"reads prose":                 {"structured-errors-only", "Never match words"},
		"prompt in argv":              {"send-once", "standard input or in a private file"},
		"rewrites prompt":             {"send-once", "unchanged"},
		"sends twice":                 {"send-once", "exactly once"},
		"resends on read":             {"send-once", "must never re-send"},
		"new session each time":       {"exact-resume", "started a new conversation"},
		"forgets session":             {"exact-resume", "not saved in the state directory"},
		"hangs":                       {"hang-is-cut", "timeout"},
		"idle while hung":             {"hang-is-cut", "never idle"},
		"kills foreign work":          {"stop-own-work", "did not start"},
		"stop does nothing":           {"stop-own-work", "still active"},
		"unbounded":                   {"bounded-output", "must read as unknown"},
	} {
		t.Run(fault, func(t *testing.T) {
			if (fault == "readable prompt file" || fault == "success without delivery") && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("file modes do not restrict this user on this platform")
			}
			got := outcomes(Run(context.Background(), fakeSubject(t, fault), Options{Fresh: func() (Subject, error) { return fakeSubject(t, fault), nil }}))
			if r := got[want.id]; r.Outcome != Fail || !strings.Contains(r.Detail, want.says) {
				t.Fatalf("%s: %s %q; want a failure that says %q", want.id, r.Outcome, r.Detail, want.says)
			}
			for id, r := range got {
				// A broken declared capability also fails the declaration
				// case, and a resend also breaks resume's delivery count.
				// Every case that counts deliveries sees a duplicate.
				duplicate := (fault == "resends on read" || fault == "sends twice") && strings.Contains(r.Detail, "times")
				related := id == want.id || id == "declared-matches" || duplicate ||
					(fault == "hangs" && r.Outcome == Fail && strings.Contains(r.Detail, "status read"))
				if r.Outcome == Fail && !related {
					t.Errorf("unrelated case %s also failed: %s", id, r.Detail)
				}
			}
		})
	}
}

// A real agent has no stand-in. Only the status case runs, nothing is ever
// sent, and nothing the adapter says about a failure is repeated.
func TestARealAgentIsOnlyRead(t *testing.T) {
	f := &fakeAgent{situation: Idle, stateDir: t.TempDir()}
	live := Subject{Name: "real", New: func() (adapter.Adapter, error) { return f, nil }}
	got := outcomes(Run(context.Background(), live, Options{}))
	if got["one-state"].Outcome != Pass || len(f.sent) != 0 {
		t.Fatalf("%+v sent=%d", got["one-state"], len(f.sent))
	}
	for id, r := range got {
		if id != "one-state" && id != "declared-matches" && r.Outcome != Skipped {
			t.Errorf("%s ran against a real agent: %s", id, r.Outcome)
		}
	}
	unbuilt := Subject{Name: "real", New: func() (adapter.Adapter, error) { return nil, errors.New("open /fictional/private/path: denied") }}
	for _, r := range Run(context.Background(), unbuilt, Options{}) {
		if strings.Contains(r.Detail, "/fictional/private/path") {
			t.Fatalf("%s repeated the adapter's error: %s", r.ID, r.Detail)
		}
	}
}

// The states a status read may return, and the ones it may not.
func TestValidObservations(t *testing.T) {
	for _, obs := range []decide.Observation{
		{State: decide.Idle}, {State: decide.Idle, ErrorClass: decide.ErrNone}, {State: decide.Active}, {State: decide.Pending}, {State: decide.Stopping},
		{State: decide.Active, ErrorClass: decide.ErrOverloaded}, // working through its own retry
		{State: decide.Error, ErrorClass: decide.ErrUnknown}, {State: decide.Error, ErrorClass: decide.ErrRateLimit}, {State: decide.Unknown},
		{State: decide.Unknown, ErrorClass: decide.ErrUnknown}, {State: decide.Idle, AwaitingInput: true},
	} {
		if err := valid(obs); err != nil {
			t.Errorf("%+v: %v", obs, err)
		}
	}
	for _, obs := range []decide.Observation{
		{State: "busy"}, {State: ""}, {State: decide.Idle, ErrorClass: decide.ErrRateLimit}, {State: decide.Pending, ErrorClass: decide.ErrAuth},
		{State: decide.Error}, {State: decide.Error, ErrorClass: "usage limit"}, {State: decide.Active, ErrorClass: "usage limit"},
		{State: decide.Unknown, ErrorClass: decide.ErrRateLimit},
	} {
		if valid(obs) == nil {
			t.Errorf("%+v was accepted", obs)
		}
	}
}

// Something written outside the state directory fails its own case.
func TestWritesOutsideTheStateDirectoryAreCaught(t *testing.T) {
	project := t.TempDir()
	f := &fakeAgent{situation: Idle, stateDir: t.TempDir()}
	s := Subject{Name: "fake", New: func() (adapter.Adapter, error) { return f, nil }, Rig: fakeRig{f}, Protected: []string{project}}
	if r := outcomes(Run(context.Background(), s, Options{}))["writes-stay-inside"]; r.Outcome != Pass {
		t.Fatalf("%+v", r)
	}
	leaky := Subject{Name: "fake", Rig: fakeRig{f}, Protected: []string{project}, New: func() (adapter.Adapter, error) {
		return f, os.WriteFile(filepath.Join(project, "scratch.tmp"), []byte("x"), 0o600)
	}}
	r := outcomes(Run(context.Background(), leaky, Options{}))["writes-stay-inside"]
	if r.Outcome != Fail || !strings.Contains(r.Detail, "created") || !strings.Contains(r.Detail, "state_dir") {
		t.Fatalf("%+v", r)
	}
}

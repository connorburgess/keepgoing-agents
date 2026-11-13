package conformance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// fakeAgent is an in-memory agent and adapter in one. With no fault it keeps
// every contract. Each fault breaks exactly one, to show the suite catches it
// and says what to change.
type fakeAgent struct {
	fault     string
	situation Situation
	sent      []Sent
	session   string // what the adapter has saved
	foreign   bool   // foreign work is alive
}

func (f *fakeAgent) Name() string { return "fake" }

func (f *fakeAgent) Observe(ctx context.Context) (decide.Observation, error) {
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
	case Hanging:
		if f.fault == "hangs" {
			<-ctx.Done()
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
	s := Sent{Prompt: prompt, Argv: []string{"agent", "run"}, Session: f.session}
	switch f.fault {
	case "prompt in argv":
		s.Argv = append(s.Argv, prompt)
	case "rewrites prompt":
		s.Prompt = strings.ToUpper(prompt)
	case "new session each time":
		s.Session = ""
	case "sends twice":
		f.sent = append(f.sent, s)
	}
	f.sent = append(f.sent, s)
	if f.fault != "forgets session" {
		f.session = "session-example"
	}
	f.situation = Idle
	return nil
}

func (f *fakeAgent) Stop(context.Context) error {
	if f.fault == "kills foreign work" {
		f.foreign = false
	}
	if f.fault != "stop does nothing" && f.situation == Active {
		f.situation = Idle
	}
	return nil
}

// fakeRig stages the fake agent directly.
type fakeRig struct{ f *fakeAgent }

func (r fakeRig) Supports(Situation) bool { return true }
func (r fakeRig) Stage(_ context.Context, _ adapter.Adapter, s Situation) error {
	r.f.situation = s
	return nil
}
func (r fakeRig) Sent() ([]Sent, error)                         { return append([]Sent(nil), r.f.sent...), nil }
func (r fakeRig) Settle(context.Context, adapter.Adapter) error { return nil }
func (r fakeRig) CodedClass() decide.ErrorClass                 { return decide.ErrRateLimit }
func (r fakeRig) ReportedSession() string                       { return "session-example" }
func (r fakeRig) StartForeign() (func() bool, func(), error) {
	r.f.foreign = true
	return func() bool { return r.f.foreign }, func() { r.f.foreign = false }, nil
}

func fakeSubject(fault string) Subject {
	f := &fakeAgent{fault: fault, situation: Idle}
	return Subject{Name: "fake", New: func() (adapter.Adapter, error) { return f, nil }, Rig: fakeRig{f}, CallLimit: 300 * time.Millisecond,
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
	for id, r := range outcomes(Run(context.Background(), fakeSubject(""), Options{})) {
		if r.Outcome == Fail || (r.Outcome == Skipped && id != "live-send" && id != "writes-stay-inside") {
			t.Errorf("%s: %s %s", id, r.Outcome, r.Detail)
		}
	}
}

// Each fault breaks one contract. The case for that contract fails, says
// what to change, and no unrelated case fails with it.
func TestEachFaultFailsItsOwnCase(t *testing.T) {
	for fault, want := range map[string]struct{ id, says string }{
		"invents a state":       {"never-guess", "is not one of idle, active, error, unknown"},
		"guesses":               {"never-guess", "do not guess"},
		"misses waiting":        {"waiting-on-person", "awaiting input"},
		"ignores codes":         {"structured-errors-only", "Map the agent's error code"},
		"reads prose":           {"structured-errors-only", "Never match words"},
		"prompt in argv":        {"send-once", "standard input or in a private file"},
		"rewrites prompt":       {"send-once", "unchanged"},
		"sends twice":           {"send-once", "exactly once"},
		"resends on read":       {"send-once", "must never re-send"},
		"new session each time": {"exact-resume", "started a new conversation"},
		"forgets session":       {"exact-resume", "started a new conversation"},
		"hangs":                 {"hang-is-cut", "timeout"},
		"idle while hung":       {"hang-is-cut", "never idle"},
		"kills foreign work":    {"stop-own-work", "did not start"},
		"stop does nothing":     {"stop-own-work", "still active"},
		"unbounded":             {"bounded-output", "must read as unknown"},
	} {
		t.Run(fault, func(t *testing.T) {
			got := outcomes(Run(context.Background(), fakeSubject(fault), Options{Fresh: func() (Subject, error) { return fakeSubject(fault), nil }}))
			if r := got[want.id]; r.Outcome != Fail || !strings.Contains(r.Detail, want.says) {
				t.Fatalf("%s: %s %q; want a failure that says %q", want.id, r.Outcome, r.Detail, want.says)
			}
			for id, r := range got {
				// A broken declared capability also fails the declaration
				// case, and a resend also breaks resume's delivery count.
				related := id == want.id || id == "declared-matches" || (fault == "resends on read" && id == "exact-resume") || (fault == "sends twice" && id == "exact-resume") ||
					(fault == "hangs" && r.Outcome == Fail && strings.Contains(r.Detail, "status read"))
				if r.Outcome == Fail && !related {
					t.Errorf("unrelated case %s also failed: %s", id, r.Detail)
				}
			}
		})
	}
}

// A real agent has no stand-in. Only the status case runs, and nothing is
// sent unless that is allowed explicitly.
func TestARealAgentIsOnlyReadUnlessSendingIsAllowed(t *testing.T) {
	f := &fakeAgent{situation: Idle}
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
	if !strings.Contains(got["live-send"].Detail, "allow it explicitly") {
		t.Fatal(got["live-send"].Detail)
	}
	got = outcomes(Run(context.Background(), live, Options{Send: true}))
	if got["live-send"].Outcome != Pass || len(f.sent) != 1 || f.sent[0].Prompt != CheckPrompt {
		t.Fatalf("%+v sent=%v", got["live-send"], f.sent)
	}
}

// Something written outside the state directory fails its own case.
func TestWritesOutsideTheStateDirectoryAreCaught(t *testing.T) {
	project := t.TempDir()
	f := &fakeAgent{situation: Idle}
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

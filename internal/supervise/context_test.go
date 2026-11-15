package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

// contextFake is a fake adapter that can also read its context size.
type contextFake struct {
	*fakeAdapter
	reading adapter.ContextReading
	err     error
	block   bool // wait for the check to give up
	calls   int
}

func (f *contextFake) ReadContext(ctx context.Context) (adapter.ContextReading, error) {
	f.calls++
	if f.block {
		<-ctx.Done()
		return adapter.ContextReading{}, ctx.Err()
	}
	return f.reading, f.err
}

var sampleReading = adapter.ContextReading{UsedTokens: 48_000, WindowTokens: 200_000, Estimated: true}

// contextWorld is a world whose primary can read its context size.
func contextWorld(t *testing.T, obs decide.Observation) (*world, *contextFake) {
	t.Helper()
	w := newWorld(t, true, false)
	f := &contextFake{fakeAdapter: w.primary(obs), reading: sampleReading}
	return w, f
}

func (w *world) contextOpts(now time.Time, f *contextFake) Options {
	o := w.opts(now)
	o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return f, nil }
	return o
}

// The rule for when a check reads the context, one row per case.
func TestContextReadDue(t *testing.T) {
	idle := decide.Observation{State: decide.Idle}
	active := decide.Observation{State: decide.Active}
	act := func(a decide.Action) decide.Decision { return decide.Decision{Action: a} }
	cases := []struct {
		name     string
		d        decide.Decision
		obs      decide.Observation
		previous string
		held     bool
		same     bool
		dry      bool
		want     bool
	}{
		{"wake", act(decide.Wake), idle, "idle", false, true, false, true},
		{"re-prompt", act(decide.Reprompt), decide.Observation{State: decide.Error}, "error", false, true, false, true},
		{"nudge sent", act(decide.Nudge), active, "active", false, true, false, true},
		{"nudge held back sends nothing", act(decide.Nudge), active, "active", true, true, false, false},
		{"turn just ended, nothing sent", act(decide.None), idle, "active", false, true, false, true},
		{"turn ended after an error", act(decide.None), idle, "error", false, true, false, true},
		{"idle again, already seen", act(decide.None), idle, "idle", false, true, false, false},
		{"first check ever", act(decide.None), idle, "", false, true, false, false},
		{"still active", act(decide.None), active, "active", false, true, false, false},
		{"hold raised", act(decide.AwaitUser), idle, "active", false, true, false, false},
		{"stop", act(decide.Stop), idle, "active", false, true, false, false},
		{"handoff moved the loop", act(decide.Handoff), idle, "active", false, false, false, false},
		{"switch-back moved the loop", act(decide.SwitchBack), idle, "active", false, false, false, false},
		{"wake on a dry run", act(decide.Wake), idle, "idle", false, true, true, false},
	}
	for _, c := range cases {
		if got := contextReadDue(c.d, c.obs, c.previous, c.held, c.same, c.dry); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWakeRecordsTheContextReadingOnce(t *testing.T) {
	w, f := contextWorld(t, decide.Observation{State: decide.Idle})
	res, err := Tick(context.Background(), w.cfg, w.contextOpts(t0, f))
	if err != nil || res.Action != "wake" || len(f.sent) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if f.calls != 1 {
		t.Fatalf("read %d times in one check, want 1", f.calls)
	}
	got := loadState(t, w).Context
	r := state.ContextReading{UsedTokens: 48_000, WindowTokens: 200_000, Estimated: true, At: t0}
	want := &state.ContextRecord{Version: state.ContextVersion, Target: state.Primary, Latest: r, Recent: []state.ContextReading{r},
		Days: []state.ContextDay{{Day: "2026-11-09", Lowest: 48_000, Highest: 48_000, Last: 48_000, WindowTokens: 200_000, Count: 1, Estimated: true}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded %+v, want %+v", got, want)
	}
}

// A paused, stopped or held check queries no provider, and so reads no
// context; nor does a dry run.
func TestHeldAndDryRunChecksReadNoContext(t *testing.T) {
	for name, hold := range map[string]func(*control.File){
		"paused":        func(f *control.File) { f.Paused = true },
		"stopped":       func(f *control.File) { f.Stop = true },
		"awaiting user": func(f *control.File) { f.AwaitingUser = true },
		"disabled":      func(f *control.File) { f.Enabled = false },
	} {
		w, f := contextWorld(t, decide.Observation{State: decide.Idle})
		if _, err := control.Update(w.dir, hold); err != nil {
			t.Fatal(err)
		}
		Tick(context.Background(), w.cfg, w.contextOpts(t0, f))
		if f.calls != 0 || loadState(t, w).Context != nil {
			t.Errorf("%s: read the context %d times", name, f.calls)
		}
	}
	w, f := contextWorld(t, decide.Observation{State: decide.Idle})
	o := w.contextOpts(t0, f)
	o.DryRun = true
	Tick(context.Background(), w.cfg, o)
	if f.calls != 0 {
		t.Errorf("dry run read the context %d times", f.calls)
	}
}

// An unavailable reading (older bb, a malformed answer) fails nothing, warns
// nothing, and leaves the last reading with its own time.
func TestUnavailableContextReadingIsQuiet(t *testing.T) {
	w, f := contextWorld(t, decide.Observation{State: decide.Idle})
	if _, err := Tick(context.Background(), w.cfg, w.contextOpts(t0, f)); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("bb context reading unavailable")
	later := t0.Add(2 * time.Hour)
	res, err := Tick(context.Background(), w.cfg, w.contextOpts(later, f))
	if err != nil || res.Action != "wake" || len(res.Warnings) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if st := loadState(t, w); st.Context == nil || !st.Context.Latest.At.Equal(t0) || len(st.Context.Recent) != 1 || st.LastCheck.Outcome != "succeeded" {
		t.Fatalf("state after an unavailable reading: %+v %+v", st.Context, st.LastCheck)
	}
}

// A check with no room left in its budget does not start a reading, and a
// reading that does not answer is given up within what is left.
func TestContextReadingStaysInsideTheBudget(t *testing.T) {
	w, f := contextWorld(t, decide.Observation{State: decide.Idle})
	o := w.contextOpts(t0, f)
	o.Deadline = time.Now().Add(reserveReports + minReportTime) // room for a report, none for a reading
	res, err := Tick(context.Background(), w.cfg, o)
	if err != nil || res.Action != "wake" || f.calls != 0 {
		t.Fatalf("no room: %+v %v, %d reads", res, err, f.calls)
	}

	w, f = contextWorld(t, decide.Observation{State: decide.Idle})
	f.block = true
	o = w.contextOpts(t0, f)
	o.Deadline = time.Now().Add(reserveReports + minReportTime + 1500*time.Millisecond)
	start := time.Now()
	res, err = Tick(context.Background(), w.cfg, o)
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("a reading that did not answer held the check for %v", took)
	}
	if err != nil || res.Action != "wake" || len(f.sent) != 1 || f.calls != 1 || len(res.Warnings) != 0 {
		t.Fatalf("slow: %+v %v, %d reads", res, err, f.calls)
	}
	if loadState(t, w).Context != nil {
		t.Fatal("a reading that did not answer was recorded")
	}
}

// A check decides and acts identically whether or not a context reading
// exists, is recorded, or is unavailable: the reading is an input to nothing.
func TestContextReadingChangesNoDecision(t *testing.T) {
	steps := []decide.Observation{
		{State: decide.Idle},
		{State: decide.Active},
		{State: decide.Active},
		{State: decide.Idle},
		{State: decide.Error, ErrorClass: decide.ErrUnknown},
		{State: decide.Error, ErrorClass: decide.ErrUnknown},
		{State: decide.Error, ErrorClass: decide.ErrUnknown},
		{State: decide.Error, ErrorClass: decide.ErrRateLimit},
		{State: decide.Idle},
	}
	type outcome struct {
		res   []Result
		sent  []string
		state state.File
	}
	run := func(reader func(*fakeAdapter) adapter.Adapter) outcome {
		w := newWorld(t, true, false)
		w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
		p := w.primary(decide.Observation{})
		a := reader(p)
		var out outcome
		for i, obs := range steps {
			p.obs = obs
			o := w.opts(t0.Add(time.Duration(i) * 40 * time.Minute))
			o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return a, nil }
			o.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
			res, err := Tick(context.Background(), w.cfg, o)
			if err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
			res.At = time.Time{}
			out.res = append(out.res, res)
		}
		out.sent = p.sent
		out.state = loadState(t, w)
		out.state.Context, out.state.CheckFinishedAt = nil, time.Time{} // the latter is wall time
		return out
	}
	without := run(func(p *fakeAdapter) adapter.Adapter { return p })
	with := run(func(p *fakeAdapter) adapter.Adapter { return &contextFake{fakeAdapter: p, reading: sampleReading} })
	huge := run(func(p *fakeAdapter) adapter.Adapter {
		return &contextFake{fakeAdapter: p, reading: adapter.ContextReading{UsedTokens: 199_999, WindowTokens: 200_000}}
	})
	failing := run(func(p *fakeAdapter) adapter.Adapter {
		return &contextFake{fakeAdapter: p, err: errors.New("unavailable")}
	})
	enc := func(o outcome) string {
		b, _ := json.Marshal(o.res)
		s, _ := json.Marshal(o.state)
		return string(b) + "\n" + string(s) + "\n" + jsonString(o.sent)
	}
	base := enc(without)
	for name, o := range map[string]outcome{"with a reading": with, "with a near-full reading": huge, "with an unavailable reading": failing} {
		if got := enc(o); got != base {
			t.Errorf("%s: the checks differ from those without a reading\nwithout: %s\n   with: %s", name, base, got)
		}
	}
	if without.res[0].Action != "wake" {
		t.Fatalf("the sequence no longer starts with a wake: %+v", without.res[0])
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The reading belongs to the conversation it was taken from: a change of
// target clears it with the thread's other observations.
func TestTargetChangeClearsTheContextReading(t *testing.T) {
	st := state.File{}
	st.NoteContext(state.Primary, state.ContextReading{UsedTokens: 1, WindowTokens: 2, At: t0})
	st.ClearChildErrors()
	if st.Context != nil {
		t.Fatal("the context reading survived a change of target")
	}
}

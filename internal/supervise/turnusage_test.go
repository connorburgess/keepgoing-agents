package supervise

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

// usageFake is a fake adapter whose agent reports its latest turn's usage.
type usageFake struct {
	*fakeAdapter
	usage adapter.TurnUsage
	ok    bool
	calls int
}

func (f *usageFake) LatestTurnUsage() (adapter.TurnUsage, bool) {
	f.calls++
	return f.usage, f.ok
}

func i64(n int64) *int64 { return &n }

var sampleUsage = adapter.TurnUsage{ID: "turn-example-1", Input: i64(21_000), CachedInput: i64(13_000), Output: i64(5)}

// The rule for when a check records turn usage, one row per case.
func TestTurnUsageReadDue(t *testing.T) {
	act := func(a decide.Action) decide.Decision { return decide.Decision{Action: a} }
	obs := func(s decide.State) decide.Observation { return decide.Observation{State: s} }
	cases := []struct {
		name      string
		d         decide.Decision
		obs       decide.Observation
		same, dry bool
		want      bool
	}{
		{"wake after a finished turn", act(decide.Wake), obs(decide.Idle), true, false, true},
		{"idle, nothing sent", act(decide.None), obs(decide.Idle), true, false, true},
		{"re-prompt after an error", act(decide.Reprompt), obs(decide.Error), true, false, true},
		{"hold raised on a finished turn", act(decide.AwaitUser), obs(decide.Idle), true, false, true},
		{"turn still running", act(decide.Nudge), obs(decide.Active), true, false, false},
		{"state unknown", act(decide.None), obs(decide.Unknown), true, false, false},
		{"stop", act(decide.Stop), obs(decide.Idle), true, false, false},
		{"handoff moved the loop", act(decide.Handoff), obs(decide.Error), false, false, false},
		{"dry run", act(decide.Wake), obs(decide.Idle), true, true, false},
	}
	for _, c := range cases {
		if got := turnUsageReadDue(c.d, c.obs, c.same, c.dry); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Two checks that see the same finished turn record it once.
func TestTurnUsageIsRecordedOncePerTurn(t *testing.T) {
	w := newWorld(t, true, false)
	f := &usageFake{fakeAdapter: w.primary(decide.Observation{State: decide.Idle}), usage: sampleUsage, ok: true}
	for i := range 2 {
		o := w.opts(t0.Add(time.Duration(i) * 40 * time.Minute))
		o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return f, nil }
		if _, err := Tick(context.Background(), w.cfg, o); err != nil {
			t.Fatal(err)
		}
	}
	got := loadState(t, w).TurnUsage
	if f.calls != 2 || got == nil || got.Latest.Turn != "turn-example-1" || !got.Latest.At.Equal(t0) || len(got.Days) != 1 ||
		got.Days[0].Turns != 1 || got.Days[0].Input != (state.TurnUsageSum{Tokens: 21_000, Turns: 1}) {
		t.Fatalf("%d reads, recorded %+v", f.calls, got)
	}
}

// A check decides, sends and saves the same state whether or not the agent
// reports a usage object, a huge one, or none: the reading is an input to
// nothing.
func TestTurnUsageChangesNoDecision(t *testing.T) {
	steps := []decide.Observation{
		{State: decide.Idle},
		{State: decide.Active},
		{State: decide.Idle},
		{State: decide.Error, ErrorClass: decide.ErrUnknown},
		{State: decide.Error, ErrorClass: decide.ErrUnknown},
		{State: decide.Error, ErrorClass: decide.ErrRateLimit},
		{State: decide.Idle},
	}
	type outcome struct {
		res   []Result
		sent  []string
		state state.File
		usage *state.TurnUsageRecord
	}
	run := func(reader func(*fakeAdapter, int) adapter.Adapter) outcome {
		w := newWorld(t, true, false)
		w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
		p := w.primary(decide.Observation{})
		var out outcome
		for i, obs := range steps {
			p.obs = obs
			a := reader(p, i)
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
		out.usage = out.state.TurnUsage
		out.state.TurnUsage, out.state.CheckFinishedAt = nil, time.Time{} // the latter is wall time
		return out
	}
	turn := func(i int, u adapter.TurnUsage) adapter.TurnUsage {
		u.ID = "turn-example-" + string(rune('a'+i))
		return u
	}
	without := run(func(p *fakeAdapter, _ int) adapter.Adapter { return p })
	with := run(func(p *fakeAdapter, i int) adapter.Adapter {
		return &usageFake{fakeAdapter: p, usage: turn(i, sampleUsage), ok: true}
	})
	huge := run(func(p *fakeAdapter, i int) adapter.Adapter {
		big := i64(10_000_000_000)
		return &usageFake{fakeAdapter: p, usage: turn(i, adapter.TurnUsage{Input: big, CachedInput: big, CacheWriteInput: big, Output: big, ReasoningOutput: big}), ok: true}
	})
	none := run(func(p *fakeAdapter, _ int) adapter.Adapter { return &usageFake{fakeAdapter: p} })
	enc := func(o outcome) string {
		b, _ := json.Marshal(o.res)
		s, _ := json.Marshal(o.state)
		return string(b) + "\n" + string(s) + "\n" + jsonString(o.sent)
	}
	base := enc(without)
	for name, o := range map[string]outcome{"with usage": with, "with huge usage": huge, "with no usage reported": none} {
		if got := enc(o); got != base {
			t.Errorf("%s: the checks differ from those without usage\nwithout: %s\n   with: %s", name, base, got)
		}
	}
	if with.usage == nil || with.usage.Days[0].Turns < 2 || without.usage != nil || none.usage != nil {
		t.Fatalf("the runs did not record what they should: with %+v, without %+v, none %+v", with.usage, without.usage, none.usage)
	}
	if without.res[0].Action != "wake" {
		t.Fatalf("the sequence no longer starts with a wake: %+v", without.res[0])
	}
}

// The decision engine and usage-aware timing never refer to turn usage.
func TestDecisionPackagesDoNotReadTurnUsage(t *testing.T) {
	for _, dir := range []string{"../decide", "../timing"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "TurnUsage") {
				t.Errorf("%s refers to turn usage", file)
			}
		}
	}
}

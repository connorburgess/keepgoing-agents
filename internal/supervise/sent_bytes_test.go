package supervise

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/health"
	"github.com/looptender/looptender/internal/probe"
	"github.com/looptender/looptender/internal/state"
)

// sentBytesGolden holds every byte the journey below sends, as main sent it
// before loop.prompt_context existed. It was written by this same test run
// against that main with LOOPTENDER_UPDATE_SENT_BYTES=1. The test uses no
// newer API, so it still runs there.
const sentBytesGolden = "testdata/sent_bytes_main.golden"

// sentJourney runs one loop through a wake, a nudge, a re-prompt, a
// handoff with its brief, a wake on the fallback, a switch-back with its
// brief, and, on a second loop, a wake that is only a child-error notice. It
// returns every send in order: target, mode and text. configure adjusts both
// loops before their first check.
func sentJourney(t *testing.T, configure func(*config.Loop)) string {
	t.Helper()
	var out strings.Builder
	sent := func(label string, adapters map[string]*fakeAdapter) {
		for _, id := range []string{state.Primary, "same-account", "claude-other", "codex-main"} {
			f := adapters[id]
			if f == nil {
				continue
			}
			for i, text := range f.sent {
				fmt.Fprintf(&out, "=== %s %s #%d mode=%s\n%s\n", label, id, i, f.modes[i], text)
			}
			f.sent, f.modes = nil, nil
		}
	}

	w := newWorld(t, true, true)
	w.cfg.Loop.Progress.Repositories = []string{"/example/repo"}
	w.cfg.Loop.SwitchBack = config.SwitchBack{Enabled: true, MinAfter: config.Duration{Duration: time.Hour}, RetryEvery: config.Duration{Duration: 30 * time.Minute}}
	w.cfg.Loop.Probe = config.Probe{Kind: "command", Command: []string{"x"}}
	configure(&w.cfg.Loop)
	p := w.primary(decide.Observation{State: decide.Idle})
	reg := &health.Registry{Candidates: map[string]*health.Entry{}}
	for _, id := range []string{"same-account", "claude-other", "codex-main"} {
		reg.Record(id, probe.Result{At: t0.Add(-time.Hour), Tier: probe.Scheduled, OK: true})
	}
	reg.Save(w.dir)
	w.probeOK = map[string]bool{"codex-main": true}
	tick := func(at time.Duration, want string) {
		t.Helper()
		o := w.opts(t0.Add(at))
		o.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
		inner := o.NewAdapter
		o.NewAdapter = func(a config.Agent, dir, target string) (adapter.Adapter, error) {
			ad, err := inner(a, dir, target)
			if target == state.Primary {
				return bbNamedAdapter{ad.(*fakeAdapter)}, err
			}
			return ad, err
		}
		res, err := Tick(context.Background(), w.cfg, o)
		if err != nil || res.Action != want {
			t.Fatalf("at %v: %+v %v, want %s", at, res, err, want)
		}
		sent(fmt.Sprintf("%v %s", at, want), w.adapters)
	}
	tick(0, "wake")
	p.obs = decide.Observation{State: decide.Active}
	tick(61*time.Minute, "nudge")
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
	tick(2*time.Hour, "reprompt")
	p.obs = limited
	tick(2*time.Hour+30*time.Minute, "handoff")
	tick(4*time.Hour, "wake")
	p.obs = decide.Observation{State: decide.Idle}
	w.probeOK[state.Primary] = true
	tick(4*time.Hour+40*time.Minute, "switch_back")

	c := newWorld(t, true, false)
	configure(&c.cfg.Loop)
	cp := c.primary(decide.Observation{State: decide.Idle, ActiveChildren: 1, ErroredChildren: children("thr_child_a")})
	o := c.opts(t0)
	o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return bbNamedAdapter{cp}, nil }
	if res, err := Tick(context.Background(), c.cfg, o); err != nil || res.Action != "wake" || res.Reason != decide.ChildErrorWakeReason {
		t.Fatalf("notice-only wake: %+v %v", res, err)
	}
	sent("child-error notice", map[string]*fakeAdapter{state.Primary: cp})
	return out.String()
}

// With loop.prompt_context off, every byte sent is what main sent.
func TestPromptContextOffSendsMainBytes(t *testing.T) {
	got := sentJourney(t, func(*config.Loop) {})
	if os.Getenv("LOOPTENDER_UPDATE_SENT_BYTES") == "1" {
		if err := os.MkdirAll(filepath.Dir(sentBytesGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sentBytesGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(sentBytesGolden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("sent bytes differ from main:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

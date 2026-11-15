package supervise

import (
	"context"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/prompt"
)

type bbNamedAdapter struct{ *fakeAdapter }

func (bbNamedAdapter) Name() string { return "bb" }

// Every message to a BB thread starts with the fixed line; a headless or
// other target gets the configured prompt unchanged.
func TestWakeHeaderOnlyForBBTargets(t *testing.T) {
	for _, bb := range []bool{true, false} {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Idle})
		o := w.opts(t0)
		if bb {
			o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return bbNamedAdapter{p}, nil }
		}
		if res, err := Tick(context.Background(), w.cfg, o); err != nil || res.Action != "wake" || len(p.sent) != 1 {
			t.Fatalf("bb=%v: %+v %v", bb, res, err)
		}
		want, _ := prompt.Wake(w.cfg.Loop)
		if bb {
			want = prompt.WakeHeader(w.cfg.Loop.Name) + "\n\n" + want
		}
		if p.sent[0] != want {
			t.Fatalf("bb=%v: sent %q, want %q", bb, p.sent[0], want)
		}
		if !bb && strings.Contains(p.sent[0], "Automatic looptender wake") {
			t.Fatal("header sent to a target that is not a BB thread")
		}
	}
}

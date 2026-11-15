package supervise

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/state"
)

// A failed idle wake is not recorded: it started no turn, so it is not a
// wake that changed nothing, and the next check wakes again rather than
// waiting a growing interval. The send still counts toward max_sends as
// every other attempt does.
func TestIdleSpacingIgnoresFailedWakes(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.IdleSpacing = &config.IdleSpacing{First: config.Duration{Duration: 10 * time.Minute}, Max: config.Duration{Duration: 2 * time.Hour}}
	p := w.primary(decide.Observation{State: decide.Idle, Turns: &decide.TurnCounts{}})
	p.sendErr = errors.New("uncertain delivery")
	for i := range 3 {
		before := len(p.sent)
		_, _ = Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*5*time.Minute))) // the send's error is returned
		if len(p.sent) != before+1 {
			t.Fatalf("check %d sent nothing", i)
		}
	}
	st, err := state.Load(w.cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.IdleSpacing != nil {
		t.Fatalf("failed wakes recorded: %+v", st.IdleSpacing)
	}
}

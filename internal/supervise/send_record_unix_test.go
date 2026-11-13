//go:build unix

package supervise

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A check saves the record of a send before it makes it, limit or no limit.
// With a state directory it cannot write, it sends nothing: before, the
// re-prompt was sent, could not be counted, and the next check sent another,
// so the cap that ends in a hold was never reached.
func TestUnwritableStateDirectorySendsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only directory")
	}
	for _, max := range []int64{0, 20} {
		w := newWorld(t, true, false)
		w.cfg.Loop.Limits.MaxSends = max
		p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
		runTick(t, w, t0) // a first check makes the state files
		sent := len(p.sent)
		if err := os.Chmod(w.dir, 0o500); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 6; i++ {
			if _, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*15*time.Minute))); err == nil {
				t.Fatalf("max_sends %d, check %d: no error from an unwritable state directory", max, i)
			}
		}
		os.Chmod(w.dir, 0o700)
		if len(p.sent) != sent {
			t.Fatalf("max_sends %d: %d sends while nothing could be saved", max, len(p.sent)-sent)
		}
	}
}

// The state directory becomes unwritable after the record of the send was
// saved and the send was made, before the check could save its outcome. The
// next check finds the record without an outcome and counts the send as
// made: the re-prompt cap is spent and the backoff runs from it, so it is not
// repeated at once, and the cap still ends in the hold.
func TestSendWithoutARecordedOutcomeIsCountedByTheNextCheck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only directory")
	}
	w := newWorld(t, true, false) // max_reprompts 2, backoff 10m
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	p.onSend = func() { os.Chmod(w.dir, 0o500) }
	t.Cleanup(func() { os.Chmod(w.dir, 0o700) })
	if _, err := Tick(context.Background(), w.cfg, w.opts(t0)); err == nil {
		t.Fatal("the check saved its state in a directory it could not write")
	}
	if len(p.sent) != 1 {
		t.Fatalf("sends %d", len(p.sent))
	}
	p.onSend = nil
	os.Chmod(w.dir, 0o700)
	if st, _ := state.Load(w.dir); st.Reprompts != 0 {
		t.Fatalf("the outcome was saved after all: %+v", st)
	}

	// Inside the backoff: nothing is sent, and the send is now counted.
	r := runTick(t, w, t0.Add(5*time.Minute))
	st, _ := state.Load(w.dir)
	if r.Action != "none" || r.Reason != "backing off after error" || len(p.sent) != 1 || st.Reprompts != 1 || !st.LastRepromptAt.Equal(t0) || !st.LastWakeAt.Equal(t0) {
		t.Fatalf("%s (%s) sends=%d %+v", r.Action, r.Reason, len(p.sent), st)
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "was not recorded, because that check could not save its state; it is counted as made") {
		t.Fatalf("no warning: %v", r.Warnings)
	}
	// Counted once: the next check does not count it again.
	if r = runTick(t, w, t0.Add(10*time.Minute)); r.Action != "reprompt" || len(p.sent) != 2 {
		t.Fatalf("%+v", r)
	}
	if st, _ = state.Load(w.dir); st.Reprompts != 2 {
		t.Fatalf("reprompts %d", st.Reprompts)
	}
	if r = runTick(t, w, t0.Add(20*time.Minute)); r.Action != "await_user" {
		t.Fatalf("%+v", r)
	}
	if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser || len(p.sent) != 2 {
		t.Fatalf("held=%t sends=%d", ctl.AwaitingUser, len(p.sent))
	}
}

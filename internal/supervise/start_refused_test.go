package supervise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// An adapter that refuses to start a turn, because of something on this
// machine, has sent nothing. The check spends nothing on it: no send, no
// re-prompt and no hold however long it lasts. It is reported once, stands
// in the state for status and doctor, and ends with the first accepted send.
func TestARefusedStartSpendsNothingAndIsReportedOnce(t *testing.T) {
	const reason = "the fictional binary on disk is not the one that is running: restart the daemon"
	w := newWorld(t, true, false)
	w.cfg.Loop.Limits.MaxSends = 3
	w.cfg.Loop.Errors.MaxReprompts = 2
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	p.sendErr = adapter.ErrNotStarted{Reason: reason}
	now := t0
	check := func() Result {
		t.Helper()
		now = now.Add(time.Hour)
		res, err := Tick(context.Background(), w.cfg, w.opts(now))
		if err != nil {
			t.Fatalf("a refused start failed the check: %v", err)
		}
		return res
	}
	started := func() int {
		n := 0
		for _, e := range w.reports {
			if e.Type == "error" && e.Data["cause"] == "start_refused" && strings.Contains(e.Text, reason) {
				n++
			}
		}
		return n
	}
	// Far more checks than there are sends or re-prompts to spend.
	for i := 0; i < 8; i++ {
		res := check()
		if res.Action != string(decide.Reprompt) || !strings.Contains(res.Reason, reason) || !strings.Contains(res.Reason, "nothing was sent") {
			t.Fatalf("check %d: %+v", i, res)
		}
	}
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	used, err := state.LoadSends(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	if used != 0 || st.Reprompts != 0 || st.Nudges != 0 || st.FailedChecks != 0 {
		t.Fatalf("eight refused starts spent %d sends, %d re-prompts, %d nudges and failed %d checks", used, st.Reprompts, st.Nudges, st.FailedChecks)
	}
	if st.StartRefused == nil || st.StartRefused.Reason != reason || !st.StartRefused.Since.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v", st.StartRefused)
	}
	if st.LastPromptSend == nil || st.LastPromptSend.Outcome != "not_started" {
		t.Fatalf("%+v", st.LastPromptSend)
	}
	if got := started(); got != 1 {
		t.Fatalf("the refusal was reported %d times, want once", got)
	}
	for _, e := range w.reports {
		if e.Type == "await_user" {
			t.Fatalf("a refused start ended in a hold: %+v", e)
		}
	}
	// The adapter starts turns again: the send is made and counted, and the
	// refusal no longer stands.
	p.sendErr = nil
	if res := check(); res.Action != string(decide.Reprompt) || strings.Contains(res.Reason, "nothing was sent") {
		t.Fatalf("%+v", res)
	}
	st, _ = state.Load(w.dir)
	used, _ = state.LoadSends(w.dir)
	if st.StartRefused != nil || used != 1 || st.Reprompts != 1 {
		t.Fatalf("after an accepted send: refused=%+v sends=%d re-prompts=%d", st.StartRefused, used, st.Reprompts)
	}
	// A second refusal later is a new one, and is reported again.
	p.sendErr = adapter.ErrNotStarted{Reason: reason}
	check()
	if got := started(); got != 2 {
		t.Fatalf("a later refusal was reported %d times in all, want twice", got)
	}
}

//go:build unix

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/state"
)

// status tells a held-back nudge from a sent one: "last sent" is the last
// real send, and a line says when a nudge was last held back. The panel's
// hold says the nudges were held back, and how many.
func TestHeldNudgesAreToldFromSentOnes(t *testing.T) {
	f := newFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC)
	held := time.Date(2030, 1, 1, 11, 0, 0, 0, time.UTC)
	if err := state.Save(cfg.Loop.StateDir, state.File{CheckedAt: held, LastWakeAt: held, LastSentAt: sent, LastHeldNudgeAt: held, Nudges: 3, HeldNudges: 2}); err != nil {
		t.Fatal(err)
	}
	_, out, _ := f.run("status")
	if !strings.Contains(out, "last sent: "+sent.Format(time.RFC3339)) || !strings.Contains(out, "last nudge held back: "+held.Format(time.RFC3339)+" (the turn looked hung; 2 of 3 nudges held back, not sent)") {
		t.Fatalf("%s", out)
	}
	st := &statusOut{}
	st.State.Nudges, st.State.HeldNudges = 3, 3
	n := dockHoldNeed(st, &dockHold{Cause: "stall_limit_held"}, false)
	if n.Reason != "No progress after 3 nudges, all held back because the turn looked hung." {
		t.Fatalf("%+v", n)
	}
	st.State.HeldNudges = 1
	if n = dockHoldNeed(st, &dockHold{Cause: incident.Cause("no progress after 3 nudges (1 held back, not sent, because the turn looked hung)")}, false); n.Reason != "No progress after 3 nudges, 1 of them held back because the turn looked hung." {
		t.Fatalf("%+v", n)
	}
}

// A hold's fixed advice must not contradict its note: with a note the advice
// points at the note first, and a stated cause keeps its own advice.
func TestDockHoldAdviceFollowsNote(t *testing.T) {
	noted := &statusOut{}
	noted.Control.Note = "waiting for example-team to free a slot"
	for _, c := range []struct {
		name  string
		st    *statusOut
		cause string
		want  string
	}{
		{"no note", &statusOut{}, "", dockHoldAdvice},
		{"note, shown", noted, "", dockHoldAdviceNote},
		{"cause keeps its advice", noted, "input", "Answer it in the conversation, then resume the loop."},
	} {
		for _, ids := range []bool{true, false} {
			if n := dockHoldNeed(c.st, &dockHold{Cause: c.cause}, ids); n.Action != c.want {
				t.Errorf("%s (identifiers %v): %q", c.name, ids, n.Action)
			}
		}
	}
}

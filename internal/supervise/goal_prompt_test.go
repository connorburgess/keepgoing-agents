package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestGoalPromptRevisionsAcrossTicksAndPrivateHistory(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptGoal = true
	p := w.primary(decide.Observation{State: decide.Idle})
	spec := control.GoalSpec{Title: "fictional-private-old-title", ObjectiveRefs: []string{"fictional-private-objective"}, AcceptanceRefs: []string{"acceptance"}}
	g, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0)
	first, err := state.Load(w.dir)
	if err != nil || first.LastPromptSend == nil || first.LastPromptSend.GoalID != g.ID || first.LastPromptSend.GoalRevision != 1 || first.LastPromptSend.Outcome != "adapter_returned" {
		t.Fatalf("first recorded send: %+v %v", first.LastPromptSend, err)
	}
	spec.Title = "fictional-private-new-title"
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(time.Minute))
	second, err := state.Load(w.dir)
	if err != nil || second.LastPromptSend == nil || second.LastPromptSend.GoalID != g.ID || second.LastPromptSend.GoalRevision != 2 || !second.LastPromptSend.CheckAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("second recorded send: %+v %v", second.LastPromptSend, err)
	}
	if len(p.sent) != 2 || !strings.Contains(p.sent[0], `"revision": 1`) || !strings.Contains(p.sent[1], `"revision": 2`) || strings.Contains(p.sent[1], "old-title") {
		t.Fatalf("sent: %v", p.sent)
	}
	for _, name := range []string{"runs.jsonl", "state.json", "incidents.json"} {
		b, err := os.ReadFile(filepath.Join(w.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "fictional-private") {
			t.Fatalf("private context in %s", name)
		}
	}
	b, _ := json.Marshal(w.reports)
	if strings.Contains(string(b), "fictional-private") {
		t.Fatal("private context in reports")
	}
}

func TestMissingPromptGoalDoesNotSendOrSpendAndHoldsStillWin(t *testing.T) {
	for _, hold := range []string{"none", "paused", "completed", "stop", "disabled"} {
		t.Run(hold, func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.PromptGoal = true
			w.cfg.Loop.Limits.MaxSends = 3
			p := w.primary(decide.Observation{State: decide.Idle})
			if hold == "stop" {
				p.obs.State = decide.Active
			}
			if _, err := control.Update(w.dir, func(f *control.File) {
				switch hold {
				case "paused":
					f.Paused = true
				case "completed":
					f.Completed = true
				case "stop":
					f.Stop = true
				case "disabled":
					f.Enabled = false
				}
			}); err != nil {
				t.Fatal(err)
			}
			_, err := Tick(context.Background(), w.cfg, w.opts(t0))
			if (hold == "none") != (err != nil) {
				t.Fatalf("hold %s: %v", hold, err)
			}
			if len(p.sent) != 0 {
				t.Fatal("sent despite absent goal")
			}
			st, err := state.Load(w.dir)
			if err != nil || st.LastPromptSend != nil {
				t.Fatalf("invented send evidence: %+v %v", st.LastPromptSend, err)
			}
			if _, err := os.Stat(filepath.Join(w.dir, "sends.json")); !os.IsNotExist(err) {
				t.Fatalf("reserved allowance: %v", err)
			}
			if hold == "stop" && p.stopped != 1 {
				t.Fatal("goal blocked stop")
			}
		})
	}
}

func TestPromptSendFailureAndNonSendingTicks(t *testing.T) {
	for _, sendErr := range []error{errors.New("fictional-private-send-error"), adapter.ErrUnsupported{Op: "send"}} {
		t.Run(sendErr.Error(), func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.PromptGoal = true
			g, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", control.GoalSpec{Title: "private-title", ObjectiveRefs: []string{"private-objective"}, AcceptanceRefs: []string{"private-acceptance"}}, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
			p.sendErr = sendErr
			Tick(context.Background(), w.cfg, w.opts(t0))
			st, err := state.Load(w.dir)
			if err != nil || st.LastPromptSend == nil || st.LastPromptSend.GoalID != g.ID || st.LastPromptSend.GoalRevision != 1 || st.LastPromptSend.Action != "reprompt" || st.LastPromptSend.Outcome != "adapter_error" {
				t.Fatalf("failed-send evidence: %+v %v", st.LastPromptSend, err)
			}
			before, _ := json.Marshal(st.LastPromptSend)
			if strings.Contains(string(before), "private-") {
				t.Fatal("private send data recorded")
			}
			for _, kind := range []string{"active", "dry_run", "paused"} {
				p.obs.State = decide.Active
				o := w.opts(t0.Add(time.Minute))
				if kind == "dry_run" {
					p.obs.State = decide.Idle
					o.DryRun = true
				}
				if kind == "paused" {
					if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = true }); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := Tick(context.Background(), w.cfg, o); err != nil {
					t.Fatal(err)
				}
				st, err = state.Load(w.dir)
				after, _ := json.Marshal(st.LastPromptSend)
				if err != nil || string(before) != string(after) || len(p.sent) != 1 {
					t.Fatalf("%s rewrote historical attempt: %s %v", kind, after, err)
				}
			}
		})
	}
}

func TestQueuedPromptRetainsHistoricalGoalRevision(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptGoal = true
	w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	spec := control.GoalSpec{Title: "old", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}
	g, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	p := w.primary(decide.Observation{State: decide.Active})
	for _, at := range []time.Time{t0, t0.Add(time.Hour)} {
		o := w.opts(at)
		o.Fingerprint = func([]string) (string, error) { return "stable", nil }
		if _, err := Tick(context.Background(), w.cfg, o); err != nil {
			t.Fatal(err)
		}
	}
	spec.Title = "new"
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(w.dir)
	if err != nil || st.LastPromptSend == nil || st.LastPromptSend.GoalRevision != 1 || st.LastPromptSend.Mode != "queue" || st.LastPromptSend.Action != "nudge" || len(p.sent) != 1 || !strings.Contains(p.sent[0], `"revision": 1`) {
		t.Fatalf("queue evidence: %+v, messages %v, %v", st.LastPromptSend, p.sent, err)
	}
}

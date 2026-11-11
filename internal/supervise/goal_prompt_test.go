package supervise

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
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
	spec.Title = "fictional-private-new-title"
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(time.Minute))
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
			if _, err := os.Stat(filepath.Join(w.dir, "sends.json")); !os.IsNotExist(err) {
				t.Fatalf("reserved allowance: %v", err)
			}
			if hold == "stop" && p.stopped != 1 {
				t.Fatal("goal blocked stop")
			}
		})
	}
}

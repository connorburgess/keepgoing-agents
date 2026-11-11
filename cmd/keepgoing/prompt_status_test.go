package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestPromptSendStatusComparisonAndCertainty(t *testing.T) {
	const id = "goal_0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, id, outcome, comparison string
		revision                      uint64
		goal                          *control.GoalSummary
	}{
		{"same", id, "adapter_returned", "same_revision", 1, &control.GoalSummary{ID: id, Revision: 1}},
		{"revised_after_queue", id, "adapter_returned", "different_revision", 1, &control.GoalSummary{ID: id, Revision: 2}},
		{"failed", id, "adapter_error", "same_revision", 1, &control.GoalSummary{ID: id, Revision: 1}},
		{"goal_unavailable", id, "adapter_returned", "unavailable", 1, nil},
		{"opt_out", "", "adapter_returned", "not_included", 0, &control.GoalSummary{ID: id, Revision: 1}},
		{"changed_identity", id, "adapter_returned", "different_goal", 1, &control.GoalSummary{ID: "goal_abcdef0123456789abcdef0123456789", Revision: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &state.PromptSend{Version: 1, CheckAt: time.Now(), Target: "primary", Action: "nudge", Mode: "queue", Outcome: tc.outcome, GoalID: tc.id, GoalRevision: tc.revision}
			r := describePromptSend(p, tc.goal)
			if r.Availability != "recorded" || r.Last != p || r.CurrentGoalComparison != tc.comparison || r.Delivery != "unverified" {
				t.Fatalf("%+v", r)
			}
			var out bytes.Buffer
			printPromptSend(&out, r)
			if !strings.Contains(out.String(), tc.outcome) || !strings.Contains(out.String(), "delivery and execution unverified") {
				t.Fatal(out.String())
			}
		})
	}
	for _, p := range []*state.PromptSend{nil, {Version: 2}, {Version: 1, GoalID: "private-prompt"}} {
		r := describePromptSend(p, nil)
		if r.Availability != "unavailable" || r.Last != nil {
			t.Fatalf("%+v", r)
		}
	}
}

func TestStatusReloadsPromptHistoryWithoutPrivateGoalText(t *testing.T) {
	f := newDoctorFixture(t)
	cfg, err := config.Load(f.config)
	if err != nil {
		t.Fatal(err)
	}
	spec := control.GoalSpec{Title: "PRIVATE-GOAL-TITLE", ObjectiveRefs: []string{"PRIVATE-OBJECTIVE"}, AcceptanceRefs: []string{"PRIVATE-ACCEPTANCE"}}
	g, err := control.WriteGoal(f.state, cfg.Loop.Name, "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	checkAt := time.Now().UTC().Add(-time.Hour)
	if err := state.Save(f.state, state.File{LastPromptSend: &state.PromptSend{Version: 1, CheckAt: checkAt, Target: "primary", Action: "nudge", Mode: "queue", Outcome: "adapter_returned", GoalID: g.ID, GoalRevision: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.WriteGoal(f.state, cfg.Loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(f.state, state.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{true, false} {
		var out bytes.Buffer
		if status(env{context.Background(), &out, &out}, cfg, time.Now(), asJSON, defaultStaleAfter) != exitOK {
			t.Fatal(out.String())
		}
		if strings.Contains(out.String(), "PRIVATE-") || !strings.Contains(out.String(), "different_revision") {
			t.Fatal(out.String())
		}
		if asJSON {
			var parsed statusOut
			if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.PromptSend.Last == nil || parsed.PromptSend.Last.GoalRevision != 1 || parsed.Goal.Revision != 2 || !parsed.PromptSend.Last.CheckAt.Equal(checkAt) || parsed.PromptSend.Delivery != "unverified" {
				t.Fatalf("%+v", parsed.PromptSend)
			}
		}
	}
	after, err := os.ReadFile(filepath.Join(f.state, state.FileName))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("status changed prompt history", err)
	}
}

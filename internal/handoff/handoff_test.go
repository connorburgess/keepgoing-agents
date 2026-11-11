package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestGeneratedBrief(t *testing.T) {
	loop := config.Loop{Name: "example-loop", Prompt: "Ship the fictional feature.", Progress: config.Progress{Repositories: []string{"/fictional/example-app"}}}
	got, err := Brief(loop, Facts{Loop: "example-loop", At: time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC), From: "primary", To: "codex-main", Reason: "rate_limit"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"example-loop" loop`, "primary stopped with a rate_limit error at 2026-11-09 03:00 UTC", "(codex-main)", "- /fictional/example-app", "Ship the fictional feature."} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q:\n%s", want, got)
		}
	}
}

func TestHandoffFileWins(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "HANDOFF.md")
	loop := config.Loop{Prompt: "goal", HandoffFile: p}
	if got, _ := Brief(loop, Facts{}); !strings.Contains(got, "goal") {
		t.Error("missing handoff file should fall back to the generated brief")
	}
	os.WriteFile(p, []byte("Custom brief for the fictional loop.\n"), 0o600)
	if got, _ := Brief(loop, Facts{}); got != "Custom brief for the fictional loop." {
		t.Errorf("got %q", got)
	}
}

func TestSwitchBackBrief(t *testing.T) {
	got, err := Brief(config.Loop{Name: "example-loop", Prompt: "Ship it."}, Facts{Loop: "example-loop", At: time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC), From: "codex-main", To: "primary", SwitchBack: true})
	if err != nil || !strings.Contains(got, "limit has reset") || !strings.Contains(got, "from the fallback that covered for you (codex-main)") || strings.Contains(got, "error at") {
		t.Errorf("%v\n%s", err, got)
	}
}

func TestCurrentGoalIncludedInGeneratedAndCustomBrief(t *testing.T) {
	loop := config.Loop{Name: "example-loop", StateDir: t.TempDir(), Prompt: "Read authorized sources", PromptGoal: true}
	spec := control.GoalSpec{Title: "Fictional current goal", ObjectiveRefs: []string{"missing-objective"}, AcceptanceRefs: []string{"missing-acceptance"}}
	if _, err := control.WriteGoal(loop.StateDir, loop.Name, "", spec, "", 0); err != nil {
		t.Fatal(err)
	}
	for _, custom := range []bool{false, true} {
		if custom {
			loop.HandoffFile = filepath.Join(loop.StateDir, "handoff.md")
			if err := os.WriteFile(loop.HandoffFile, []byte("Custom handoff scaffold"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, back := range []bool{false, true} {
			message, err := ComposeBrief(loop, Facts{SwitchBack: back})
			text := message.Text
			if message.GoalID == "" || message.GoalRevision != 1 {
				t.Fatalf("missing goal provenance: %+v", message)
			}
			if err != nil || strings.Count(text, `"revision": 1`) != 1 || !strings.Contains(text, spec.Title) {
				t.Fatalf("custom=%v switchBack=%v: %q %v", custom, back, text, err)
			}
		}
	}
	if err := os.Remove(filepath.Join(loop.StateDir, control.GoalName)); err != nil {
		t.Fatal(err)
	}
	if text, err := Brief(loop, Facts{}); err == nil || text != "" {
		t.Fatalf("missing goal: %q %v", text, err)
	}
}

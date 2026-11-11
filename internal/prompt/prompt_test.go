package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestCurrentGoalIsOptInAndReread(t *testing.T) {
	loop := config.Loop{Name: "example-loop", StateDir: t.TempDir(), Prompt: "Read the current authorized roadmap."}
	spec := control.GoalSpec{Title: "fictional-private-old-title", ObjectiveRefs: []string{"missing-objective.md"}, AcceptanceRefs: []string{"missing-acceptance.md"}}
	g, err := control.WriteGoal(loop.StateDir, loop.Name, "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if text, err := Wake(loop); err != nil || text != loop.Prompt {
		t.Fatalf("legacy: %q %v", text, err)
	}
	loop.PromptGoal = true
	first, err := Wake(loop)
	if err != nil || !strings.Contains(first, spec.Title) || !strings.Contains(first, `"revision": 1`) {
		t.Fatalf("first: %q %v", first, err)
	}
	spec.Title = "fictional-private-current-title"
	spec.NextAction = &control.GoalNextAction{Summary: "Inspect current evidence"}
	if _, err := control.WriteGoal(loop.StateDir, loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(loop.StateDir, control.GoalName))
	current, err := Wake(loop)
	if err != nil || !strings.Contains(current, spec.Title) || !strings.Contains(current, `"revision": 2`) || !strings.Contains(current, g.ID) || strings.Contains(current, "fictional-private-old-title") {
		t.Fatalf("current: %q %v", current, err)
	}
	after, _ := os.ReadFile(filepath.Join(loop.StateDir, control.GoalName))
	if string(before) != string(after) {
		t.Fatal("render changed goal state")
	}
	// Missing reference files succeed: references are never dereferenced.
	if !strings.Contains(current, "missing-objective.md") {
		t.Fatal("reference omitted")
	}
}

func TestUnavailableGoalRefusesWithoutPrivateErrors(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "foreign", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			loop := config.Loop{Name: "example-loop", StateDir: t.TempDir(), Prompt: "scaffold", PromptGoal: true}
			path := filepath.Join(loop.StateDir, control.GoalName)
			switch kind {
			case "corrupt":
				if err := os.WriteFile(path, []byte("fictional-private-invalid-input"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				spec := control.GoalSpec{Title: "fictional-private-title", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}
				if _, err := control.WriteGoal(loop.StateDir, "other-loop", "", spec, "", 0); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Skip("symlink unavailable")
				}
			}
			text, err := Wake(loop)
			if text != "" || err == nil || strings.Contains(err.Error(), "fictional-private") || strings.Contains(err.Error(), loop.StateDir) {
				t.Fatalf("%q %v", text, err)
			}
			loop.PromptGoal = false
			if text, err := Wake(loop); err != nil || text != "scaffold" {
				t.Fatalf("opt out: %q %v", text, err)
			}
		})
	}
}

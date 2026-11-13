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

func TestWithChildErrorsIsFixedWording(t *testing.T) {
	m := Message{Text: "Continue the fictional loop.", GoalID: "goal_example", GoalRevision: 2}
	for _, n := range []int{0, -1} {
		if got := WithChildErrors(m, n); got != m {
			t.Fatalf("n=%d changed the message: %+v", n, got)
		}
	}
	got := WithChildErrors(m, 3)
	want := "Continue the fictional loop.\n\n[keepgoing:child-errors count=3]\n" +
		"keepgoing notice: 3 direct child thread(s) of this thread were in an error state at this check. " +
		"keepgoing has not retried, messaged or stopped them and has not read their content. " +
		"You own that work: decide whether to retry or replace it. " +
		"This notice is sent once when a child newly enters an error state.\n"
	if got.Text != want || got.GoalID != m.GoalID || got.GoalRevision != m.GoalRevision {
		t.Fatalf("got %q", got.Text)
	}
}

// The wake that errored children cause carries the notice alone: no task
// prompt and no goal, and it says other children are still running.
func TestChildErrorsOnlyIsFixedWordingWithoutATaskPrompt(t *testing.T) {
	got := ChildErrorsOnly(2, 3)
	want := "[keepgoing:child-errors count=2 running=3]\n" +
		"keepgoing notice: 2 direct child thread(s) of this thread were in an error state at this check, and 3 other direct child thread(s) are still running. " +
		"keepgoing has not retried, messaged or stopped any of them and has not read their content. " +
		"This is not an instruction to continue the task: the running children are still working. " +
		"You own the errored work: decide whether to retry or replace it. " +
		"This notice is sent once when a child newly enters an error state.\n"
	if got.Text != want || got.GoalID != "" || got.GoalRevision != 0 {
		t.Fatalf("got %+v", got)
	}
}

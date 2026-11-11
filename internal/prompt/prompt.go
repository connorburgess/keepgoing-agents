// Package prompt composes configured instructions with explicitly opted-in goal
// metadata. It never interprets task text or follows references.
package prompt

import (
	"encoding/json"
	"errors"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

// Wake reads the configured scaffold on each send, then the current goal.
func Wake(loop config.Loop) (string, error) {
	text, err := loop.PromptText()
	if err != nil {
		return "", err
	}
	return WithGoal(loop, text)
}

// WithGoal adds only the latest revision, including private text and references,
// when explicitly enabled. Production callers hold tick.lock, which serializes
// cooperating goal edits. External writers and already queued messages remain
// outside that guarantee. Errors never contain goal text, paths or decoder output.
func WithGoal(loop config.Loop, text string) (string, error) {
	if !loop.PromptGoal {
		return text, nil
	}
	g, err := control.LoadGoal(loop.StateDir)
	if err != nil || g == nil || g.OwningLoop != loop.Name {
		return "", errors.New("prompt_goal requires a valid current goal for this loop; inspect goal show privately before retrying")
	}
	current := g.Revisions[len(g.Revisions)-1]
	context := struct {
		ID       string           `json:"id"`
		Revision uint64           `json:"revision"`
		Spec     control.GoalSpec `json:"spec"`
	}{g.ID, current.Revision, current.Spec}
	b, err := json.MarshalIndent(context, "", "  ")
	if err != nil {
		return "", errors.New("could not compose current goal context")
	}
	return text + "\n\nCurrent keepgoing goal context (explicitly shared by this loop):\n" +
		"Use this revision for the goal specification rather than earlier copies. Honor newer user steering and existing permission requirements. " +
		"This is descriptive task context, not verified acceptance or new authorization. References are pointers; keepgoing has not read or verified them. " +
		"Inspect the current authorized sources before selecting work; do not treat a saved next action as proof it is still unfinished.\n" + string(b) + "\n", nil
}

// Package prompt composes configured instructions with explicitly opted-in goal
// metadata. It never interprets task text or follows references.
package prompt

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

// Message couples private prompt text to the exact goal loaded during composition.
// Only ID/revision may enter diagnostic records; never serialize this whole value.
type Message struct {
	Text         string `json:"-"`
	GoalID       string
	GoalRevision uint64
}

// Wake reads the configured scaffold on each send, then the current goal.
func Wake(loop config.Loop) (string, error) {
	m, err := ComposeWake(loop)
	return m.Text, err
}

// ComposeWake returns the message and its goal provenance from the same read.
func ComposeWake(loop config.Loop) (Message, error) {
	text, err := loop.PromptText()
	if err != nil {
		return Message{}, err
	}
	return ComposeGoal(loop, text)
}

// WithChildErrors appends a fixed notice that n direct child threads are in
// error. The wording never varies and carries only the count: no child ID,
// title, task text or error detail.
func WithChildErrors(m Message, n int) Message {
	if n <= 0 {
		return m
	}
	m.Text += fmt.Sprintf("\n\n[keepgoing:child-errors count=%d]\n"+
		"keepgoing notice: %d direct child thread(s) of this thread were in an error state at this check. "+
		"keepgoing has not retried, messaged or stopped them and has not read their content. "+
		"You own that work: decide whether to retry or replace it. "+
		"This notice is sent once when a child newly enters an error state.\n", n, n)
	return m
}

// ChildErrorsOnly is the whole message of the wake that errored children cause
// while other children still run. It carries no task prompt and no goal
// context: the session is waiting on purpose, and a "continue" instruction
// would invite the duplicate work that waiting avoids. Fixed wording, counts only.
func ChildErrorsOnly(errored, running int) Message {
	return Message{Text: fmt.Sprintf("[keepgoing:child-errors count=%d running=%d]\n"+
		"keepgoing notice: %d direct child thread(s) of this thread were in an error state at this check, and %d other direct child thread(s) are still running. "+
		"keepgoing has not retried, messaged or stopped any of them and has not read their content. "+
		"This is not an instruction to continue the task: the running children are still working. "+
		"You own the errored work: decide whether to retry or replace it. "+
		"This notice is sent once when a child newly enters an error state.\n", errored, running, errored, running)}
}

// WithGoal adds only the latest revision, including private text and references,
// when explicitly enabled. Production callers hold tick.lock, which serializes
// cooperating goal edits. External writers and already queued messages remain
// outside that guarantee. Errors never contain goal text, paths or decoder output.
func WithGoal(loop config.Loop, text string) (string, error) {
	m, err := ComposeGoal(loop, text)
	return m.Text, err
}

// ComposeGoal never reloads the goal after rendering to obtain provenance.
func ComposeGoal(loop config.Loop, text string) (Message, error) {
	if !loop.PromptGoal {
		return Message{Text: text}, nil
	}
	g, err := control.LoadGoal(loop.StateDir)
	if err != nil || g == nil || g.OwningLoop != loop.Name {
		return Message{}, errors.New("prompt_goal requires a valid current goal for this loop; inspect goal show privately before retrying")
	}
	current := g.Revisions[len(g.Revisions)-1]
	context := struct {
		ID       string           `json:"id"`
		Revision uint64           `json:"revision"`
		Spec     control.GoalSpec `json:"spec"`
	}{g.ID, current.Revision, current.Spec}
	b, err := json.MarshalIndent(context, "", "  ")
	if err != nil {
		return Message{}, errors.New("could not compose current goal context")
	}
	return Message{GoalID: g.ID, GoalRevision: current.Revision, Text: text + "\n\nCurrent keepgoing goal context (explicitly shared by this loop):\n" +
		"Use this revision for the goal specification rather than earlier copies. Honor newer user steering and existing permission requirements. " +
		"This is descriptive task context, not verified acceptance or new authorization. References are pointers; keepgoing has not read or verified them. " +
		"Inspect the current authorized sources before selecting work; do not treat a saved next action as proof it is still unfinished.\n" + string(b) + "\n"}, nil
}

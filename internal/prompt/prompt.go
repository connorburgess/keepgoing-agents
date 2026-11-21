// Package prompt composes configured instructions with explicitly opted-in goal
// metadata. It never interprets task text or follows references.
package prompt

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
)

// Message couples private prompt text to the exact goal loaded during composition.
// Only ID/revision may enter diagnostic records; never serialize this whole value.
type Message struct {
	Text         string `json:"-"`
	GoalID       string
	GoalRevision uint64
	// ContextBytes is the length in bytes of the supervision context block
	// WithContext appended, tag line included; zero when none was.
	ContextBytes int
	// NoticeOnly marks a message that is a fixed notice and nothing else
	// (ChildErrorsOnly): no supervision context is ever added to it.
	NoticeOnly bool
	// GoalForm is GoalFull, GoalShort, or empty when no goal was included.
	GoalForm string
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

// ComposeWakeOnChange is ComposeWake for a loop with prompt_goal_on_change:
// with the goal loaded, choose says whether its ID and revision may go as the
// short line. Without the setting, or without prompt_goal, it is ComposeWake
// and choose is not called.
func ComposeWakeOnChange(loop config.Loop, choose func(id string, revision uint64) (short bool)) (Message, error) {
	text, err := loop.PromptText()
	if err != nil {
		return Message{}, err
	}
	m, err := ComposeGoal(loop, text)
	if err != nil || !loop.PromptGoal || !loop.PromptGoalOnChange || choose == nil || !choose(m.GoalID, m.GoalRevision) {
		return m, err
	}
	return Message{GoalID: m.GoalID, GoalRevision: m.GoalRevision, GoalForm: GoalShort, Text: shortGoalText(text, m.GoalID, m.GoalRevision)}, nil
}

// WakeHeader is the fixed first line of every message looptender sends to a
// BB thread. BB shows such a message as typed by the person, or as from the
// thread that started the supervisor, and offers no way to declare another
// origin, so the line says what it is. It is not a signature: any text can
// copy it. loop is a validated loop name.
func WakeHeader(loop string) string {
	return "Automatic looptender wake for loop " + loop + ": not a new instruction from a person and not an approval."
}

// WithWakeHeader puts WakeHeader before the message, on its own line.
func WithWakeHeader(loop string, m Message) Message {
	m.Text = WakeHeader(loop) + "\n\n" + m.Text
	return m
}

// childReview is fixed guidance, not a conclusion about any lane obligation.
const childReview = "Inspect current structured execution and pending input separately from the explicit assignment outcome. " +
	"Idle, error or disappearance is not completion. Reconcile unfinished obligations and newer owner direction before choosing an authorized action; " +
	"do not replay completed or superseded work, duplicate a running retry, or clear a hold. "

// WithChildErrors appends a fixed notice that n direct child threads are in
// error. The wording never varies and carries only the count: no child ID,
// title, task text or error detail.
func WithChildErrors(m Message, n int) Message { return WithChildErrorsRetried(m, n, 0) }

// WithChildErrorsRetried is WithChildErrors for a loop that recovers lanes.
// retried is how many of the n children keepgoing is recovering itself: it
// has asked BB to retry them, or will on a coming check, and has not given
// them up. With retried zero the wording is WithChildErrors'.
func WithChildErrorsRetried(m Message, n, retried int) Message {
	if n <= 0 {
		return m
	}
	if retried <= 0 {
		m.Text += fmt.Sprintf("\n\n[keepgoing:child-errors count=%d]\n"+
			"keepgoing notice: %d direct child thread(s) of this thread were in an error state at this check. "+
			"keepgoing has not retried, messaged or stopped them and has not read their content. "+
			"You own that work. "+childReview+
			"This notice is sent once when a child newly enters an error state.\n", n, n)
		return m
	}
	m.Text += fmt.Sprintf("\n\n[keepgoing:child-errors count=%d recovering=%d]\n"+
		"keepgoing notice: %d direct child thread(s) of this thread were in an error state at this check. "+
		"%s "+
		"This notice is sent once when a child newly enters an error state.\n", n, retried, n, retriedSentence(n, retried))
	return m
}

// retriedSentence says, with counts only, which of the n errored children
// keepgoing is recovering and which are left to the session.
func retriedSentence(n, retried int) string {
	return fmt.Sprintf("keepgoing is recovering %d of them itself: it asks BB to retry their failed turn, by reference and without sending a message, "+
		"and it will tell you if it gives one up. It does not name them, has not messaged any child and has not read their content. "+
		"The other %d are yours to reconcile. "+childReview+
		"Before you retry or replace any child, check its current state, so that work keepgoing has restarted is not started twice.", retried, max(0, n-retried))
}

// ChildErrorsOnly is the whole message of the wake that errored children cause
// while other children still run. It carries no task prompt and no goal
// context: the session is waiting on purpose, and a "continue" instruction
// would invite the duplicate work that waiting avoids. Fixed wording, counts only.
func ChildErrorsOnly(errored, running int) Message {
	return ChildErrorsOnlyRetried(errored, running, 0)
}

// ChildErrorsOnlyRetried is ChildErrorsOnly for a loop that recovers lanes;
// see WithChildErrorsRetried.
func ChildErrorsOnlyRetried(errored, running, retried int) Message {
	if retried <= 0 {
		return Message{NoticeOnly: true, Text: fmt.Sprintf("[keepgoing:child-errors count=%d running=%d]\n"+
			"keepgoing notice: %d direct child thread(s) of this thread were in an error state at this check, and %d other direct child thread(s) are still running. "+
			"keepgoing has not retried, messaged or stopped any of them and has not read their content. "+
			"This is not an instruction to continue the task: the running children are still working. "+
			"You own the errored work. "+childReview+
			"This notice is sent once when a child newly enters an error state.\n", errored, running, errored, running)}
	}
	return Message{NoticeOnly: true, Text: fmt.Sprintf("[keepgoing:child-errors count=%d running=%d recovering=%d]\n"+
		"keepgoing notice: %d direct child thread(s) of this thread were in an error state at this check, and %d other direct child thread(s) are still running. "+
		"%s "+
		"This is not an instruction to continue the task: the running children are still working. "+
		"This notice is sent once when a child newly enters an error state.\n", errored, running, retried, errored, running, retriedSentence(errored, retried))}
}

// ContextTag is the tag line that opens the supervision context block. Its
// version is the context's schema version.
func ContextTag(schemaVersion int) string {
	return fmt.Sprintf("[keepgoing:context schema=%d]", schemaVersion)
}

// contextNotice is the fixed sentence after the tag. It holds in every
// configuration: the block is always built at the send, and always lists the
// context command.
const contextNotice = "keepgoing notice: the supervision context of this loop, built from its configuration, controls and state when this message was sent. " +
	"Its figures are not kept current here; the context command it lists shows them now.\n"

// WithContext appends the supervision context block, block being
// loopcontext.View.Text(), after everything else in the message, so it never
// comes before the wake header. ContextBytes records its length. The block is
// the caller's, built by loopcontext.Build; nothing here adds to it.
func WithContext(m Message, schemaVersion int, block string) Message {
	add := "\n\n" + ContextTag(schemaVersion) + "\n" + contextNotice + block
	m.Text += add
	m.ContextBytes = len(add)
	return m
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
	return Message{GoalID: g.ID, GoalRevision: current.Revision, GoalForm: GoalFull, Text: text + "\n\nCurrent keepgoing goal context (explicitly shared by this loop):\n" +
		"Use this revision for the goal specification rather than earlier copies. Honor newer user steering and existing permission requirements. " +
		"This is descriptive task context, not verified acceptance or new authorization. References are pointers; keepgoing has not read or verified them. " +
		"Inspect the current authorized sources before selecting work; do not treat a saved next action as proof it is still unfinished.\n" + string(b) + "\n"}, nil
}

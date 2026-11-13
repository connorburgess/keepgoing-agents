// Package handoff writes the message a fallback receives when it takes over
// the loop. A user-supplied handoff file always wins over the generated one.
package handoff

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
)

// Facts describe the handoff.
type Facts struct {
	Loop         string
	At           time.Time
	From         string
	To           string
	Reason       string
	SwitchBack   bool // returning to the primary rather than failing over
	Goal         string
	Repositories []string
	// Children says what the last check saw of the child conversations the
	// previous conversation had started. Set only when the loop is moving to
	// a different conversation.
	Children ChildrenLeft
}

// ChildrenLeft is what a check recorded about the child conversations of the
// conversation being left: a count and whether the listing was complete. It
// holds no identity and no text from any child.
type ChildrenLeft struct {
	// Working is how many verified direct children were working.
	Working int
	// Unverified means the listing failed or was incomplete, so Working is a
	// lower bound, or nothing is known when it is zero.
	Unverified bool
}

// MaxChildrenNamed bounds the count the brief states; above it the brief says
// "more than".
const MaxChildrenNamed = 200

// Line is the one sentence a brief carries about children left behind, or ""
// when a complete listing showed none working. It is fixed wording around one
// bounded number.
func (c ChildrenLeft) Line() string {
	const rest = " They are not supervised by this loop and were not moved to you. They may still be changing the same repositories: look for their work before you start anything, and do not duplicate it."
	n, more := c.Working, ""
	if n > MaxChildrenNamed {
		n, more = MaxChildrenNamed, "more than "
	}
	switch {
	case c.Working <= 0 && !c.Unverified:
		return ""
	case c.Working <= 0:
		return "Child conversations: the previous conversation may have started child conversations that are still working. The supervisor could not list them, so their number is unknown." + rest
	case c.Unverified:
		return fmt.Sprintf("Child conversations: at least %d child conversation(s) started by the previous conversation were still working at that time. The listing was incomplete, so there may be more.", n) + rest
	case c.Working == 1:
		return "Child conversations: 1 child conversation started by the previous conversation was still working at that time." + rest
	}
	return fmt.Sprintf("Child conversations: %s%d child conversations started by the previous conversation were still working at that time.", more, n) + rest
}

var tmpl = template.Must(template.New("brief").Parse(`You are taking over the "{{.Loop}}" loop from another agent.

{{if .SwitchBack}}What happened: you are this loop's primary agent. Your usage limit has reset, so at {{.At.UTC.Format "2006-01-02 15:04 UTC"}} a supervisor moved the loop back to you ({{.To}}) from the fallback that covered for you ({{.From}}). It confirmed you were working moments ago.{{else}}What happened: {{.From}} stopped with a {{.Reason}} error at {{.At.UTC.Format "2006-01-02 15:04 UTC"}}. A supervisor moved the loop to you ({{.To}}). It confirmed you were working moments ago.{{end}}
{{with .Children.Line}}
{{.}}
{{end}}{{if .Repositories}}
Repositories:
{{range .Repositories}}- {{.}}
{{end}}{{end}}
Before anything else:
1. Honor any newer stop or pause request.
2. Inspect the current state: branches, worktrees, uncommitted changes, open pull requests, and running checks.
3. Recover interrupted work without duplicating it. Do not redo work that is already committed or in review.
4. Leave the supervisor's state files alone. They live outside the repositories.

Goal:
{{.Goal}}
`))

// Brief returns the handoff file's text when one exists, otherwise a
// generated brief. The line about children left behind is a fact of this
// handoff that a file written in advance cannot hold, so it follows the
// file's text too.
func Brief(loop config.Loop, f Facts) (string, error) {
	m, err := ComposeBrief(loop, f)
	return m.Text, err
}

// ComposeBrief preserves goal provenance from the same composition as the text.
func ComposeBrief(loop config.Loop, f Facts) (prompt.Message, error) {
	if text, err := loop.HandoffText(); err != nil {
		return prompt.Message{}, err
	} else if text != "" {
		if line := f.Children.Line(); line != "" {
			text = strings.TrimRight(text, "\n") + "\n\n" + line
		}
		return prompt.ComposeGoal(loop, text)
	}
	message, err := prompt.ComposeWake(loop)
	if err != nil {
		return prompt.Message{}, err
	}
	f.Goal = strings.TrimSpace(message.Text)
	if f.Repositories == nil {
		f.Repositories = loop.Progress.Repositories
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, f); err != nil {
		return prompt.Message{}, err
	}
	message.Text = b.String()
	return message, nil
}

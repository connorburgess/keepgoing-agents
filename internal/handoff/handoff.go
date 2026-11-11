// Package handoff writes the message a fallback receives when it takes over
// the loop. A user-supplied handoff file always wins over the generated one.
package handoff

import (
	"bytes"
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
}

var tmpl = template.Must(template.New("brief").Parse(`You are taking over the "{{.Loop}}" loop from another agent.

{{if .SwitchBack}}What happened: you are this loop's primary agent. Your usage limit has reset, so at {{.At.UTC.Format "2006-01-02 15:04 UTC"}} a supervisor moved the loop back to you ({{.To}}) from the fallback that covered for you ({{.From}}). It confirmed you were working moments ago.{{else}}What happened: {{.From}} stopped with a {{.Reason}} error at {{.At.UTC.Format "2006-01-02 15:04 UTC"}}. A supervisor moved the loop to you ({{.To}}). It confirmed you were working moments ago.{{end}}
{{if .Repositories}}
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
// generated brief.
func Brief(loop config.Loop, f Facts) (string, error) {
	if text, err := loop.HandoffText(); err != nil {
		return "", err
	} else if text != "" {
		return prompt.WithGoal(loop, text)
	}
	goal, err := prompt.Wake(loop)
	if err != nil {
		return "", err
	}
	f.Goal = strings.TrimSpace(goal)
	if f.Repositories == nil {
		f.Repositories = loop.Progress.Repositories
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, f); err != nil {
		return "", err
	}
	return b.String(), nil
}

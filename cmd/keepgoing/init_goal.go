package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

// A setup goal points at one private task snapshot. The goal store remains the
// sole owner of identity and revisions; the wake scaffold contains only policy.
func setupGoalSpec(o initOptions) *control.GoalSpec {
	if !o.withGoal {
		return nil
	}
	taskPath := filepath.Join(o.directory, "task.md")
	return &control.GoalSpec{Title: o.name, ObjectiveRefs: []string{taskPath}, AcceptanceRefs: []string{taskPath}}
}

func validateSetupGoal(o initOptions) error {
	if len(o.task) > initTaskLimit || !utf8.ValidString(o.task) {
		return errors.New("goal task snapshot must be UTF-8 text of at most 1 MiB")
	}
	b, err := json.Marshal(setupGoalSpec(o))
	if err != nil {
		return errors.New("invalid setup goal")
	}
	if _, err := control.ParseGoalSpec(b); err != nil {
		return err
	}
	return nil
}

const initGoalBindingNotice = "Task binding: snapshot imported once into private task.md, referenced by a recorded goal titled with the loop name. The agent must read that file for the task and completion criteria. The original input is not tracked. Future prompts share the current goal revision and references; prompt.md contains stable wake policy. Goal metadata is not verified acceptance; editing a referenced file does not revise the goal."

func initBindingNotice(o initOptions) string {
	if o.withGoal {
		return initGoalBindingNotice
	}
	return initTaskBindingNotice
}

func initGoalPrompt(cfgPath string) string {
	return initPrompt("Read the objective and acceptance references in the current goal context appended to this message before choosing work. If they are unavailable or the completion criteria are unclear, request clarification; do not invent requirements. Use the current revision rather than earlier task copies. Goal metadata does not grant new permissions or independently verify completion.", cfgPath)
}

type initGoalReport struct {
	PromptContext       bool                 `json:"promptContext"`
	TitleSource         string               `json:"titleSource"`
	SpecificationSource string               `json:"specificationSource"`
	Record              *control.GoalSummary `json:"record,omitempty"`
}

// Bound private goal task imports before allocating their contents. Existing
// snapshot-only setup keeps its original input contract.
func readSetupGoalTask(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("goal task input must be a readable regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read goal task input")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("goal task input must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, initTaskLimit+1))
	if err != nil {
		return nil, errors.New("cannot read goal task input")
	}
	if len(b) > initTaskLimit {
		return nil, errors.New("goal task input exceeds 1 MiB")
	}
	return b, nil
}

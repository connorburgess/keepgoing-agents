package state

import (
	"regexp"
	"time"
)

// PromptSend describes the latest returned automatic send call saved by a tick.
// It is diagnostic only: absence cannot prove that nothing was sent, and success
// cannot prove delivery, consumption, execution or acceptance by an agent.
type PromptSend struct {
	Version      int       `json:"version"`
	CheckAt      time.Time `json:"checkAt"`
	Target       string    `json:"target"` // alias as configured at that check
	Action       string    `json:"action"`
	Mode         string    `json:"mode"`
	Outcome      string    `json:"outcome"`
	GoalID       string    `json:"goalId,omitempty"`
	GoalRevision uint64    `json:"goalRevision,omitempty"`
}

var promptGoalID = regexp.MustCompile(`^goal_[0-9a-f]{32}$`)

// Valid withholds unsupported/malformed evidence without changing loop policy.
func (p PromptSend) Valid() bool {
	if p.Version != 1 || p.CheckAt.IsZero() || p.Target == "" || len(p.Target) > 128 {
		return false
	}
	switch p.Action {
	case "wake", "nudge", "reprompt", "handoff", "switch_back":
	default:
		return false
	}
	if p.Mode != "auto" && p.Mode != "queue" {
		return false
	}
	if p.Outcome != "adapter_returned" && p.Outcome != "adapter_error" {
		return false
	}
	if p.GoalID == "" {
		return p.GoalRevision == 0
	}
	return promptGoalID.MatchString(p.GoalID) && p.GoalRevision > 0 && p.GoalRevision <= 128
}

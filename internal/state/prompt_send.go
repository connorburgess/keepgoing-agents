package state

import (
	"encoding/json"
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
	// ContextBytes is the length of the supervision context block the send
	// carried (loop.prompt_context), zero when it carried none.
	ContextBytes int `json:"contextBytes,omitempty"`
	// Bytes is the length of the whole text handed to the adapter, wake
	// header and supervision context block included. Diagnostic only: bytes
	// are measured, tokens are not, and no rule reads it.
	Bytes int `json:"bytes,omitempty"`
	// GoalForm is "full" when the send carried the whole goal, "short" when
	// it carried only the line naming an unchanged goal, and empty when it
	// carried no goal.
	GoalForm string `json:"goalForm,omitempty"`
}

// MaxPromptBytes bounds a recorded send size; a larger record is malformed.
const MaxPromptBytes = 1 << 24

var promptGoalID = regexp.MustCompile(`^goal_[0-9a-f]{32}$`)

// UnmarshalJSON keeps malformed diagnostic metadata from preventing ordinary
// control/decision state from loading. Load withholds this zero/invalid record.
func (p *PromptSend) UnmarshalJSON(b []byte) error {
	type plain PromptSend
	var candidate plain
	if json.Unmarshal(b, &candidate) != nil || !PromptSend(candidate).Valid() {
		*p = PromptSend{}
		return nil
	}
	*p = PromptSend(candidate)
	return nil
}

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
	if p.Outcome != "adapter_returned" && p.Outcome != "adapter_error" && p.Outcome != "not_started" {
		return false
	}
	if p.ContextBytes < 0 {
		return false
	}
	if p.Bytes < 0 || p.Bytes > MaxPromptBytes {
		return false
	}
	switch p.GoalForm {
	case "":
	case "full", "short":
		if p.GoalID == "" {
			return false
		}
	default:
		return false
	}
	if p.GoalID == "" {
		return p.GoalRevision == 0
	}
	return promptGoalID.MatchString(p.GoalID) && p.GoalRevision > 0 && p.GoalRevision <= 128
}

// GoalCopy records the latest full goal copy that an adapter call returned
// from, so a later send can name an unchanged goal instead of repeating it.
// It is cleared when a full send fails, and a missing or malformed record
// means the next send carries the goal in full.
type GoalCopy struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	Target  string    `json:"target"`
	// Conversation is a short hash of the conversation the copy went to, or
	// empty when the adapter reports none or the send started a new one.
	Conversation string `json:"conversation,omitempty"`
	GoalID       string `json:"goalId"`
	GoalRevision uint64 `json:"goalRevision"`
	// ShortSends counts the short sends that returned since the copy.
	ShortSends int `json:"shortSends,omitempty"`
}

var goalCopyConversation = regexp.MustCompile(`^[0-9a-f]{16}$`)

// UnmarshalJSON turns a malformed record into an invalid one, which Load
// drops, so it never prevents the rest of the state from loading.
func (g *GoalCopy) UnmarshalJSON(b []byte) error {
	type plain GoalCopy
	var candidate plain
	if json.Unmarshal(b, &candidate) != nil || !GoalCopy(candidate).Valid() {
		*g = GoalCopy{}
		return nil
	}
	*g = GoalCopy(candidate)
	return nil
}

// Valid reports whether the record is one this version wrote.
func (g GoalCopy) Valid() bool {
	return g.Version == 1 && !g.At.IsZero() && g.Target != "" && len(g.Target) <= 128 &&
		(g.Conversation == "" || goalCopyConversation.MatchString(g.Conversation)) &&
		promptGoalID.MatchString(g.GoalID) && g.GoalRevision > 0 && g.GoalRevision <= 128 &&
		g.ShortSends >= 0 && g.ShortSends <= 1<<20
}

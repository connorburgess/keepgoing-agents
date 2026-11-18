package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/looptender/looptender/internal/state"
)

// Goal forms recorded with a send.
const (
	GoalFull  = "full"
	GoalShort = "short"
)

// After this many short sends since the last full copy, or this long, the
// goal goes in full again. A provider may compact a conversation and drop
// the copy without looptender being able to see it, so the gap is bounded.
// Twelve sends is about four hours of wakes at a twenty minute spacing,
// which is where the two bounds meet; a full copy in every twelve keeps
// most of the byte saving.
const (
	GoalShortSendsMax = 12
	GoalCopyMaxAge    = 4 * time.Hour
)

// GoalFacts is what the full-or-short rule reads. Conversation is the
// adapter's conversation key, already hashed; ConversationKnown is false for
// an adapter that reports none, and an empty known key means the next turn
// starts a new conversation.
type GoalFacts struct {
	Last              *state.GoalCopy
	Target            string
	Conversation      string
	ConversationKnown bool
	GoalID            string
	GoalRevision      uint64
	Now               time.Time
}

// Reasons the rule returns.
const (
	GoalReasonNoRecord        = "no_record"
	GoalReasonTarget          = "target_changed"
	GoalReasonRevision        = "goal_changed"
	GoalReasonNewConversation = "new_conversation"
	GoalReasonConversation    = "conversation_changed"
	GoalReasonShortSends      = "short_sends"
	GoalReasonAge             = "age"
	GoalReasonUnchanged       = "unchanged"
)

// ChooseGoalForm is the pure rule that picks the full goal or the short line
// for a wake, nudge or re-prompt under prompt_goal_on_change. Anything it
// cannot confirm as unchanged sends the goal in full.
func ChooseGoalForm(f GoalFacts) (form, reason string) {
	l := f.Last
	switch {
	case l == nil || !l.Valid():
		return GoalFull, GoalReasonNoRecord
	case l.Target != f.Target:
		return GoalFull, GoalReasonTarget
	case l.GoalID != f.GoalID || l.GoalRevision != f.GoalRevision:
		return GoalFull, GoalReasonRevision
	case f.ConversationKnown && f.Conversation == "":
		return GoalFull, GoalReasonNewConversation
	case f.ConversationKnown && l.Conversation != "" && l.Conversation != f.Conversation:
		return GoalFull, GoalReasonConversation
	case l.ShortSends >= GoalShortSendsMax:
		return GoalFull, GoalReasonShortSends
	case f.Now.Before(l.At) || f.Now.Sub(l.At) >= GoalCopyMaxAge:
		return GoalFull, GoalReasonAge
	}
	return GoalShort, GoalReasonUnchanged
}

// ConversationHash keeps a conversation identity out of the state file:
// only a short hash is recorded. Empty stays empty.
func ConversationHash(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("looptender-conversation\x00" + key))
	return hex.EncodeToString(sum[:8])
}

// shortGoalText is the fixed line that replaces an unchanged goal.
func shortGoalText(text, id string, revision uint64) string {
	return text + fmt.Sprintf("\n\nCurrent keepgoing goal: %s revision %d, unchanged since the last full copy this loop sent to this conversation. "+
		"Use that copy; if you no longer have it, the full copy can be read from this loop's private goal record.\n", id, revision)
}

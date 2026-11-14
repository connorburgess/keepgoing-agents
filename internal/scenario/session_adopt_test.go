package scenario

import (
	"strings"
	"testing"
	"time"
)

// adoptedSession is the fictional Claude Code conversation a person hands
// over from the IDE extension.
const adoptedSession = "00000000-0000-4000-8000-0000000000bb"

// TestSessionAdoptHandsAnIDEConversationToALoop: a person closes a Claude
// Code conversation in the IDE and hands it to a headless loop. The command
// says the conversation must be closed in the IDE and quotes Claude Code's
// own warning; the next check resumes exactly that conversation, and it is
// continued from then on. The timeline records the handover.
func TestSessionAdoptHandsAnIDEConversationToALoop(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.InitHeadless("claude", "await_user")
	w.Control("enable")
	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "the loop's own first turn", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "claude"})))

	// The loop has a conversation of its own, so it is left on purpose first.
	s = w.record("session adopt", w.Run("session", "adopt", adoptedSession, "-c", w.Config), nil)
	w.Check(s, "adopt refuses to replace the loop's own conversation", all(exit(s, 1), nothingSent(s)))
	if !strings.Contains(s.Result.Stderr, "run keepgoing session reset first") {
		w.Failf(s, "stderr %q", s.Result.Stderr)
	}
	s = w.record("session reset", w.Run("session", "reset", "-c", w.Config), nil)
	w.Check(s, "reset", all(exit(s, 0), nothingSent(s)))
	s = w.record("session adopt", w.Run("session", "adopt", adoptedSession, "-c", w.Config), nil)
	w.Check(s, "adopt", all(exit(s, 0), nothingSent(s), holds(s)))
	for _, want := range []string{"Close it in the IDE first", `"Using it in two places at once can mix up its messages."`} {
		if !strings.Contains(s.Result.Stdout, want) {
			w.Failf(s, "adopt output %q lacks %q", s.Result.Stdout, want)
		}
	}
	if !strings.Contains(s.Timeline.Raw, "session adopted from a terminal or scheduler") || strings.Contains(s.Timeline.Raw, adoptedSession) {
		w.Failf(s, "the timeline does not record the handover, or shows the identifier")
	}

	// The next check resumes the adopted conversation, and it stays the one
	// the loop continues.
	w.Commit("feat: fictional step before the handover")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "the next turn resumes the adopted conversation", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "claude", Mode: adoptedSession}), holds(s), noReports(s)))
	w.Commit("feat: fictional step in the adopted conversation")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	// The stand-in reports its own fixed session, which the loop then keeps:
	// a real Claude Code turn resumed with --resume reports the same one.
	w.Check(s, "the conversation is continued", all(action(s, "wake"), holds(s), noReports(s)))
}

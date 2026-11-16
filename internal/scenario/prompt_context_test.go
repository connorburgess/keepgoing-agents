package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// contextTag opens the supervision context block (loop.prompt_context).
const contextTag = "[keepgoing:context schema=1]"

// carriesContext says the step's one send carried the context block once,
// or did not carry it at all.
func carriesContext(s *Step, want bool) error {
	if len(s.Sent) != 1 {
		return fmt.Errorf("sent %s, want one send", describeSends(s.Sent))
	}
	n := strings.Count(s.Sent[0].Message, contextTag)
	if want && n != 1 || !want && n != 0 {
		return fmt.Errorf("%d context blocks in %q", n, s.Sent[0].Message)
	}
	return nil
}

// contextAfterMessage says the block comes after the rest of the message,
// with the session commands naming this loop's configuration.
func contextAfterMessage(s *Step, config string) error {
	m := s.Sent[0].Message
	i := strings.Index(m, contextTag)
	if i < 0 || !strings.Contains(m[:i], scenarioTask) || strings.Contains(m[i:], scenarioTask) {
		return fmt.Errorf("the block is not after the message: %q", m)
	}
	if !strings.Contains(m[i:], " context -c '"+config+"'\n") {
		return fmt.Errorf("the block's commands do not name %s: %q", config, m[i:])
	}
	return nil
}

// TestScenarioPromptContextBB: on a BB loop with loop.prompt_context the
// first wake carries the context after the fixed wake header and the task,
// the next wake does not, and a pause and resume sends it again.
func TestScenarioPromptContextBB(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.Init()
	w.EditConfig(func(loop map[string]any) { loop["prompt_context"] = true })
	primary := w.Thread(primaryThread)
	w.Control("enable")

	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first wake carries the context", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}), carriesContext(s, true)))
	if len(s.Sent) == 1 {
		if !strings.HasPrefix(s.Sent[0].Message, "Automatic looptender wake for loop example-loop: ") {
			w.Failf(s, "the wake header is not first: %q", s.Sent[0].Message)
		}
		w.Check(s, "the context comes last", contextAfterMessage(s, w.Config))
	}

	w.Commit("feat: fictional first step")
	primary.FinishTurn()
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "an unchanged context is not repeated", all(action(s, "wake"), carriesContext(s, false)))

	w.Control("pause")
	w.Control("resume")
	w.Commit("feat: fictional second step")
	primary.FinishTurn()
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "sent again after a resume", all(action(s, "wake"), carriesContext(s, true)))
}

// TestScenarioPromptContextHeadless: on a headless Claude Code loop the
// context goes into the first turn's prompt, with no wake header, is not
// repeated in the resumed session, and goes again into the turn that a
// session reset starts as a new conversation.
func TestScenarioPromptContextHeadless(t *testing.T) {
	t.Parallel()
	w := NewWorld(t, scenarioStart)
	w.InitHeadless("claude")
	w.EditConfig(func(loop map[string]any) { loop["prompt_context"] = true })
	w.Control("enable")

	w.Advance(time.Minute)
	s := w.Tick()
	w.Check(s, "first turn carries the context", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "claude"}), carriesContext(s, true)))
	if len(s.Sent) == 1 {
		if strings.Contains(s.Sent[0].Message, "Automatic looptender wake") {
			w.Failf(s, "a wake header went to a headless turn: %q", s.Sent[0].Message)
		}
		w.Check(s, "the context comes last", contextAfterMessage(s, w.Config))
	}

	w.Commit("feat: fictional first step")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "the resumed session is not sent it again", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "claude", Mode: headlessSession}), carriesContext(s, false)))

	s = w.record("session reset", w.Run("session", "reset", "-c", w.Config), nil)
	w.Check(s, "session reset", all(exit(s, 0), nothingSent(s)))
	w.Commit("feat: fictional second step")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "the new conversation is sent it", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "claude"}), carriesContext(s, true)))

	w.Commit("feat: fictional third step")
	w.Advance(5 * time.Minute)
	s = w.Tick()
	w.Check(s, "and not again in that conversation", all(action(s, "wake"), sentExactly(s, Send{Kind: "turn", Thread: "claude", Mode: headlessSession}), carriesContext(s, false)))
}

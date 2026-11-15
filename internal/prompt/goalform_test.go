package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

func TestChooseGoalFormTable(t *testing.T) {
	const id, other = "goal_0123456789abcdef0123456789abcdef", "goal_abcdef0123456789abcdef0123456789"
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	conv := ConversationHash("example-thread-1")
	rec := func(edit func(*state.GoalCopy)) *state.GoalCopy {
		g := &state.GoalCopy{Version: 1, At: now.Add(-time.Hour), Target: "primary", Conversation: conv, GoalID: id, GoalRevision: 3, ShortSends: 2}
		if edit != nil {
			edit(g)
		}
		return g
	}
	base := GoalFacts{Target: "primary", Conversation: conv, ConversationKnown: true, GoalID: id, GoalRevision: 3, Now: now}
	for _, tc := range []struct {
		name   string
		last   *state.GoalCopy
		facts  func(*GoalFacts)
		form   string
		reason string
	}{
		{"unchanged", rec(nil), nil, GoalShort, GoalReasonUnchanged},
		{"first send, no record", nil, nil, GoalFull, GoalReasonNoRecord},
		{"unreadable record", &state.GoalCopy{Version: 2}, nil, GoalFull, GoalReasonNoRecord},
		{"goal revised", rec(nil), func(f *GoalFacts) { f.GoalRevision = 4 }, GoalFull, GoalReasonRevision},
		{"other goal", rec(nil), func(f *GoalFacts) { f.GoalID = other }, GoalFull, GoalReasonRevision},
		{"target changed by handoff or switch-back", rec(nil), func(f *GoalFacts) { f.Target = "example-fallback" }, GoalFull, GoalReasonTarget},
		{"session reset starts a new conversation", rec(nil), func(f *GoalFacts) { f.Conversation = "" }, GoalFull, GoalReasonNewConversation},
		{"another conversation", rec(nil), func(f *GoalFacts) { f.Conversation = ConversationHash("example-thread-2") }, GoalFull, GoalReasonConversation},
		{"copy that started the conversation", rec(func(g *state.GoalCopy) { g.Conversation = "" }), nil, GoalShort, GoalReasonUnchanged},
		{"adapter reports no conversation", rec(func(g *state.GoalCopy) { g.Conversation = "" }), func(f *GoalFacts) { f.Conversation, f.ConversationKnown = "", false }, GoalShort, GoalReasonUnchanged},
		{"short sends at the limit", rec(func(g *state.GoalCopy) { g.ShortSends = GoalShortSendsMax }), nil, GoalFull, GoalReasonShortSends},
		{"short sends below the limit", rec(func(g *state.GoalCopy) { g.ShortSends = GoalShortSendsMax - 1 }), nil, GoalShort, GoalReasonUnchanged},
		{"copy too old", rec(func(g *state.GoalCopy) { g.At = now.Add(-GoalCopyMaxAge) }), nil, GoalFull, GoalReasonAge},
		{"copy just young enough", rec(func(g *state.GoalCopy) { g.At = now.Add(-GoalCopyMaxAge + time.Second) }), nil, GoalShort, GoalReasonUnchanged},
		{"copy in the future", rec(func(g *state.GoalCopy) { g.At = now.Add(time.Minute) }), nil, GoalFull, GoalReasonAge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			f.Last = tc.last
			if tc.facts != nil {
				tc.facts(&f)
			}
			if form, reason := ChooseGoalForm(f); form != tc.form || reason != tc.reason {
				t.Fatalf("got %s %s, want %s %s", form, reason, tc.form, tc.reason)
			}
		})
	}
}

func TestComposeWakeOnChangeOffIsByteIdentical(t *testing.T) {
	loop := config.Loop{Name: "example-loop", StateDir: t.TempDir(), Prompt: "Read the current authorized roadmap.", PromptGoal: true}
	if _, err := control.WriteGoal(loop.StateDir, loop.Name, "", control.GoalSpec{Title: "fictional-title", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}, "", 0); err != nil {
		t.Fatal(err)
	}
	want, err := ComposeWake(loop)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	got, err := ComposeWakeOnChange(loop, func(string, uint64) bool { called = true; return true })
	if err != nil || got != want || called {
		t.Fatalf("off: %q %v called=%v, want %q", got.Text, err, called, want.Text)
	}
	loop.PromptGoal, loop.PromptGoalOnChange = false, true // rejected by config validation; still never short
	if got, err := ComposeWakeOnChange(loop, func(string, uint64) bool { return true }); err != nil || got.Text != loop.Prompt || got.GoalForm != "" {
		t.Fatalf("without prompt_goal: %+v %v", got, err)
	}
}

func TestComposeWakeOnChangeShortLineIsFixed(t *testing.T) {
	loop := config.Loop{Name: "example-loop", StateDir: t.TempDir(), Prompt: "Read the current authorized roadmap.", PromptGoal: true, PromptGoalOnChange: true}
	g, err := control.WriteGoal(loop.StateDir, loop.Name, "", control.GoalSpec{Title: "fictional-private-title", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	full, err := ComposeWakeOnChange(loop, func(string, uint64) bool { return false })
	if err != nil || full.GoalForm != GoalFull || !strings.Contains(full.Text, "fictional-private-title") {
		t.Fatalf("full: %+v %v", full, err)
	}
	var gotID string
	var gotRev uint64
	short, err := ComposeWakeOnChange(loop, func(id string, rev uint64) bool { gotID, gotRev = id, rev; return true })
	want := loop.Prompt + "\n\nCurrent keepgoing goal: " + g.ID + " revision 1, unchanged since the last full copy this loop sent to this conversation. " +
		"Use that copy; if you no longer have it, the full copy can be read from this loop's private goal record.\n"
	if err != nil || short.Text != want || short.GoalForm != GoalShort || short.GoalID != g.ID || short.GoalRevision != 1 || gotID != g.ID || gotRev != 1 {
		t.Fatalf("short: %+v %v", short, err)
	}
	if len(short.Text) >= len(full.Text) {
		t.Fatalf("short %d bytes, full %d", len(short.Text), len(full.Text))
	}
}

func TestConversationHash(t *testing.T) {
	if ConversationHash("") != "" {
		t.Fatal("empty key must stay empty")
	}
	h := ConversationHash("example-thread-1")
	if len(h) != 16 || strings.Contains(h, "example") || h == ConversationHash("example-thread-2") {
		t.Fatal(h)
	}
	if !(state.GoalCopy{Version: 1, At: time.Unix(1, 0), Target: "primary", Conversation: h, GoalID: "goal_0123456789abcdef0123456789abcdef", GoalRevision: 1}).Valid() {
		t.Fatal("hash not accepted by the record")
	}
}

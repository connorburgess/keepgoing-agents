package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPromptSendReloadAndMalformedEvidence(t *testing.T) {
	dir := t.TempDir()
	p := PromptSend{Version: 1, CheckAt: time.Now().UTC(), Target: "primary", Action: "wake", Mode: "auto", Outcome: "adapter_returned", GoalID: "goal_0123456789abcdef0123456789abcdef", GoalRevision: 1, Bytes: 1200, GoalForm: "short"}
	for _, mutate := range []func(*PromptSend){
		nil,
		func(p *PromptSend) { p.Bytes = -1 },
		func(p *PromptSend) { p.Bytes = MaxPromptBytes + 1 },
		func(p *PromptSend) { p.GoalForm = "partial" },
		func(p *PromptSend) { p.GoalID, p.GoalRevision = "", 0 }, // a form without a goal
		func(p *PromptSend) { p.Version = 2 },
		func(p *PromptSend) { p.GoalID = "private-content" },
		func(p *PromptSend) { p.GoalRevision = 0 },
		func(p *PromptSend) { p.GoalRevision = 129 },
		func(p *PromptSend) { p.Outcome = "delivered" },
		func(p *PromptSend) { p.Mode = "unknown" },
		func(p *PromptSend) { p.Action = "private-content" },
	} {
		candidate := p
		if mutate != nil {
			mutate(&candidate)
		}
		if err := Save(dir, File{LastPromptSend: &candidate, Reprompts: 3}); err != nil {
			t.Fatal(err)
		}
		st, err := Load(dir)
		if err != nil || st.Reprompts != 3 {
			t.Fatalf("load changed policy state: %+v %v", st, err)
		}
		if mutate == nil {
			if st.LastPromptSend == nil || *st.LastPromptSend != p {
				t.Fatalf("round trip: %+v", st.LastPromptSend)
			}
		} else if st.LastPromptSend != nil {
			t.Fatalf("malformed evidence exposed: %+v", st.LastPromptSend)
		}
	}
	if err := Save(dir, File{Reprompts: 3}); err != nil {
		t.Fatal(err)
	}
	st, err := Load(dir)
	if err != nil || st.LastPromptSend != nil || st.Reprompts != 3 {
		t.Fatalf("legacy state: %+v %v", st, err)
	}
}

func TestMalformedPromptTypesDoNotBlockPolicyState(t *testing.T) {
	dir := t.TempDir()
	for _, raw := range []string{`"private-text"`, `[]`, `{"version":"private-text"}`, `{"version":1,"checkAt":"invalid-time"}`, `{"goalRevision":-1}`} {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"reprompts":3,"lastPromptSend":`+raw+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		st, err := Load(dir)
		if err != nil || st.Reprompts != 3 || st.LastPromptSend != nil {
			t.Fatalf("diagnostic field blocked state: %+v %v", st, err)
		}
	}
}

func TestGoalCopyReloadAndMalformedRecord(t *testing.T) {
	dir := t.TempDir()
	g := GoalCopy{Version: 1, At: time.Now().UTC(), Target: "primary", Conversation: "0123456789abcdef", GoalID: "goal_0123456789abcdef0123456789abcdef", GoalRevision: 2, ShortSends: 3}
	for _, mutate := range []func(*GoalCopy){
		nil,
		func(g *GoalCopy) { g.Conversation = "" },
		func(g *GoalCopy) { g.Version = 2 },
		func(g *GoalCopy) { g.Conversation = "example-session-id" },
		func(g *GoalCopy) { g.GoalID = "private-content" },
		func(g *GoalCopy) { g.GoalRevision = 0 },
		func(g *GoalCopy) { g.ShortSends = -1 },
		func(g *GoalCopy) { g.Target = "" },
		func(g *GoalCopy) { g.At = time.Time{} },
	} {
		candidate := g
		if mutate != nil {
			mutate(&candidate)
		}
		if err := Save(dir, File{GoalCopy: &candidate, Reprompts: 3}); err != nil {
			t.Fatal(err)
		}
		st, err := Load(dir)
		if err != nil || st.Reprompts != 3 {
			t.Fatalf("load changed policy state: %+v %v", st, err)
		}
		if candidate.Valid() != (st.GoalCopy != nil) || (st.GoalCopy != nil && *st.GoalCopy != candidate) {
			t.Fatalf("record %+v loaded as %+v", candidate, st.GoalCopy)
		}
	}
}

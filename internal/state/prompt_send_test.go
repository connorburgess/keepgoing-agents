package state

import (
	"testing"
	"time"
)

func TestPromptSendReloadAndMalformedEvidence(t *testing.T) {
	dir := t.TempDir()
	p := PromptSend{Version: 1, CheckAt: time.Now().UTC(), Target: "primary", Action: "wake", Mode: "auto", Outcome: "adapter_returned", GoalID: "goal_0123456789abcdef0123456789abcdef", GoalRevision: 1}
	for _, mutate := range []func(*PromptSend){
		nil,
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

package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// A brief sent to a different conversation says how many child conversations
// were left working, from a count alone, and that nobody supervises them.
func TestBriefSaysWhatChildrenWereLeftWorking(t *testing.T) {
	const always = "They are not supervised by this loop and were not moved to you."
	for _, tc := range []struct {
		name     string
		children ChildrenLeft
		want     string // "" means the brief says nothing about children
	}{
		{"none, listing complete", ChildrenLeft{}, ""},
		{"one", ChildrenLeft{Working: 1}, "1 child conversation started by the previous conversation was still working"},
		{"several", ChildrenLeft{Working: 4}, "4 child conversations started by the previous conversation were still working"},
		{"at the cap", ChildrenLeft{Working: MaxChildrenNamed}, "200 child conversations started"},
		{"over the cap", ChildrenLeft{Working: 100000}, "more than 200 child conversations started"},
		{"listing failed", ChildrenLeft{Unverified: true}, "could not list them, so their number is unknown"},
		{"listing incomplete", ChildrenLeft{Working: 3, Unverified: true}, "at least 3 child conversation(s)"},
		{"incomplete and over the cap", ChildrenLeft{Working: 100000, Unverified: true}, "at least 200 child conversation(s)"},
		{"negative count is nothing", ChildrenLeft{Working: -1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := tc.children.Line()
			if (tc.want == "") != (line == "") || !strings.Contains(line, tc.want) {
				t.Fatalf("line %q, want it to contain %q", line, tc.want)
			}
			if line != "" && (!strings.Contains(line, always) || strings.Contains(line, "\n") || len(line) > 500 || strings.Contains(line, "100000")) {
				t.Fatalf("line is not the bounded single sentence group: %q", line)
			}
			facts := Facts{Loop: "example-loop", At: time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC), From: "primary", To: "codex-main", Reason: "rate_limit", Children: tc.children}
			// Generated brief: the line is its own paragraph, before the steps.
			got, err := Brief(config.Loop{Name: "example-loop", Prompt: "Ship the fictional feature."}, facts)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, "Child conversations:") != (line != "") || !strings.Contains(got, line) {
				t.Fatalf("generated brief:\n%s", got)
			}
			if line != "" && !strings.Contains(got, "moments ago.\n\n"+line+"\n\nBefore anything else:") {
				t.Fatalf("line is not its own paragraph:\n%s", got)
			}
			// A handoff file written in advance cannot hold this fact: the line
			// follows its text, which is otherwise unchanged.
			p := filepath.Join(t.TempDir(), "HANDOFF.md")
			if err := os.WriteFile(p, []byte("Custom brief for the fictional loop.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err = Brief(config.Loop{Prompt: "goal", HandoffFile: p}, facts)
			want := "Custom brief for the fictional loop."
			if line != "" {
				want += "\n\n" + line
			}
			if err != nil || got != want {
				t.Fatalf("custom brief %q, want %q (%v)", got, want, err)
			}
		})
	}
}
